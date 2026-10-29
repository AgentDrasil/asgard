package service

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/AgentDrasil/asgard/backend/lib/cmdexec"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// startCmdService is startService with access to the Service (to stub the
// sandbox runner).
func startCmdService(t *testing.T, opts Options, tweak func(*Service)) (runnerpb.RunnerServiceClient, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ws := filepath.Join(t.TempDir(), "ws")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	if opts.Agents == nil {
		opts.Agents = func() ([]*agentspec.Agent, error) {
			return []*agentspec.Agent{{Config: agentspec.AgentConfig{RunDirs: []string{ws}}}}, nil
		}
	}
	svc := New(opts)
	t.Cleanup(svc.Close)
	if tweak != nil {
		tweak(svc)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	runnerpb.RegisterRunnerServiceServer(srv, svc)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })
	return runnerpb.NewRunnerServiceClient(cc), ws
}

func off(sandbox bool) *bool { return &sandbox }

// cmdOut is what one RunCommand stream delivered.
type cmdOut struct {
	stdout, stderr string
	stdoutOff      uint64 // offset of the first stdout frame
	exit           *runnerpb.RunCommandExit
	maxFrame       int
	err            error
}

func collect(st grpc.ServerStreamingClient[runnerpb.RunCommandFrame]) cmdOut {
	var o cmdOut
	var so, se uint64
	first := true
	for {
		f, err := st.Recv()
		if errors.Is(err, io.EOF) {
			return o
		}
		if err != nil {
			o.err = err
			return o
		}
		switch p := f.GetPayload().(type) {
		case *runnerpb.RunCommandFrame_Stdout:
			if first {
				o.stdoutOff, so, first = p.Stdout.GetOffset(), p.Stdout.GetOffset(), false
			}
			if p.Stdout.GetOffset() != so {
				o.err = errors.New("non-contiguous stdout offset")
				return o
			}
			so += uint64(len(p.Stdout.GetData()))
			o.maxFrame = max(o.maxFrame, len(p.Stdout.GetData()))
			o.stdout += string(p.Stdout.GetData())
		case *runnerpb.RunCommandFrame_Stderr:
			if p.Stderr.GetOffset() != se {
				o.err = errors.New("non-contiguous stderr offset")
				return o
			}
			se += uint64(len(p.Stderr.GetData()))
			o.stderr += string(p.Stderr.GetData())
		case *runnerpb.RunCommandFrame_Exit:
			o.exit = p.Exit
		}
	}
}

func runCmd(t *testing.T, cl runnerpb.RunnerServiceClient, req *runnerpb.RunCommandRequest) cmdOut {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	st, err := cl.RunCommand(ctx, req)
	require.NoError(t, err)
	return collect(st)
}

func unsandboxed(ws, cmd string) *runnerpb.RunCommandRequest {
	return &runnerpb.RunCommandRequest{ChatId: chat, Command: cmd, RunDir: absRef(ws), Sandbox: off(false)}
}

func TestRunCommandStreamsOutputAndExitCode(t *testing.T) {
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	req := unsandboxed(ws, "echo out; echo err >&2; pwd; exit 3")
	req.WorkingDir = "a/b" // created on demand
	o := runCmd(t, cl, req)
	require.NoError(t, o.err)
	require.NotNil(t, o.exit)
	assert.EqualValues(t, 3, o.exit.GetCode(), "a non-zero exit is a result, not an RPC error")
	real, err := filepath.EvalSymlinks(filepath.Join(ws, "a", "b"))
	require.NoError(t, err)
	assert.Equal(t, "out\n"+real+"\n", o.stdout)
	assert.Equal(t, "err\n", o.stderr)
	assert.DirExists(t, filepath.Join(ws, "a", "b"))
}

