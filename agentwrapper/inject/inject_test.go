package inject

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

func newServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), SocketName)
	s, err := Listen(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestSendOK(t *testing.T) {
	s, path := newServer(t)
	var got string
	s.SetInject(func(p string) error { got = p; return nil })
	require.NoError(t, Send(context.Background(), path, "hello"))
	assert.Equal(t, "hello", got)
}

func TestSocketMode(t *testing.T) {
	_, path := newServer(t)
	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
}

func TestStaleSocketRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), SocketName)
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	s, err := Listen(path)
	require.NoError(t, err)
	require.NoError(t, s.Close())
}

func TestNotInjectableBeforeSetInject(t *testing.T) {
	_, path := newServer(t)
	err := Send(context.Background(), path, "x")
	assert.ErrorIs(t, err, types.ErrNotInjectable)
}

func TestNotInjectableFromFunc(t *testing.T) {
	s, path := newServer(t)
	s.SetInject(func(string) error { return errors.Join(errors.New("closed"), types.ErrNotInjectable) })
	assert.ErrorIs(t, Send(context.Background(), path, "x"), types.ErrNotInjectable)
}

func TestInternalError(t *testing.T) {
	s, path := newServer(t)
	s.SetInject(func(string) error { return errors.New("boom") })
	err := Send(context.Background(), path, "x")
	assert.ErrorIs(t, err, ErrInternal)
	assert.NotErrorIs(t, err, types.ErrNotInjectable)
	assert.Contains(t, err.Error(), "boom")
}

func TestBigMessage(t *testing.T) {
	s, path := newServer(t)
	big := strings.Repeat("a", 1<<20)
	var got string
	s.SetInject(func(p string) error { got = p; return nil })
	require.NoError(t, Send(context.Background(), path, big))
	assert.Equal(t, big, got)
}

func rawExchange(t *testing.T, path, payload string) string {
	t.Helper()
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.Write([]byte(payload))
	require.NoError(t, err)
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	return line
}

func TestBadJSON(t *testing.T) {
	s, path := newServer(t)
	called := false
	s.SetInject(func(string) error { called = true; return nil })
	line := rawExchange(t, path, "not json\n")
	assert.Contains(t, line, `"bad_request"`)
	assert.False(t, called)
}

func TestTooLarge(t *testing.T) {
	_, path := newServer(t)
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	go func() {
		chunk := []byte(strings.Repeat("a", 1<<20))
		for i := 0; i < 18; i++ {
			if _, err := conn.Write(chunk); err != nil {
				return
			}
		}
	}()
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	assert.Contains(t, line, `"bad_request"`)
}

func TestNoListener(t *testing.T) {
	dir := t.TempDir()
	err := Send(context.Background(), filepath.Join(dir, "missing.sock"), "x")
	assert.ErrorIs(t, err, types.ErrNotInjectable)
	assert.ErrorIs(t, err, ErrUnavailable)

	// Socket file present but nobody listening: connection refused.
	path := filepath.Join(dir, "stale.sock")
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, ln.Close())
	err = Send(context.Background(), path, "x")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.ErrorIs(t, err, types.ErrNotInjectable)
}

func TestClientContextCancel(t *testing.T) {
	s, path := newServer(t)
	release := make(chan struct{})
	s.SetInject(func(string) error { <-release; return nil })
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Send(ctx, path, "x") }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("Send did not return after cancel")
	}
}

func TestConcurrent(t *testing.T) {
	s, path := newServer(t)
	var mu sync.Mutex
	seen := map[string]bool{}
	s.SetInject(func(p string) error {
		mu.Lock()
		seen[p] = true
		mu.Unlock()
		return nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := strings.Repeat("m", i+1)
			assert.NoError(t, Send(context.Background(), path, p))
		}()
	}
	wg.Wait()
	assert.Len(t, seen, 50)
}

