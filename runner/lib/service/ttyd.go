package service

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// ttydChunk is the largest output frame.
const ttydChunk = 32 * 1024

var sessionKeyRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// ttydSessions tracks the live terminal per session key: a new connection for
// a key replaces (kills) the previous one, like a single-client ttyd.
type ttydSessions struct {
	mu   sync.Mutex
	live map[string]*ttydSession
}

type ttydSession struct {
	cmd    *exec.Cmd
	done   chan struct{}
	marker string // ASGARD_TTYD=<id> in the environment of every process of the session
}

func (t *ttydSessions) replace(key string, ses *ttydSession) {
	t.mu.Lock()
	old := t.live[key]
	if t.live == nil {
		t.live = make(map[string]*ttydSession)
	}
	t.live[key] = ses
	t.mu.Unlock()
	if old != nil {
		old.kill()
		<-old.done
	}
}

func (t *ttydSessions) remove(key string, ses *ttydSession) {
	t.mu.Lock()
	if t.live[key] == ses {
		delete(t.live, key)
	}
	t.mu.Unlock()
}

func (s *ttydSession) alive() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// kill ends the shell and everything it started: SIGHUP to every process of
// the session, then SIGKILL after a grace period. Members are found by
// session id, by the per-session environment marker (which survives setsid
// and double forks, unless a job scrubs its environment) and by descent from
// the shell, so background jobs in their own process groups or sessions go
// too.
func (s *ttydSession) kill() {
	if s.cmd.Process == nil {
		return
	}
	pid := s.cmd.Process.Pid
	// The /proc sweeps run off the caller's goroutine: they cost time
	// proportional to the number of processes on the host, and the stream
	// handler must not delay its final frame / status behind them.
	aliveNow := s.alive()
	go func() {
		signalSession(pid, aliveNow, s.marker, syscall.SIGHUP)
		select {
		case <-s.done:
			time.Sleep(time.Second)
		case <-time.After(2 * time.Second):
		}
		signalSession(pid, s.alive(), s.marker, syscall.SIGKILL)
		_ = s.cmd.Process.Kill()
	}()
}

// signalSession signals every process belonging to the terminal session.
func signalSession(shellPid int, shellAlive bool, marker string, sig syscall.Signal) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		if shellAlive {
			_ = syscall.Kill(-shellPid, sig)
		}
		return
	}
	type proc struct {
		pid, ppid, sid int
		marked         bool // carries the session's environment marker
	}
	var procs []proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid pgrp session ...; comm may contain spaces.
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(stat[i+1:]))
		if len(f) < 4 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		sid, _ := strconv.Atoi(f[3])
		// A shell's own session and descendants are matched without the
		// environment, so only read it for processes outside the session.
		inSession := shellAlive && sid == shellPid
		var marked bool
		if !inSession {
			env, _ := os.ReadFile("/proc/" + e.Name() + "/environ")
			marked = bytes.Contains(env, []byte("\x00"+marker+"\x00")) || bytes.HasPrefix(env, []byte(marker+"\x00"))
		}
		procs = append(procs, proc{pid, ppid, sid, marked})
	}
	// A reaped shell's pid may be reused: then only marker/session matches count.
	members := map[int]bool{}
	if shellAlive {
		members[shellPid] = true
	}
	for changed := true; changed; {
		changed = false
		for _, p := range procs {
			if members[p.pid] {
				continue
			}
			if shellAlive && p.sid == shellPid || members[p.ppid] || p.marked {
				members[p.pid] = true
				changed = true
			}
		}
	}
	for pid := range members {
		_ = syscall.Kill(pid, sig)
	}
	if shellAlive {
		_ = syscall.Kill(-shellPid, sig)
	}
}

