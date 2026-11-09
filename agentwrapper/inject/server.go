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

	mu     sync.RWMutex
	inject func(prompt string) error

	wg        sync.WaitGroup
	closeOnce sync.Once
}

// Listen removes a stale socket file at path, listens there (mode 0600) and
// starts serving. Until SetInject is called every request is answered
// not_injectable.
func Listen(path string) (*Server, error) {
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
	s := &Server{path: path, ln: ln}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// SetInject installs the injection function. It is safe for concurrent use.
func (s *Server) SetInject(fn func(prompt string) error) {
	s.mu.Lock()
	s.inject = fn
	s.mu.Unlock()
}

// Close stops the listener, removes the socket and waits for handlers.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		_ = s.ln.Close()
		_ = os.Remove(s.path)
	})
	s.wg.Wait()
	return nil
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
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
			s.handle(conn)
		}()
	}
}

func (s *Server) handle(conn net.Conn) {
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
	if err := fn(req.Prompt); err != nil {
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