func TestRunCommandFrameSizeAndLargeOutput(t *testing.T) {
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	o := runCmd(t, cl, unsandboxed(ws, "head -c 3000000 /dev/zero | tr '\\0' x"))
	require.NoError(t, o.err)
	assert.Len(t, o.stdout, 3000000)
	assert.LessOrEqual(t, o.maxFrame, cmdFrameSize)
	assert.EqualValues(t, 0, o.exit.GetCode())
}

func TestRunCommandSandboxDefaultFailsClosed(t *testing.T) {
	// No sandbox setup at all: the default (sandbox unset) is an error even
	// with the operator opt-in for unsandboxed commands.
	for _, opts := range []Options{{}, {AllowUnsandboxed: true}} {
		cl, ws := startCmdService(t, opts, nil)
		req := unsandboxed(ws, "echo hi")
		req.Sandbox = nil
		o := runCmd(t, cl, req)
		require.Error(t, o.err)
		assert.Equal(t, codes.FailedPrecondition, status.Code(o.err))
		assert.Contains(t, o.err.Error(), "sandbox not ready")
		_, statErr := os.Stat(filepath.Join(ws, "ran"))
		assert.True(t, os.IsNotExist(statErr))
	}

	// A sandbox that reports itself unhealthy refuses, too.
	called := false
	cl, ws := startCmdService(t, Options{
		Ready:       func() error { return errors.New("proxy is not listening") },
		SandboxConf: func() *config.Config { return &config.Config{} },
	}, func(s *Service) {
		s.runSandboxed = func(context.Context, cmdexec.SandboxSpec, io.Writer, io.Writer) (int, error) {
			called = true
			return 0, nil
		}
	})
	req := unsandboxed(ws, "echo hi")
	req.Sandbox = off(true)
	o := runCmd(t, cl, req)
	require.Error(t, o.err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(o.err))
	assert.Contains(t, o.err.Error(), "proxy is not listening")
	assert.False(t, called)
}

func TestRunCommandUnsandboxedNeedsOperatorOptIn(t *testing.T) {
	cl, ws := startCmdService(t, Options{}, nil)
	o := runCmd(t, cl, unsandboxed(ws, "touch "+filepath.Join(ws, "ran")))
	require.Error(t, o.err)
	assert.Equal(t, codes.PermissionDenied, status.Code(o.err))
	assert.NoFileExists(t, filepath.Join(ws, "ran"))

	cl, ws = startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	o = runCmd(t, cl, unsandboxed(ws, "touch "+filepath.Join(ws, "ran")))
	require.NoError(t, o.err)
	assert.FileExists(t, filepath.Join(ws, "ran"))
}

func TestRunCommandSandboxedUsesSandboxSpec(t *testing.T) {
	t.Setenv("ASGARD_RUNNER_TLS_KEY", "/secret/key.pem")
	var got cmdexec.SandboxSpec
	cl, ws := startCmdService(t, Options{
		Ready:       func() error { return nil },
		SandboxConf: func() *config.Config { return &config.Config{} },
	}, func(s *Service) {
		s.runSandboxed = func(_ context.Context, spec cmdexec.SandboxSpec, out, errw io.Writer) (int, error) {
			got = spec
			_, _ = out.Write([]byte("sandboxed out"))
			_, _ = errw.Write([]byte("sandboxed err"))
			return 7, nil
		}
	})
	req := unsandboxed(ws, "echo hi")
	req.Sandbox = nil // the default
	req.WorkingDir = "sub"
	req.AllowCrossSession = true
	o := runCmd(t, cl, req)
	require.NoError(t, o.err)
	assert.Equal(t, "sandboxed out", o.stdout)
	assert.Equal(t, "sandboxed err", o.stderr)
	assert.EqualValues(t, 7, o.exit.GetCode())

	real, err := filepath.EvalSymlinks(filepath.Join(ws, "sub"))
	require.NoError(t, err)
	assert.Equal(t, real, got.Dir, "the sandbox gets the resolved, symlink-free working dir")
	assert.Equal(t, "echo hi", got.Command)
	assert.Equal(t, chat, got.ChatID)
	assert.True(t, got.AllowCrossSession)
	assert.Equal(t, []string(nil), got.ReadonlyDirs, "no readonly mounts configured")
	assert.Contains(t, got.Env, "ASGARD_TMP_DIR=/tmp")
	assert.Contains(t, got.Env, "ASGARD_SESSION_DIR=/session")
	for _, e := range got.Env {
		assert.False(t, strings.HasPrefix(e, "ASGARD_RUNNER_"), "runner settings must not reach commands: %s", e)
	}
}

