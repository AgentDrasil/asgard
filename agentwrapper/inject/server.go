package inject

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// readTimeout keeps a stuck client from holding a handler goroutine.
const readTimeout = 10 * time.Second

// Server accepts injection requests on a unix socket.
type Server struct {
	path string
	ln   net.Listener
	// cpath / cln: the control socket (see ControlPath), a separate endpoint
	// so that an aw that predates controls never receives one.
	cpath string
	cln   net.Listener

	mu      sync.RWMutex
	inject  func(prompt string) (uint64, error)
	control func(hold bool, idle time.Duration) error

	wg        sync.WaitGroup
	closeOnce sync.Once
}

// listenSocket removes a stale socket file at path and listens there (mode
// 0600).
func listenSocket(path string) (net.Listener, error) {
	// A leftover file from a crashed run would make bind fail.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("removing stale socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return ln, nil
}

// Listen listens on the injection socket at path and on the control socket
// next to it (ControlPath(path)) and starts serving. Until SetInject is
// called every injection is answered not_injectable; likewise controls until
// SetControl.
func Listen(path string) (*Server, error) {
	ln, err := listenSocket(path)
	if err != nil {
		return nil, err
	}
	cpath := ControlPath(path)
	cln, err := listenSocket(cpath)
	if err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, err
	}
	s := &Server{path: path, ln: ln, cpath: cpath, cln: cln}
	s.wg.Add(2)
	go s.acceptLoop(ln, false)
	go s.acceptLoop(cln, true)
	return s, nil
}

// SetInject installs the injection function. It is safe for concurrent use.
func (s *Server) SetInject(fn func(prompt string) error) {
	s.SetInjectGen(func(prompt string) (uint64, error) { return 0, fn(prompt) })
}

// SetInjectGen is SetInject for an injection function that also returns the
// injector's idle generation after accepting the message; it goes into the
// reply (see response.IdleGen).
func (s *Server) SetInjectGen(fn func(prompt string) (uint64, error)) {
	s.mu.Lock()
	s.inject = fn
	s.mu.Unlock()
}

// SetControl installs the function that applies a hold (hold true, idle is
// the cap on an idle hold, 0 for the default) or a release. Until it is set,
// controls are answered not_injectable.
func (s *Server) SetControl(fn func(hold bool, idle time.Duration) error) {
	s.mu.Lock()
	s.control = fn
	s.mu.Unlock()
}

// Close stops the listener, removes the socket and waits for handlers.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		_ = s.ln.Close()
		_ = s.cln.Close()
		_ = os.Remove(s.path)
		_ = os.Remove(s.cpath)
	})
	s.wg.Wait()
	return nil
}

func (s *Server) acceptLoop(ln net.Listener, control bool) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Transient error (e.g. EMFILE): back off instead of spinning.
			time.Sleep(50 * time.Millisecond)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn, control)
		}()
	}
}

// handle serves one request. Injections are only accepted on the injection
// socket and controls only on the control socket.
func (s *Server) handle(conn net.Conn, control bool) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))

	line, err := readLine(bufio.NewReader(conn))
	if err != nil {
		reply(conn, response{Reason: ReasonBadRequest, Error: err.Error()})
		return
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		reply(conn, response{Reason: ReasonBadRequest, Error: "invalid JSON: " + err.Error()})
		return
	}

	if control != (req.Control != "") {
		reply(conn, response{Reason: ReasonBadRequest, Error: "request does not belong on this socket"})
		return
	}
	if control {
		s.handleControl(conn, req)
		return
	}

	s.mu.RLock()
	fn := s.inject
	s.mu.RUnlock()
	if fn == nil {
		reply(conn, response{Reason: ReasonNotInjectable, Error: types.ErrNotInjectable.Error()})
		return
	}
	// The injection may block on a pipe write; the read deadline no longer
	// matters, and the reply must not be cut short by it.
	_ = conn.SetDeadline(time.Time{})
	gen, err := fn(req.Prompt)
	if err != nil {
		if errors.Is(err, types.ErrNotInjectable) {
			reply(conn, response{Reason: ReasonNotInjectable, Error: err.Error()})
		} else {
			reply(conn, response{Reason: ReasonInternal, Error: err.Error()})
		}
		return
	}
	reply(conn, response{OK: true, IdleGen: gen})
}

func (s *Server) handleControl(conn net.Conn, req request) {
	var hold bool
	switch req.Control {
	case ControlHold:
		hold = true
	case ControlRelease:
	default:
		reply(conn, response{Reason: ReasonBadRequest, Error: "unknown control " + req.Control})
		return
	}
	s.mu.RLock()
	fn := s.control
	s.mu.RUnlock()
	if fn == nil {
		reply(conn, response{Reason: ReasonNotInjectable, Error: types.ErrNotInjectable.Error()})
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if err := fn(hold, time.Duration(req.IdleSeconds)*time.Second); err != nil {
		if errors.Is(err, types.ErrNotInjectable) {
			reply(conn, response{Reason: ReasonNotInjectable, Error: err.Error()})
		} else {
			reply(conn, response{Reason: ReasonInternal, Error: err.Error()})
		}
		return
	}
	reply(conn, response{OK: true})
}

// readLine reads one newline-terminated line of at most maxRequestBytes.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxRequestBytes {
			return nil, errors.New("request too large")
		}
		switch {
		case err == nil:
			return buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buf) > 0:
			// Tolerate a missing trailing newline.
			return buf, nil
		default:
			return nil, err
		}
	}
}

func reply(conn net.Conn, resp response) {
	b, _ := json.Marshal(resp)
	_ = conn.SetWriteDeadline(time.Now().Add(readTimeout))
	_, _ = conn.Write(append(b, '\n'))
}
