package runnerfs

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// scriptedStream replays a list of frames, then ends with err.
type scriptedStream struct {
	grpc.ClientStream
	frames []*runnerpb.RunCommandFrame
	err    error
	block  context.Context // when set, Recv blocks until it is done
}

func (s *scriptedStream) Recv() (*runnerpb.RunCommandFrame, error) {
	if len(s.frames) > 0 {
		f := s.frames[0]
		s.frames = s.frames[1:]
		return f, nil
	}
	if s.block != nil {
		<-s.block.Done()
		return nil, status.FromContextError(s.block.Err()).Err()
	}
	return nil, s.err
}

func out(off uint64, d string) *runnerpb.RunCommandFrame {
	return &runnerpb.RunCommandFrame{Payload: &runnerpb.RunCommandFrame_Stdout{Stdout: &runnerpb.StreamData{Offset: off, Data: []byte(d)}}}
}

func errf(off uint64, d string) *runnerpb.RunCommandFrame {
	return &runnerpb.RunCommandFrame{Payload: &runnerpb.RunCommandFrame_Stderr{Stderr: &runnerpb.StreamData{Offset: off, Data: []byte(d)}}}
}

func exit(code int32, arts ...*runnerpb.Artifact) *runnerpb.RunCommandFrame {
	return &runnerpb.RunCommandFrame{Payload: &runnerpb.RunCommandFrame_Exit{Exit: &runnerpb.RunCommandExit{Code: code, Artifacts: arts}}}
}

// cmdClient is a RunnerServiceClient whose RunCommand is scripted per call.
type cmdClient struct {
	runnerpb.RunnerServiceClient
	mu    sync.Mutex
	calls []*runnerpb.RunCommandRequest
	next  func(n int, req *runnerpb.RunCommandRequest) (*scriptedStream, error)
}

func (c *cmdClient) RunCommand(_ context.Context, req *runnerpb.RunCommandRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[runnerpb.RunCommandFrame], error) {
	c.mu.Lock()
	c.calls = append(c.calls, req)
	n := len(c.calls)
	c.mu.Unlock()
	st, err := c.next(n, req)
	if err != nil {
		return nil, err
	}
	return st, nil
}

func fastRetries(t *testing.T) {
	t.Helper()
	b, m := commandRetryBackoff, commandMaxBackoff
	commandRetryBackoff, commandMaxBackoff = time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() { commandRetryBackoff, commandMaxBackoff = b, m })
}

