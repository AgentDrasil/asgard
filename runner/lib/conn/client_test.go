package conn

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

type treeService struct {
	runnerpb.UnimplementedRunnerServiceServer
}

func (treeService) Tree(_ context.Context, req *runnerpb.TreeRequest) (*runnerpb.TreeResponse, error) {
	return &runnerpb.TreeResponse{Path: "tree:" + req.GetPath()}, nil
}

func waitRun(t *testing.T, r *runnerhub.Run) *runnerhub.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	res, err := r.Wait(ctx)
	require.NoError(t, err)
	return res
}

func TestRegisterHeartbeatAndRunnerService(t *testing.T) {
	t.Parallel()
	env := newHubEnv(t, "r1")
	c := startClient(t, env.pki, env.hub.Addr().String(), "r1", func(o *Options) {
		o.MaxRuns = 3
		o.Service = treeService{}
		o.Quotas = func() []*runnerpb.TargetQuota {
			return []*runnerpb.TargetQuota{{Cli: "opencode", Model: "m1", Remaining: 0.5, Enabled: true}}
		}
	})
	env.waitOnline("r1", true)

	// Heartbeat reaches the hub.
	require.Eventually(t, func() bool {
		n, _ := env.node("r1")
		return n.MaxRuns == 3 && len(n.Quotas) == 1
	}, 5*time.Second, 10*time.Millisecond)
	n, _ := env.node("r1")
	assert.Equal(t, "v-test", n.Version)
	assert.Equal(t, uint32(runnerpb.ProtocolVersion), n.ProtocolVersion)
	assert.Equal(t, c.BootID(), n.RunnerBootID)
	assert.Equal(t, map[string][]string{"opencode": {"m1"}}, n.SupportedCLIs)
	assert.InDelta(t, 0.5, n.Quotas[0].GetRemaining(), 1e-9)

	// Backend -> runner RunnerService call reaches the runner.
	cl, err := env.hub.Client("r1")
	require.NoError(t, err)
	resp, err := cl.Tree(context.Background(), &runnerpb.TreeRequest{Path: "x"})
	require.NoError(t, err)
	assert.Equal(t, "tree:x", resp.GetPath())

	// Heartbeats do not pile up in the unacked queue.
	require.Eventually(t, func() bool { return c.Queued() <= 1 }, 5*time.Second, 10*time.Millisecond)
}

func TestDefaultRunnerServiceUnimplemented(t *testing.T) {
	t.Parallel()
	env := newHubEnv(t, "r1")
	startClient(t, env.pki, env.hub.Addr().String(), "r1", nil)
	env.waitOnline("r1", true)
	cl, err := env.hub.Client("r1")
	require.NoError(t, err)
	_, err = cl.Tree(context.Background(), &runnerpb.TreeRequest{})
	assert.Equal(t, codes.Unimplemented, status.Code(err))
}

func TestDispatchNotImplementedResult(t *testing.T) {
	t.Parallel()
	env := newHubEnv(t, "r1")
	startClient(t, env.pki, env.hub.Addr().String(), "r1", nil)
	env.waitOnline("r1", true)

	run, err := env.hub.Dispatch(context.Background(), "r1", &runnerpb.StartRun{
		RunId: "run-1", ChatId: "chat-1", Attempt: 2,
		StatusScope: &runnerpb.StatusScope{NodeId: "wf", RunToken: "tok"},
	})
	require.NoError(t, err)
	res := waitRun(t, run)
	assert.Equal(t, ErrNotImplemented, res.RunResult.GetError())
	assert.Equal(t, "chat-1", res.RunResult.GetChatId())
	assert.Equal(t, uint32(2), res.RunResult.GetAttempt())
	assert.Equal(t, "wf", res.RunResult.GetWorkflowNodeId())
	assert.Equal(t, "tok", res.RunResult.GetRunToken())
}

func TestCancelRunReachesExecutor(t *testing.T) {
	t.Parallel()
	env := newHubEnv(t, "r1")
	exec := newRecordingExecutor(make(chan struct{}))
	startClient(t, env.pki, env.hub.Addr().String(), "r1", func(o *Options) { o.Executor = exec })
	env.waitOnline("r1", true)

	_, err := env.hub.Dispatch(context.Background(), "r1", &runnerpb.StartRun{RunId: "run-1"})
	require.NoError(t, err)
	<-exec.startedCh
	require.NoError(t, env.hub.Cancel("r1", "run-1"))
	require.Eventually(t, func() bool {
		exec.mu.Lock()
		defer exec.mu.Unlock()
		return len(exec.cancelled) == 1 && exec.cancelled[0] == "run-1"
	}, 5*time.Second, 10*time.Millisecond)
}