func TestRunCommandWorkingDirIsConfined(t *testing.T) {
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "link")))
	require.NoError(t, os.Symlink("../..", filepath.Join(ws, "up")))

	for name, wd := range map[string]string{
		"dotdot":           "../escaped",
		"dotdot nested":    "a/../../escaped",
		"symlink":          "link/sub",
		"symlink itself":   "link",
		"symlink up":       "up/escaped",
		"absolute outside": filepath.Join(outside, "abs"),
	} {
		req := unsandboxed(ws, "touch marker")
		req.WorkingDir = wd
		o := runCmd(t, cl, req)
		if name == "absolute outside" {
			// Under /tmp the path reads as the chat's namespaced /tmp form and
			// lands in the chat's own tmp dir (never at the host path); the
			// check below proves nothing was created outside.
			continue
		}
		require.Error(t, o.err, name)
		assert.Contains(t, []codes.Code{codes.PermissionDenied, codes.InvalidArgument, codes.NotFound}, status.Code(o.err), name)
	}
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing may be created outside the allowed roots")
	assert.NoDirExists(t, filepath.Join(filepath.Dir(ws), "escaped"))

	// A run dir outside the allowed roots is refused, too.
	req := unsandboxed(outside, "touch marker")
	o := runCmd(t, cl, req)
	require.Error(t, o.err)
	assert.Equal(t, codes.PermissionDenied, status.Code(o.err))
	assert.NoFileExists(t, filepath.Join(outside, "marker"))

	// No run dir and no working dir: nothing to run in.
	o = runCmd(t, cl, &runnerpb.RunCommandRequest{ChatId: chat, Command: "true", Sandbox: off(false)})
	require.Error(t, o.err)
	assert.Equal(t, codes.InvalidArgument, status.Code(o.err))
}

func TestRunCommandOutputFileArtifact(t *testing.T) {
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	req := unsandboxed(ws, `mkdir -p "$ASGARD_TMP_DIR/sub" && echo data > "$ASGARD_TMP_DIR/sub/out.txt"`)
	req.OutputFile = "sub/out.txt"
	req.Tmp = &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP}
	o := runCmd(t, cl, req)
	require.NoError(t, o.err)
	require.Len(t, o.exit.GetArtifacts(), 1)
	a := o.exit.GetArtifacts()[0]
	assert.Equal(t, "sub/out.txt", a.GetName())
	assert.Equal(t, "/tmp/sub/out.txt", a.GetViewerPath())
	assert.Equal(t, filepath.Join(paths.SessionTmpDir(chat), "sub", "out.txt"), a.GetAbsPath())

	// Not produced: no artifact.
	req = unsandboxed(ws, "true")
	req.OutputFile = "missing.txt"
	req.Tmp = &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP}
	o = runCmd(t, cl, req)
	require.NoError(t, o.err)
	assert.Empty(t, o.exit.GetArtifacts())

	// A custom tmp dir given as an absolute path.
	custom := filepath.Join(ws, "mytmp")
	require.NoError(t, os.MkdirAll(custom, 0o755))
	req = unsandboxed(ws, "echo x > mytmp/o.txt")
	req.OutputFile = "o.txt"
	req.Tmp = absRef(custom)
	o = runCmd(t, cl, req)
	require.NoError(t, o.err)
	require.Len(t, o.exit.GetArtifacts(), 1)
	assert.Equal(t, filepath.Join(custom, "o.txt"), o.exit.GetArtifacts()[0].GetAbsPath())

	// An output file escaping the tmp dir is refused.
	req = unsandboxed(ws, "true")
	req.OutputFile = "../x"
	req.Tmp = &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP}
	o = runCmd(t, cl, req)
	require.Error(t, o.err)
	assert.Equal(t, codes.InvalidArgument, status.Code(o.err))
}

