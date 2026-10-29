package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// fakeShell writes a script standing in for the login shell: it reports its
// working directory and size, echoes one line and exits 3.
func fakeShell(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fakeshell")
	script := "#!/bin/sh\nprintf 'cwd=%s\\n' \"$(pwd -P)\"\nstty size\nread line\nprintf 'got:%s\\n' \"$line\"\nexit 3\n"
	require.NoError(t, os.WriteFile(p, []byte(script), 0o755))
	return p
}

type ttydClient struct {
	t      *testing.T
	st     grpc.BidiStreamingClient[runnerpb.TtydFrame, runnerpb.TtydFrame]
	mu     sync.Mutex
	out    strings.Builder
	exit   chan int32
	closed chan struct{}
	err    error
}

func openTtyd(t *testing.T, cl runnerpb.RunnerServiceClient, open *runnerpb.TtydOpen) *ttydClient {
	t.Helper()
	st, err := cl.Ttyd(t.Context())
	require.NoError(t, err)
	require.NoError(t, st.Send(&runnerpb.TtydFrame{Payload: &runnerpb.TtydFrame_Open{Open: open}}))
	c := &ttydClient{t: t, st: st, exit: make(chan int32, 1), closed: make(chan struct{})}
	go func() {
		defer close(c.closed)
		for {
			f, err := st.Recv()
			if err != nil {
				c.err = err
				return
			}
			switch p := f.GetPayload().(type) {
			case *runnerpb.TtydFrame_Output:
				c.mu.Lock()
				c.out.Write(p.Output)
				c.mu.Unlock()
			case *runnerpb.TtydFrame_Exit:
				c.exit <- p.Exit.GetCode()
			}
		}
	}()
	return c
}

func (c *ttydClient) output() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String()
}

func (c *ttydClient) waitFor(substr string) {
	c.t.Helper()
	require.Eventually(c.t, func() bool { return strings.Contains(c.output(), substr) }, 5*time.Second, 10*time.Millisecond, "output: %q", c.output())
}

func TestTtydFramesWithFakeShell(t *testing.T) {
	cl, ws := startService(t, Options{Shell: fakeShell(t)})
	c := openTtyd(t, cl, &runnerpb.TtydOpen{SessionKey: "agent-x", ChatId: chat, WorkingDir: absRef(ws), Cols: 100, Rows: 30})

	realWS, err := filepath.EvalSymlinks(ws)
	require.NoError(t, err)
	c.waitFor("cwd=" + realWS)
	c.waitFor("30 100")

	require.NoError(t, c.st.Send(&runnerpb.TtydFrame{Payload: &runnerpb.TtydFrame_Resize{Resize: &runnerpb.TtydResize{Cols: 120, Rows: 40}}}))
	require.NoError(t, c.st.Send(&runnerpb.TtydFrame{Payload: &runnerpb.TtydFrame_Input{Input: []byte("hello\n")}}))
	c.waitFor("got:hello")
	select {
	case code := <-c.exit:
		assert.EqualValues(t, 3, code)
	case <-time.After(5 * time.Second):
		t.Fatal("no exit frame")
	}
	<-c.closed
	assert.Equal(t, "EOF", c.err.Error())
}

func TestTtydDefaultDirIsHome(t *testing.T) {
	cl, _ := startService(t, Options{Shell: fakeShell(t)})
	c := openTtyd(t, cl, &runnerpb.TtydOpen{SessionKey: "sidebar"})
	home, err := filepath.EvalSymlinks(os.Getenv("HOME"))
	require.NoError(t, err)
	c.waitFor("cwd=" + home)
	require.NoError(t, c.st.Send(&runnerpb.TtydFrame{Payload: &runnerpb.TtydFrame_Input{Input: []byte("x\n")}}))
	assert.EqualValues(t, 3, <-c.exit)
}