func TestReconnectKeepsPendingRunAndBufferedResult(t *testing.T) {
	t.Parallel()
	env := newHubEnv(t, "r1")
	hold := make(chan struct{})
	exec := newRecordingExecutor(hold)
	c := startClient(t, env.pki, env.hub.Addr().String(), "r1", func(o *Options) { o.Executor = exec })
	env.waitOnline("r1", true)

	run, err := env.hub.Dispatch(context.Background(), "r1", &runnerpb.StartRun{RunId: "run-1", ChatId: "c"})
	require.NoError(t, err)
	<-exec.startedCh

	// Drop the connection, finish the run while offline: the result is
	// buffered and delivered after the runner reconnects (same boot id).
	env.setAllowed()
	env.waitOnline("r1", false)
	close(hold)
	require.Eventually(t, func() bool { return c.Queued() >= 1 }, 5*time.Second, 10*time.Millisecond)
	env.setAllowed("r1")
	env.waitOnline("r1", true)

	res := waitRun(t, run)
	assert.Equal(t, "done:run-1", res.RunResult.GetError())
	assert.Equal(t, []string{"run-1"}, exec.startedRuns())
	n, _ := env.node("r1")
	assert.Equal(t, c.BootID(), n.RunnerBootID)
	require.Eventually(t, func() bool { return c.Queued() <= 1 }, 5*time.Second, 10*time.Millisecond)
}

func TestReconnectAfterHubRestart(t *testing.T) {
	t.Parallel()
	env := newHubEnv(t, "r1")
	exec := newRecordingExecutor(nil)
	c := startClient(t, env.pki, env.hub.Addr().String(), "r1", func(o *Options) { o.Executor = exec })
	env.waitOnline("r1", true)

	run1, err := env.hub.Dispatch(context.Background(), "r1", &runnerpb.StartRun{RunId: "run-1"})
	require.NoError(t, err)
	assert.Equal(t, "done:run-1", waitRun(t, run1).RunResult.GetError())

	env.restartHub()
	env.waitOnline("r1", true)
	n, _ := env.node("r1")
	assert.Equal(t, c.BootID(), n.RunnerBootID)

	// The new hub boot counts downstream seq from 1 again; the runner must
	// not drop it as a duplicate.
	run2, err := env.hub.Dispatch(context.Background(), "r1", &runnerpb.StartRun{RunId: "run-2"})
	require.NoError(t, err)
	assert.Equal(t, "done:run-2", waitRun(t, run2).RunResult.GetError())
	assert.Equal(t, []string{"run-1", "run-2"}, exec.startedRuns())
}

func accept(boot string) *runnerpb.BackendMessage {
	return &runnerpb.BackendMessage{Payload: &runnerpb.BackendMessage_RegisterResult{RegisterResult: &runnerpb.RegisterResult{
		Accepted: true, ProtocolVersion: runnerpb.ProtocolVersion, BackendBootId: boot,
	}}}
}

func startMsg(seq uint64, id string) *runnerpb.BackendMessage {
	return &runnerpb.BackendMessage{Seq: seq, Payload: &runnerpb.BackendMessage_StartRun{StartRun: &runnerpb.StartRun{RunId: id}}}
}

func TestRejectedRegistrationRetriesWithBackoff(t *testing.T) {
	t.Parallel()
	fb := newFakeBackend(t, func(_ int, _ *runnerpb.Register, stream grpc.BidiStreamingServer[runnerpb.RunnerMessage, runnerpb.BackendMessage]) error {
		return stream.Send(&runnerpb.BackendMessage{Payload: &runnerpb.BackendMessage_RegisterResult{RegisterResult: &runnerpb.RegisterResult{
			RejectReason: "protocol version mismatch", ProtocolVersion: 99, BackendBootId: "b",
		}}})
	})
	c := startClient(t, fb.pki, fb.ln.Addr().String(), "r1", nil)

	require.Eventually(t, func() bool { return fb.attemptCount() >= 3 }, 10*time.Second, 10*time.Millisecond)
	assert.False(t, c.Connected())
	reg := fb.register(0)
	assert.Equal(t, "r1", reg.GetNodeId())
	assert.Equal(t, uint32(runnerpb.ProtocolVersion), reg.GetProtocolVersion())
	assert.Equal(t, c.BootID(), reg.GetRunnerBootId())
	assert.Equal(t, "v-test", reg.GetVersion())
}