func TestRunCommandEnvironment(t *testing.T) {
	t.Setenv("ASGARD_RUNNER_TLS_KEY", "/secret/key.pem")
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	o := runCmd(t, cl, unsandboxed(ws, `echo "[$ASGARD_RUNNER_TLS_KEY] $ASGARD_CHAT_ID $ASGARD_TMP_DIR"`))
	require.NoError(t, o.err)
	assert.Equal(t, "[] "+chat+" "+paths.SessionTmpDir(chat)+"\n", o.stdout)
}

func TestRunCommandResumeAndIdempotentAttach(t *testing.T) {
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	counter := filepath.Join(ws, "count")
	gate := filepath.Join(ws, "gate")
	req := unsandboxed(ws, "echo run >> "+counter+"; printf one; while [ ! -e "+gate+" ]; do sleep 0.05; done; printf two; printf E >&2")
	req.RequestId = "req-1"

	// First stream: read "one" then drop the stream.
	ctx1, cancel1 := context.WithCancel(t.Context())
	st, err := cl.RunCommand(ctx1, req)
	require.NoError(t, err)
	f, err := st.Recv()
	require.NoError(t, err)
	assert.Equal(t, "one", string(f.GetStdout().GetData()))
	assert.EqualValues(t, 0, f.GetStdout().GetOffset())
	cancel1()

	// Retry with the same request_id attaches to the running command (no
	// second execution) and resumes after the received bytes.
	retry := proto.Clone(req).(*runnerpb.RunCommandRequest)
	retry.ResumeStdoutOffset = 3
	ctx2, cancel2 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel2()
	st2, err := cl.RunCommand(ctx2, retry)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(gate, nil, 0o644))
	o := collect(st2)
	require.NoError(t, o.err)
	assert.Equal(t, "two", o.stdout)
	assert.EqualValues(t, 3, o.stdoutOff)
	assert.Equal(t, "E", o.stderr)
	assert.EqualValues(t, 0, o.exit.GetCode())

	// After completion the cached result is replayed from any offset.
	again := collect(mustStream(t, cl, req))
	require.NoError(t, again.err)
	assert.Equal(t, "onetwo", again.stdout)
	assert.EqualValues(t, 0, again.exit.GetCode())
	mid := proto.Clone(req).(*runnerpb.RunCommandRequest)
	mid.ResumeStdoutOffset = 4
	tail := collect(mustStream(t, cl, mid))
	require.NoError(t, tail.err)
	assert.Equal(t, "wo", tail.stdout)

	data, err := os.ReadFile(counter)
	require.NoError(t, err)
	assert.Equal(t, "run\n", string(data), "the command ran exactly once")

	// Resuming a command the runner knows nothing about must not run it.
	gone := proto.Clone(req).(*runnerpb.RunCommandRequest)
	gone.RequestId = "unknown"
	gone.ResumeStdoutOffset = 5
	gone.Resumed = true
	o = collect(mustStream(t, cl, gone))
	require.Error(t, o.err)
	assert.Equal(t, codes.NotFound, status.Code(o.err))
	data, _ = os.ReadFile(counter)
	assert.Equal(t, "run\n", string(data))

	// An offset beyond the produced output is refused.
	beyond := proto.Clone(req).(*runnerpb.RunCommandRequest)
	beyond.ResumeStdoutOffset = 99
	o = collect(mustStream(t, cl, beyond))
	assert.Equal(t, codes.OutOfRange, status.Code(o.err))
}