func TestRemoteRunCommandRequestAndResult(t *testing.T) {
	cl := &cmdClient{next: func(int, *runnerpb.RunCommandRequest) (*scriptedStream, error) {
		return &scriptedStream{frames: []*runnerpb.RunCommandFrame{
			out(0, "hel"), errf(0, "e1"), out(3, "lo"), errf(2, "e2"),
			exit(2, &runnerpb.Artifact{Name: "o.txt", ViewerPath: "/tmp/o.txt", AbsPath: "/r/tmp/o.txt"}),
		}}, nil
	}}
	res, err := NewRemote(cl, "chat1").RunCommand(t.Context(), CommandSpec{
		Command: "echo", WorkingDir: "sub", RunDir: "/ws", TmpDir: "/ws/t", OutputFile: "o.txt",
		Sandbox: true, AllowCrossSession: true, Timeout: 1500 * time.Millisecond,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, res.ExitCode)
	assert.Equal(t, "hello", string(res.Stdout))
	assert.Equal(t, "e1e2", string(res.Stderr))
	assert.Equal(t, []CommandArtifact{{Name: "o.txt", ViewerPath: "/tmp/o.txt", AbsPath: "/r/tmp/o.txt"}}, res.Artifacts)

	require.Len(t, cl.calls, 1)
	req := cl.calls[0]
	assert.Equal(t, "chat1", req.GetChatId())
	assert.Equal(t, "echo", req.GetCommand())
	assert.Equal(t, "sub", req.GetWorkingDir())
	assert.Equal(t, "/ws", req.GetRunDir().GetRelOrPath())
	assert.True(t, req.Sandbox != nil && *req.Sandbox, "the effective sandbox setting is explicit")
	assert.True(t, req.GetAllowCrossSession())
	assert.EqualValues(t, 2, req.GetTimeoutSeconds(), "rounded up to whole seconds")
	assert.NotEmpty(t, req.GetRequestId())
}

func TestRemoteRunCommandResumesAfterStreamReset(t *testing.T) {
	fastRetries(t)
	cl := &cmdClient{next: func(n int, req *runnerpb.RunCommandRequest) (*scriptedStream, error) {
		switch n {
		case 1:
			return &scriptedStream{frames: []*runnerpb.RunCommandFrame{out(0, "ab"), errf(0, "x")}, err: status.Error(codes.Unavailable, "reset")}, nil
		case 2:
			// Reset again before anything new (and a failed open).
			return nil, status.Error(codes.Unavailable, "still down")
		default:
			// The runner replays from the offsets; it may also resend an
			// overlapping slice, which is dropped.
			return &scriptedStream{frames: []*runnerpb.RunCommandFrame{out(1, "bcd"), errf(1, "y"), exit(0)}}, nil
		}
	}}
	res, err := NewRemote(cl, "chat1").RunCommand(t.Context(), CommandSpec{Command: "c", RunDir: "/ws"})
	require.NoError(t, err)
	assert.Equal(t, "abcd", string(res.Stdout))
	assert.Equal(t, "xy", string(res.Stderr))

	require.Len(t, cl.calls, 3)
	id := cl.calls[0].GetRequestId()
	for _, c := range cl.calls {
		assert.Equal(t, id, c.GetRequestId(), "retries share the request_id")
	}
	assert.EqualValues(t, 0, cl.calls[0].GetResumeStdoutOffset())
	assert.False(t, cl.calls[0].GetResumed(), "the first attempt is not a resume")
	assert.True(t, cl.calls[1].GetResumed() && cl.calls[2].GetResumed())
	assert.EqualValues(t, 2, cl.calls[1].GetResumeStdoutOffset())
	assert.EqualValues(t, 1, cl.calls[1].GetResumeStderrOffset())
	assert.EqualValues(t, 2, cl.calls[2].GetResumeStdoutOffset())
}

func TestRemoteRunCommandEndedWithoutExitIsResumed(t *testing.T) {
	fastRetries(t)
	cl := &cmdClient{next: func(n int, _ *runnerpb.RunCommandRequest) (*scriptedStream, error) {
		if n == 1 {
			return &scriptedStream{frames: []*runnerpb.RunCommandFrame{out(0, "a")}, err: io.EOF}, nil
		}
		return &scriptedStream{frames: []*runnerpb.RunCommandFrame{exit(5)}}, nil
	}}
	res, err := NewRemote(cl, "chat1").RunCommand(t.Context(), CommandSpec{Command: "c"})
	require.NoError(t, err)
	assert.Equal(t, 5, res.ExitCode)
	assert.Equal(t, "a", string(res.Stdout))
}

func TestRemoteRunCommandRunnerErrorsAreNotRetried(t *testing.T) {
	fastRetries(t)
	for code, want := range map[codes.Code]Code{
		codes.PermissionDenied:   CodeDenied,
		codes.FailedPrecondition: CodeIsDir, // ToError's mapping
		codes.NotFound:           CodeNotFound,
		codes.InvalidArgument:    CodeBadRequest,
		codes.ResourceExhausted:  CodeTooLarge,
		codes.Internal:           CodeInternal,
		codes.OutOfRange:         CodeInternal,
		codes.Unimplemented:      CodeInternal,
		codes.Unauthenticated:    CodeInternal,
	} {
		cl := &cmdClient{next: func(int, *runnerpb.RunCommandRequest) (*scriptedStream, error) {
			return &scriptedStream{frames: []*runnerpb.RunCommandFrame{out(0, "partial")}, err: status.Error(code, "refused")}, nil
		}}
		res, err := NewRemote(cl, "chat1").RunCommand(t.Context(), CommandSpec{Command: "c"})
		require.Error(t, err)
		assert.Equal(t, want, CodeOf(err), code.String())
		assert.Len(t, cl.calls, 1, "%s must not be retried", code)
		assert.Equal(t, "partial", string(res.Stdout), "output received so far is kept")
	}
}

func TestRemoteRunCommandGivesUpAfterRepeatedResets(t *testing.T) {
	fastRetries(t)
	cl := &cmdClient{next: func(int, *runnerpb.RunCommandRequest) (*scriptedStream, error) {
		return nil, status.Error(codes.Unavailable, "down")
	}}
	_, err := NewRemote(cl, "chat1").RunCommand(t.Context(), CommandSpec{Command: "c"})
	require.Error(t, err)
	assert.Equal(t, CodeUnavailable, CodeOf(err))
	assert.Len(t, cl.calls, commandMaxAttempts)
}

func TestRemoteRunCommandOutputGapFails(t *testing.T) {
	fastRetries(t)
	cl := &cmdClient{next: func(int, *runnerpb.RunCommandRequest) (*scriptedStream, error) {
		return &scriptedStream{frames: []*runnerpb.RunCommandFrame{out(0, "a"), out(5, "b"), exit(0)}}, nil
	}}
	_, err := NewRemote(cl, "chat1").RunCommand(t.Context(), CommandSpec{Command: "c"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gap")
	assert.Len(t, cl.calls, 1)
}

func TestRemoteRunCommandCancelKillsOnTheRunner(t *testing.T) {
	var idMu sync.Mutex
	cl := &cmdClient{}
	ctx, cancel := context.WithCancel(t.Context())
	cl.next = func(n int, req *runnerpb.RunCommandRequest) (*scriptedStream, error) {
		idMu.Lock()
		defer idMu.Unlock()
		if n == 1 {
			return &scriptedStream{frames: []*runnerpb.RunCommandFrame{out(0, "x")}, block: ctx}, nil
		}
		return &scriptedStream{err: io.EOF}, nil
	}
	done := make(chan error, 1)
	var res CommandResult
	go func() {
		var err error
		res, err = NewRemote(cl, "chat1").RunCommand(ctx, CommandSpec{Command: "sleep"})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	err := <-done
	assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
	assert.Equal(t, "x", string(res.Stdout))

	cl.mu.Lock()
	defer cl.mu.Unlock()
	require.Len(t, cl.calls, 2, "the stream plus one explicit cancel call")
	assert.Equal(t, cl.calls[0].GetRequestId(), cl.calls[1].GetRequestId())
	assert.True(t, cl.calls[1].GetCancel())
	assert.Equal(t, "chat1", cl.calls[1].GetChatId())
}

func TestRemoteRunCommandCapsCapturedOutput(t *testing.T) {
	var cs commandStream
	cs.addStdout(make([]byte, MaxCommandStdout-1))
	cs.addStdout([]byte("abcd"))
	assert.Len(t, cs.res.Stdout, MaxCommandStdout)
	assert.True(t, cs.res.StdoutTruncated)
	cs.addStderr(make([]byte, MaxCommandStderr))
	cs.addStderr([]byte("tail"))
	assert.Len(t, cs.stderrTail, MaxCommandStderr)
	assert.Equal(t, "tail", string(cs.stderrTail[len(cs.stderrTail)-4:]))
}