func TestDownstreamDedupAndBackendBootReset(t *testing.T) {
	t.Parallel()
	fb := newFakeBackend(t, nil)
	fb.connect = func(n int, _ *runnerpb.Register, stream grpc.BidiStreamingServer[runnerpb.RunnerMessage, runnerpb.BackendMessage]) error {
		switch n {
		case 1:
			// seq 1 delivered twice (replay), then seq 2.
			for _, m := range []*runnerpb.BackendMessage{accept("b1"), startMsg(1, "r1"), startMsg(1, "r1"), startMsg(2, "r2")} {
				if err := stream.Send(m); err != nil {
					return err
				}
			}
			// Wait until the runner acked seq 2, then end the stream.
			for {
				m, err := stream.Recv()
				if err != nil {
					return err
				}
				if m.GetAck().GetAckSeq() == 2 {
					return nil
				}
			}
		case 2:
			// Same backend boot: seq 2 replayed is a duplicate, seq 3 is new.
			for _, m := range []*runnerpb.BackendMessage{accept("b1"), startMsg(2, "r2"), startMsg(3, "r3")} {
				if err := stream.Send(m); err != nil {
					return err
				}
			}
			for {
				m, err := stream.Recv()
				if err != nil {
					return err
				}
				if m.GetAck().GetAckSeq() == 3 {
					return nil
				}
			}
		default:
			// Backend restarted: seq counts from 1 again.
			for _, m := range []*runnerpb.BackendMessage{accept("b2"), startMsg(1, "r4")} {
				if err := stream.Send(m); err != nil {
					return err
				}
			}
		}
		<-stream.Context().Done()
		return nil
	}
	exec := newRecordingExecutor(make(chan struct{})) // never finishes runs
	startClient(t, fb.pki, fb.ln.Addr().String(), "r1", func(o *Options) { o.Executor = exec })

	require.Eventually(t, func() bool { return len(exec.startedRuns()) >= 4 }, 10*time.Second, 10*time.Millisecond)
	assert.ElementsMatch(t, []string{"r1", "r2", "r3", "r4"}, exec.startedRuns())

	reg3 := fb.register(2)
	assert.Equal(t, uint64(3), reg3.GetLastAckedBackendSeq())
	assert.Equal(t, "b1", reg3.GetLastSeenBackendBootId())
	reg2 := fb.register(1)
	assert.Equal(t, uint64(2), reg2.GetLastAckedBackendSeq())
	assert.Equal(t, "b1", reg2.GetLastSeenBackendBootId())
	assert.Len(t, reg2.GetActiveRuns(), 2, "runs still executing are reported")
}