func mustStream(t *testing.T, cl runnerpb.RunnerServiceClient, req *runnerpb.RunCommandRequest) grpc.ServerStreamingClient[runnerpb.RunCommandFrame] {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := cl.RunCommand(ctx, req)
	require.NoError(t, err)
	return st
}

func TestRunCommandRequestIDsAreScopedToTheChat(t *testing.T) {
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	a := unsandboxed(ws, "echo a")
	a.RequestId = "same"
	b := unsandboxed(ws, "echo b")
	b.RequestId = "same"
	b.ChatId = "chat2"
	assert.Equal(t, "a\n", runCmd(t, cl, a).stdout)
	assert.Equal(t, "b\n", runCmd(t, cl, b).stdout)
}

// pidAlive reports whether the process exists (and is not a zombie we could
// not reap).
func pidAlive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// state is the field after the ")" of the comm.
	i := strings.LastIndex(string(b), ")")
	return i >= 0 && len(b) > i+2 && b[i+2] != 'Z'
}

func waitForPid(t *testing.T, file string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(file)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	}, 10*time.Second, 20*time.Millisecond)
	return pid
}

func requireDies(t *testing.T, pid int) {
	t.Helper()
	require.Eventually(t, func() bool { return !pidAlive(pid) }, 10*time.Second, 20*time.Millisecond, "process %d must be killed", pid)
}

// spawn starts a background child (same process group as the shell) and
// records its pid.
func spawn(file string) string {
	return "sleep 300 & echo $! > " + file + "; wait"
}

func TestRunCommandCancelKillsTheProcessGroup(t *testing.T) {
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	pidFile := filepath.Join(ws, "pid")
	ctx, cancel := context.WithCancel(t.Context())
	st, err := cl.RunCommand(ctx, unsandboxed(ws, spawn(pidFile))) // no request_id: not resumable
	require.NoError(t, err)
	_ = st
	pid := waitForPid(t, pidFile)
	require.True(t, pidAlive(pid))
	cancel()
	requireDies(t, pid)
}

func TestRunCommandExplicitCancelAndDetachGrace(t *testing.T) {
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true, DetachGrace: 300 * time.Millisecond}, nil)

	// A dropped stream with a request_id leaves the command running for the
	// grace period, then kills it.
	pidFile := filepath.Join(ws, "pid1")
	req := unsandboxed(ws, spawn(pidFile))
	req.RequestId = "grace"
	ctx, cancel := context.WithCancel(t.Context())
	_, err := cl.RunCommand(ctx, req)
	require.NoError(t, err)
	pid := waitForPid(t, pidFile)
	cancel()
	requireDies(t, pid)

	// A reattach within the grace period keeps it alive; an explicit cancel
	// (negative timeout) kills it at once.
	cl2, ws2 := startCmdService(t, Options{AllowUnsandboxed: true, DetachGrace: time.Hour}, nil)
	pidFile = filepath.Join(ws2, "pid2")
	req = unsandboxed(ws2, spawn(pidFile))
	req.RequestId = "explicit"
	ctx, cancel = context.WithCancel(t.Context())
	_, err = cl2.RunCommand(ctx, req)
	require.NoError(t, err)
	pid = waitForPid(t, pidFile)
	cancel()
	time.Sleep(300 * time.Millisecond)
	require.True(t, pidAlive(pid), "the command waits for a reattach")
	st, err := cl2.RunCommand(t.Context(), &runnerpb.RunCommandRequest{ChatId: chat, RequestId: "explicit", Cancel: true})
	require.NoError(t, err)
	_ = collect(st)
	requireDies(t, pid)
	// The killed command answers a later attach with a cancelled status.
	o := collect(mustStream(t, cl2, req))
	assert.Equal(t, codes.Canceled, status.Code(o.err))
}