func TestSetInjectRace(t *testing.T) {
	s, path := newServer(t)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = Send(context.Background(), path, "x")
		}
	}()
	go func() {
		defer wg.Done()
		time.Sleep(time.Millisecond)
		s.SetInject(func(string) error { return nil })
	}()
	wg.Wait()
}

func TestCloseCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), SocketName)
	s, err := Listen(path)
	require.NoError(t, err)
	s.SetInject(func(string) error { return nil })
	require.NoError(t, Send(context.Background(), path, "x"))

	done := make(chan struct{})
	go func() { _ = s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close hung")
	}
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
	// Idempotent.
	assert.NoError(t, s.Close())
}

func TestSocketPathEnv(t *testing.T) {
	t.Setenv(EnvSock, "/some/where.sock")
	assert.Equal(t, "/some/where.sock", SocketPath())
}

func TestControlRelay(t *testing.T) {
	s, path := newServer(t)
	cpath := ControlPath(path)
	type call struct {
		hold bool
		idle time.Duration
	}
	var mu sync.Mutex
	var calls []call
	s.SetControl(func(hold bool, idle time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{hold, idle})
		return nil
	})
	s.SetInject(func(string) error { t.Fatal("a control must not inject a prompt"); return nil })
	require.NoError(t, SendControl(context.Background(), cpath, ControlHold, 90*time.Second))
	require.NoError(t, SendControl(context.Background(), cpath, ControlRelease, 0))
	assert.Equal(t, []call{{true, 90 * time.Second}, {false, 0}}, calls)
	assert.ErrorIs(t, SendControl(context.Background(), cpath, "bogus", 0), ErrBadRequest)
}

func TestControlNotInjectable(t *testing.T) {
	s, path := newServer(t)
	cpath := ControlPath(path)
	assert.ErrorIs(t, SendControl(context.Background(), cpath, ControlHold, 0), types.ErrNotInjectable, "no control installed")
	s.SetControl(func(bool, time.Duration) error { return types.ErrNotInjectable })
	assert.ErrorIs(t, SendControl(context.Background(), cpath, ControlRelease, 0), types.ErrNotInjectable)
	s.SetControl(func(bool, time.Duration) error { return errors.New("boom") })
	assert.ErrorIs(t, SendControl(context.Background(), cpath, ControlRelease, 0), ErrInternal)
}

// A control is never accepted on inject.sock (an old aw would inject it as an
// empty prompt) and an injection never on control.sock; with no control
// socket (an old aw) SendControl reports ErrUnavailable.
func TestControlSeparateSocket(t *testing.T) {
	s, path := newServer(t)
	s.SetInject(func(string) error { t.Fatal("nothing may be injected"); return nil })
	s.SetControl(func(bool, time.Duration) error { t.Fatal("no control may be applied"); return nil })
	assert.ErrorIs(t, SendControl(context.Background(), path, ControlHold, 0), ErrBadRequest, "control over inject.sock")
	assert.ErrorIs(t, Send(context.Background(), ControlPath(path), "hi"), ErrBadRequest, "injection over control.sock")

	oldDir := t.TempDir()
	err := SendControl(context.Background(), filepath.Join(oldDir, ControlSocketName), ControlHold, 0)
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.ErrorIs(t, err, types.ErrNotInjectable)
}

func TestCloseRemovesControlSocket(t *testing.T) {
	s, path := newServer(t)
	require.NoError(t, s.Close())
	_, err := os.Stat(ControlPath(path))
	assert.True(t, os.IsNotExist(err))
}

func TestSendGenReturnsIdleGen(t *testing.T) {
	s, path := newServer(t)
	s.SetInjectGen(func(string) (uint64, error) { return 7, nil })
	gen, err := SendGen(context.Background(), path, "hello")
	require.NoError(t, err)
	assert.Equal(t, uint64(7), gen)

	// A plain injection function reports no generation.
	s.SetInject(func(string) error { return nil })
	gen, err = SendGen(context.Background(), path, "hello")
	require.NoError(t, err)
	assert.Zero(t, gen)
}