func TestTtydRejectsBadOpens(t *testing.T) {
	cl, ws := startService(t, Options{Shell: fakeShell(t)})
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "escape")))

	tests := []struct {
		name string
		open *runnerpb.TtydOpen
		code codes.Code
	}{
		{"bad key", &runnerpb.TtydOpen{SessionKey: "../x"}, codes.InvalidArgument},
		{"empty key", &runnerpb.TtydOpen{}, codes.InvalidArgument},
		{"outside dir", &runnerpb.TtydOpen{SessionKey: "k", ChatId: chat, WorkingDir: absRef(outside)}, codes.PermissionDenied},
		{"etc", &runnerpb.TtydOpen{SessionKey: "k", ChatId: chat, WorkingDir: absRef("/etc")}, codes.PermissionDenied},
		{"symlink escape", &runnerpb.TtydOpen{SessionKey: "k", ChatId: chat, WorkingDir: absRef(filepath.Join(ws, "escape"))}, codes.PermissionDenied},
		{"dotdot", &runnerpb.TtydOpen{SessionKey: "k", ChatId: chat, WorkingDir: absRef(ws + "/../x")}, codes.PermissionDenied},
		{"tmp escape", &runnerpb.TtydOpen{SessionKey: "k", ChatId: chat, WorkingDir: &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP, RelOrPath: "../other"}}, codes.PermissionDenied},
		{"bad chat for tmp", &runnerpb.TtydOpen{SessionKey: "k", ChatId: "../x", WorkingDir: &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP}}, codes.InvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := cl.Ttyd(t.Context())
			require.NoError(t, err)
			require.NoError(t, st.Send(&runnerpb.TtydFrame{Payload: &runnerpb.TtydFrame_Open{Open: tt.open}}))
			_, err = st.Recv()
			assert.Equal(t, tt.code, status.Code(err), "%v", err)
		})
	}

	// The first frame must be open.
	st, err := cl.Ttyd(t.Context())
	require.NoError(t, err)
	require.NoError(t, st.Send(&runnerpb.TtydFrame{Payload: &runnerpb.TtydFrame_Input{Input: []byte("x")}}))
	_, err = st.Recv()
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestTtydSameKeyReplacesAndClientCloseKillsShell(t *testing.T) {
	// A shell that never exits on its own.
	p := filepath.Join(t.TempDir(), "sleepshell")
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\necho up\nexec sleep 600\n"), 0o755))
	cl, ws := startService(t, Options{Shell: p})

	first := openTtyd(t, cl, &runnerpb.TtydOpen{SessionKey: "dup", ChatId: chat, WorkingDir: absRef(ws)})
	first.waitFor("up")
	second := openTtyd(t, cl, &runnerpb.TtydOpen{SessionKey: "dup", ChatId: chat, WorkingDir: absRef(ws)})
	second.waitFor("up")
	select {
	case <-first.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced session was not closed")
	}

	require.NoError(t, second.st.CloseSend())
	select {
	case <-second.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the client did not end the session")
	}
}

func pidGone(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(string(b), ')')
	return i >= 0 && strings.HasPrefix(strings.TrimSpace(string(b)[i+1:]), "Z")
}

func TestTtydKillsBackgroundJobsInOtherGroupsAndSessions(t *testing.T) {
	dir := t.TempDir()
	// plain: own process group (job control style); setsid: own session;
	// double: double-forked daemon reparented to init.
	script := "#!/bin/sh\n" +
		"setsid sh -c 'echo $$ > " + dir + "/setsid; exec sleep 600' &\n" +
		"sh -c 'set -m; sh -c \"echo \\$\\$ > " + dir + "/pgrp; exec sleep 600\" &' &\n" +
		"setsid sh -c '(setsid sh -c \"echo \\$\\$ > " + dir + "/double; exec sleep 600\" &) &' &\n" +
		"echo up\nexec sleep 600\n"
	p := filepath.Join(dir, "shell")
	require.NoError(t, os.WriteFile(p, []byte(script), 0o755))
	cl, ws := startService(t, Options{Shell: p})
	c := openTtyd(t, cl, &runnerpb.TtydOpen{SessionKey: "bg", ChatId: chat, WorkingDir: absRef(ws)})
	c.waitFor("up")

	var pids []int
	for _, n := range []string{"setsid", "pgrp", "double"} {
		require.Eventually(t, func() bool {
			b, err := os.ReadFile(filepath.Join(dir, n))
			return err == nil && strings.TrimSpace(string(b)) != ""
		}, 5*time.Second, 20*time.Millisecond, n)
		b, _ := os.ReadFile(filepath.Join(dir, n))
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		require.NoError(t, err)
		require.False(t, pidGone(pid), n)
		pids = append(pids, pid)
	}

	require.NoError(t, c.st.CloseSend())
	<-c.closed
	for _, pid := range pids {
		require.Eventually(t, func() bool { return pidGone(pid) }, 8*time.Second, 50*time.Millisecond, "pid %d survived", pid)
	}
}

// A recycled shell pid must not make the sweep hit an unrelated session: the
// session-id clause only counts while the shell is still alive.
func TestSignalSessionIgnoresRecycledShellPid(t *testing.T) {
	victim := exec.Command("sleep", "600")
	victim.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // sid == pid, like a shell
	require.NoError(t, victim.Start())
	t.Cleanup(func() { _ = victim.Process.Kill(); _ = victim.Wait() })
	pid := victim.Process.Pid

	// The recorded shell has exited (alive=false) and its pid now belongs to
	// the victim: the marker does not match, so nothing may be signalled.
	signalSession(pid, false, "ASGARD_TTYD=not-the-victims", syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond)
	require.False(t, pidGone(pid), "an unrelated session was killed")

	// While the shell is alive its session is swept.
	signalSession(pid, true, "ASGARD_TTYD=whatever", syscall.SIGKILL)
	_ = victim.Wait()
	assert.True(t, pidGone(pid))
}