func TestRunCommandTimeoutKillsAndReportsExitMinusOne(t *testing.T) {
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	pidFile := filepath.Join(ws, "pid")
	req := unsandboxed(ws, spawn(pidFile))
	req.TimeoutSeconds = 1
	start := time.Now()
	o := runCmd(t, cl, req)
	require.NoError(t, o.err)
	assert.EqualValues(t, -1, o.exit.GetCode())
	assert.Less(t, time.Since(start), 15*time.Second)
	requireDies(t, waitForPid(t, pidFile))
}

func TestRunCommandTooManyCommands(t *testing.T) {
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, func(s *Service) {
		s.cmds = newCmdRegistry(0, 1)
	})
	req := unsandboxed(ws, "sleep 300")
	req.RequestId = "r1"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	st, err := cl.RunCommand(ctx, req)
	require.NoError(t, err)
	_ = st
	time.Sleep(200 * time.Millisecond)
	second := unsandboxed(ws, "true")
	second.RequestId = "r2"
	o := runCmd(t, cl, second)
	assert.Equal(t, codes.ResourceExhausted, status.Code(o.err))
	// Dropping the stream (no reattach) lets the grace period kill the sleeper.
	cancel()
}

func TestRunCommandValidatesRequest(t *testing.T) {
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	for name, mod := range map[string]func(*runnerpb.RunCommandRequest){
		"bad chat":      func(r *runnerpb.RunCommandRequest) { r.ChatId = "../x" },
		"empty command": func(r *runnerpb.RunCommandRequest) { r.Command = "  " },
	} {
		req := unsandboxed(ws, "true")
		mod(req)
		o := runCmd(t, cl, req)
		assert.Equal(t, codes.InvalidArgument, status.Code(o.err), name)
	}
}

func TestRunCommandResumedAfterRunnerRestartDoesNotRerun(t *testing.T) {
	// A fresh Service has no record of the request: a resumed retry, even with
	// zero offsets, is NotFound and never runs the command.
	cl, ws := startCmdService(t, Options{AllowUnsandboxed: true}, nil)
	marker := filepath.Join(ws, "ran")
	req := unsandboxed(ws, "echo x >> "+marker)
	req.RequestId = "r"
	req.Resumed = true
	o := runCmd(t, cl, req)
	require.Error(t, o.err)
	assert.Equal(t, codes.NotFound, status.Code(o.err))
	assert.NoFileExists(t, marker)

	// The first attempt still runs; cancel of an unknown id is a no-op.
	req.Resumed = false
	o = runCmd(t, cl, req)
	require.NoError(t, o.err)
	assert.FileExists(t, marker)
	st, err := cl.RunCommand(t.Context(), &runnerpb.RunCommandRequest{ChatId: chat, RequestId: "nope", Cancel: true})
	require.NoError(t, err)
	assert.NoError(t, collect(st).err)
}