func TestFullQueueDoesNotWedgeReader(t *testing.T) {
	t.Parallel()
	acked := make(chan uint64, 16)
	gotResult := make(chan *runnerpb.RunResult, 1)
	fb := newFakeBackend(t, func(_ int, _ *runnerpb.Register, stream grpc.BidiStreamingServer[runnerpb.RunnerMessage, runnerpb.BackendMessage]) error {
		if err := stream.Send(accept("b1")); err != nil {
			return err
		}
		if err := stream.Send(startMsg(1, "r1")); err != nil {
			return err
		}
		for {
			m, err := stream.Recv()
			if err != nil {
				return err
			}
			if a := m.GetAck(); a != nil {
				acked <- a.GetAckSeq()
				// Acknowledge the three upstream events with a real Ack frame.
				if err := stream.Send(&runnerpb.BackendMessage{Payload: &runnerpb.BackendMessage_Ack{Ack: &runnerpb.Ack{AckSeq: 3}}}); err != nil {
					return err
				}
			}
			if r := m.GetRunResult(); r != nil {
				gotResult <- r
				return nil
			}
		}
	})
	c := New(Options{
		Addr: fb.ln.Addr().String(), NodeID: "r1", TLSConfig: fb.pki.clientTLS("r1"),
		HeartbeatInterval: time.Hour, BackoffMin: 20 * time.Millisecond, BackoffMax: 100 * time.Millisecond,
		MaxQueued: 3,
	})
	// Fill the unacked queue before connecting; the fake never acks upstream.
	for i := 1; i <= 3; i++ {
		require.NoError(t, c.Event(context.Background(), &runnerpb.RunEvent{RunId: "x", EventIndex: uint64(i)}))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	// The StartRun's result blocks on the full queue, yet the reader still
	// processes the backend's frames and acks them.
	select {
	case seq := <-acked:
		assert.Equal(t, uint64(1), seq)
	case <-time.After(5 * time.Second):
		t.Fatal("downstream ack not sent while queue is full")
	}
	// The backend's Ack frame is consumed by the reader, draining the
	// queue and unblocking the result.
	select {
	case r := <-gotResult:
		assert.Equal(t, "r1", r.GetRunId())
		assert.Equal(t, ErrNotImplemented, r.GetError())
	case <-time.After(5 * time.Second):
		t.Fatal("result not delivered after queue drained")
	}
	assert.LessOrEqual(t, c.Queued(), 1)
}

// orderExecutor blocks StartRun until released and records call order.
type orderExecutor struct {
	mu      sync.Mutex
	calls   []string
	release chan struct{}
}

func (e *orderExecutor) StartRun(_ context.Context, sr *runnerpb.StartRun, _ Sink) {
	<-e.release
	e.mu.Lock()
	e.calls = append(e.calls, "start:"+sr.GetRunId())
	e.mu.Unlock()
}

func (e *orderExecutor) CancelRun(id string) {
	e.mu.Lock()
	e.calls = append(e.calls, "cancel:"+id)
	e.mu.Unlock()
}

func TestCancelDeliveredAfterBlockedStart(t *testing.T) {
	t.Parallel()
	exec := &orderExecutor{release: make(chan struct{})}
	fb := newFakeBackend(t, func(_ int, _ *runnerpb.Register, stream grpc.BidiStreamingServer[runnerpb.RunnerMessage, runnerpb.BackendMessage]) error {
		for _, m := range []*runnerpb.BackendMessage{
			accept("b1"), startMsg(1, "r1"),
			{Seq: 2, Payload: &runnerpb.BackendMessage_CancelRun{CancelRun: &runnerpb.CancelRun{RunId: "r1"}}},
		} {
			if err := stream.Send(m); err != nil {
				return err
			}
		}
		<-stream.Context().Done()
		return nil
	})
	startClient(t, fb.pki, fb.ln.Addr().String(), "r1", func(o *Options) { o.Executor = exec })

	// The reader is not blocked by the start: both frames get acked.
	require.Eventually(t, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return fb.attempts >= 1
	}, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	exec.mu.Lock()
	assert.Empty(t, exec.calls, "cancel must wait for the start")
	exec.mu.Unlock()
	close(exec.release)
	require.Eventually(t, func() bool {
		exec.mu.Lock()
		defer exec.mu.Unlock()
		return len(exec.calls) == 2
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"start:r1", "cancel:r1"}, exec.calls)
}

func TestEnqueueRejectsUnencodableMessage(t *testing.T) {
	t.Parallel()

	c := New(Options{Addr: "127.0.0.1:1", NodeID: "r1"})
	err := c.Event(context.Background(), &runnerpb.RunEvent{RunId: "x", Content: "bad \xff utf8"})
	require.ErrorIs(t, err, ErrInvalidMessage)

	c.mu.Lock()
	queued := len(c.queue)
	next := c.nextSeq
	c.mu.Unlock()
	assert.Zero(t, queued, "the invalid message must not enter the queue")
	assert.Zero(t, next, "no seq is consumed")

	require.NoError(t, c.Event(context.Background(), &runnerpb.RunEvent{RunId: "x", Content: "fine"}))
}

func TestHeartbeatSanitizesQuotaStrings(t *testing.T) {
	t.Parallel()

	c := New(Options{
		Addr: "127.0.0.1:1", NodeID: "r1",
		Quotas: func() []*runnerpb.TargetQuota {
			return []*runnerpb.TargetQuota{{Cli: "op\xffen", Model: "m\xfe", Remaining: 0.5, Enabled: true}}
		},
	})
	c.sendHeartbeat()

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.queue, 1)
	q := c.queue[0].GetHeartbeat().GetQuotas()[0]
	assert.Equal(t, "op\uFFFDen", q.GetCli())
	assert.Equal(t, "m\uFFFD", q.GetModel())
	_, err := proto.Marshal(c.queue[0])
	assert.NoError(t, err)
}