// shellPath picks the shell: the configured one, else fish, else bash.
func (s *Service) shellPath() string {
	if s.shell != "" {
		return s.shell
	}
	for _, name := range []string{"fish", "bash", "sh"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return "sh"
}

func ttydSize(cols, rows uint32) *pty.Winsize {
	c, r := cols, rows
	if c == 0 || c > 1000 {
		c = 80
	}
	if r == 0 || r > 1000 {
		r = 24
	}
	return &pty.Winsize{Cols: uint16(c), Rows: uint16(r)}
}

// Ttyd implements RunnerService: an interactive shell on a pty. The first
// frame opens the session; input/resize frames drive it, output frames carry
// the terminal bytes and an exit frame ends the stream.
func (s *Service) Ttyd(stream runnerpb.RunnerService_TtydServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return status.Error(codes.InvalidArgument, "first frame must be open")
	}
	if !sessionKeyRe.MatchString(open.GetSessionKey()) {
		return status.Error(codes.InvalidArgument, "invalid session key")
	}

	// The start directory is opened beneath an allowed root and handed to the
	// shell as /proc/self/fd/N; no directory means the runner user's home.
	var dirPath string
	var dirFile *os.File
	ref := open.GetWorkingDir()
	hasDir := ref != nil && (ref.GetRelOrPath() != "" ||
		ref.GetScope() == runnerpb.FileScope_FILE_SCOPE_TMP || ref.GetScope() == runnerpb.FileScope_FILE_SCOPE_SESSION)
	if hasDir {
		rd, err := runDirString(open.GetChatId(), ref)
		if err != nil {
			return grpcErr(err)
		}
		dirFile, err = s.fsFor(open.GetChatId()).OpenWorkDir(rd)
		if err != nil {
			return grpcErr(err)
		}
		defer func() { _ = dirFile.Close() }()
		dirPath = "/proc/self/fd/" + strconv.Itoa(int(dirFile.Fd()))
	} else if home, err := os.UserHomeDir(); err == nil {
		dirPath = home
	}

	cmd := exec.Command(s.shellPath())
	cmd.Dir = dirPath
	var idb [12]byte
	_, _ = rand.Read(idb[:])
	marker := "ASGARD_TTYD=" + hex.EncodeToString(idb[:])
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", marker)
	ptmx, err := pty.StartWithSize(cmd, ttydSize(open.GetCols(), open.GetRows()))
	if err != nil {
		return status.Errorf(codes.Internal, "failed to start shell: %v", err)
	}

	ses := &ttydSession{cmd: cmd, done: make(chan struct{}), marker: marker}
	s.ttyd.replace(open.GetSessionKey(), ses)

	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
		close(ses.done)
	}()
	defer s.ttyd.remove(open.GetSessionKey(), ses)
	// Whatever the shell left running dies with the session.
	defer ses.kill()

	// Output pump. It ends when the shell exits and the pty drains (EIO).
	outDone := make(chan error, 1)
	go func() {
		buf := make([]byte, ttydChunk)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if serr := stream.Send(&runnerpb.TtydFrame{Payload: &runnerpb.TtydFrame_Output{Output: append([]byte(nil), buf[:n]...)}}); serr != nil {
					outDone <- serr
					return
				}
			}
			if err != nil {
				outDone <- nil
				return
			}
		}
	}()

	// Input pump: ends with the client (EOF/cancel), which kills the shell.
	inDone := make(chan struct{})
	go func() {
		defer close(inDone)
		for {
			f, err := stream.Recv()
			if err != nil {
				if err != io.EOF && stream.Context().Err() == nil {
					log.Debug().Err(err).Msg("ttyd input stream ended")
				}
				return
			}
			switch p := f.GetPayload().(type) {
			case *runnerpb.TtydFrame_Input:
				if _, err := ptmx.Write(p.Input); err != nil {
					return
				}
			case *runnerpb.TtydFrame_Resize:
				_ = pty.Setsize(ptmx, ttydSize(p.Resize.GetCols(), p.Resize.GetRows()))
			}
		}
	}()

	var sendErr error
	select {
	case <-ses.done:
		// Shell exited: drain the remaining output, then report.
		select {
		case sendErr = <-outDone:
		case <-time.After(2 * time.Second):
			// A leftover process holds the pty open: stop reading.
			_ = ptmx.Close()
			sendErr = <-outDone
		}
	case <-inDone:
		ses.kill()
		<-ses.done
		_ = ptmx.Close()
		drain(outDone)
		return nil
	case sendErr = <-outDone:
		ses.kill()
		<-ses.done
	case <-stream.Context().Done():
		ses.kill()
		<-ses.done
		_ = ptmx.Close()
		drain(outDone)
		return nil
	}
	_ = ptmx.Close()
	if sendErr != nil {
		return sendErr
	}
	code := 0
	if err := <-waitErr; err != nil {
		code = -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	return stream.Send(&runnerpb.TtydFrame{Payload: &runnerpb.TtydFrame_Exit{Exit: &runnerpb.TtydExit{Code: int32(code)}}})
}

// drain waits for the output pump to finish.
func drain(ch <-chan error) {
	for range ch {
		return
	}
}