func TestRunCommandSandboxCwdIsTheSandboxView(t *testing.T) {
	var got cmdexec.SandboxSpec
	shared, wsDir := "", ""
	cl, ws := startCmdService(t, Options{
		Ready:       func() error { return nil },
		SandboxConf: func() *config.Config { return &config.Config{} },
		Agents: func() ([]*agentspec.Agent, error) {
			return []*agentspec.Agent{{Config: agentspec.AgentConfig{RunDirs: []string{shared, wsDir}}}}, nil
		},
	}, func(s *Service) {
		s.runSandboxed = func(_ context.Context, spec cmdexec.SandboxSpec, _, _ io.Writer) (int, error) {
			got = spec
			return 0, nil
		}
	})
	// The agent lists an allowed root under the runtime data dir, too.
	shared = filepath.Join(paths.DataDir(), "shared")
	wsDir = ws
	run := func(runDir *runnerpb.DirRef, wd string, cross bool) cmdOut {
		got = cmdexec.SandboxSpec{}
		return runCmd(t, cl, &runnerpb.RunCommandRequest{ChatId: chat, Command: "true", RunDir: runDir, WorkingDir: wd, AllowCrossSession: cross})
	}
	tmpRef := &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP}

	// ${tmp_dir}/sub and /session/sub: host path for creation, sandbox path for cwd.
	for wd, want := range map[string]string{"/tmp/sub": "/tmp/sub", "/session/sub": "/session/sub", "/tmp": "/tmp"} {
		o := run(tmpRef, wd, false)
		require.NoError(t, o.err, wd)
		assert.Equal(t, want, got.Cwd, wd)
		assert.True(t, strings.HasPrefix(got.Dir, paths.DataDir()), "Dir stays the host path: %s", got.Dir)
		assert.DirExists(t, got.Dir)
	}
	// A run dir that is the chat's tmp dir with a relative working dir.
	o := run(tmpRef, "rel/dir", false)
	require.NoError(t, o.err)
	assert.Equal(t, "/tmp/rel/dir", got.Cwd)

	// A run dir under an allowed absolute root keeps its path.
	o = run(absRef(ws), "sub", false)
	require.NoError(t, o.err)
	real, err := filepath.EvalSymlinks(filepath.Join(ws, "sub"))
	require.NoError(t, err)
	assert.Equal(t, real, got.Cwd)
	assert.Equal(t, real, got.Dir)

	// Under the runtime data dir (outside the chat's own dirs) it is masked in
	// the sandbox: refused unless cross-session access is allowed.
	require.NoError(t, os.MkdirAll(shared, 0o755))
	o = run(absRef(shared), "", false)
	require.Error(t, o.err)
	assert.Equal(t, codes.PermissionDenied, status.Code(o.err))
	assert.Empty(t, got.Command, "the sandbox must not start")
	o = run(absRef(shared), "", true)
	require.NoError(t, o.err)
	assert.Equal(t, got.Dir, got.Cwd)
}

func TestRunCommandSandboxGetsReadonlyMounts(t *testing.T) {
	ro := t.TempDir()
	var got cmdexec.SandboxSpec
	cl, ws := startCmdService(t, Options{
		Ready:       func() error { return nil },
		SandboxConf: func() *config.Config { return &config.Config{} },
		Agents: func() ([]*agentspec.Agent, error) {
			return []*agentspec.Agent{{Config: agentspec.AgentConfig{RunDirs: []string{ro}, MountDirs: agentspec.MountConfig{ReadOnly: []string{ro}}}}}, nil
		},
	}, func(s *Service) {
		s.runSandboxed = func(_ context.Context, spec cmdexec.SandboxSpec, _, _ io.Writer) (int, error) {
			got = spec
			return 0, nil
		}
	})
	_ = ws
	o := runCmd(t, cl, &runnerpb.RunCommandRequest{ChatId: chat, Command: "true", RunDir: absRef(ro)})
	require.NoError(t, o.err)
	assert.Equal(t, []string{ro}, got.ReadonlyDirs)
}

func TestRunCommandRefusesMalformedReadonlyMounts(t *testing.T) {
	ran := false
	cl, ws := startCmdService(t, Options{
		Ready:       func() error { return nil },
		SandboxConf: func() *config.Config { return &config.Config{} },
		Agents: func() ([]*agentspec.Agent, error) {
			return []*agentspec.Agent{{Config: agentspec.AgentConfig{MountDirs: agentspec.MountConfig{ReadOnly: []string{"relative/ro"}}}}}, nil
		},
	}, func(s *Service) {
		s.runSandboxed = func(context.Context, cmdexec.SandboxSpec, io.Writer, io.Writer) (int, error) {
			ran = true
			return 0, nil
		}
	})
	_ = ws
	o := runCmd(t, cl, &runnerpb.RunCommandRequest{ChatId: chat, Command: "true", RunDir: &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP}})
	require.Error(t, o.err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(o.err))
	assert.Contains(t, o.err.Error(), `"relative/ro"`)
	assert.False(t, ran)
}
