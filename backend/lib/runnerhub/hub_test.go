package runnerhub

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func TestHandshake(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cn         string
		mutate     func(*fakeRunner)
		wantErr    bool
		wantReason string
	}{
		{name: "accepted", cn: "runner-a"},
		{name: "register node id differs from cert CN", cn: "runner-a", mutate: func(f *fakeRunner) { f.registerNode = "runner-x" }, wantReason: "does not match certificate CN"},
		{name: "protocol mismatch", cn: "runner-a", mutate: func(f *fakeRunner) { f.protocol = 99 }, wantReason: "protocol version mismatch"},
		{name: "node not allowed", cn: "runner-evil", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := newTestEnv(t, nil, nil, "runner-a")
			f := env.newRunner(tt.cn)
			if tt.mutate != nil {
				tt.mutate(f)
			}
			err := f.connect()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, uint32(runnerpb.ProtocolVersion), f.regResult.GetProtocolVersion())
			assert.NotEmpty(t, f.regResult.GetBackendBootId())
			if tt.wantReason != "" {
				assert.False(t, f.regResult.GetAccepted())
				assert.Contains(t, f.regResult.GetRejectReason(), tt.wantReason)
				return
			}
			assert.True(t, f.regResult.GetAccepted())
			env.waitOnline("runner-a", true)

			nodes := env.hub.Nodes()
			require.Len(t, nodes, 1)
			assert.Equal(t, "v-test", nodes[0].Version)
			assert.Equal(t, map[string][]string{"opencode": {"m1"}}, nodes[0].SupportedCLIs)
			assert.False(t, nodes[0].LastSeen.IsZero())
		})
	}
}

func TestNodesIncludesAllowedOfflineNodes(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a", "runner-b")
	nodes := env.hub.Nodes()
	require.Len(t, nodes, 2)
	assert.Equal(t, "runner-a", nodes[0].NodeID)
	assert.False(t, nodes[0].Online)
}

func TestDispatchOfflineFailsFast(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	_, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.ErrorIs(t, err, ErrRunnerOffline)
	_, err = env.hub.Client("runner-a")
	require.ErrorIs(t, err, ErrRunnerOffline)
}

func TestDispatchValidation(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()

	_, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{})
	require.Error(t, err)

	big := make([]byte, maxPromptSize+1)
	_, err = env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "big", Prompt: string(big)})
	require.ErrorIs(t, err, ErrTooLarge)

	_, err = env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.NoError(t, err)
	_, err = env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.ErrorIs(t, err, ErrDuplicateRun)
}

func TestDispatchEventsChunksResult(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()

	run, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1", ChatId: "c1", Prompt: "hi"})
	require.NoError(t, err)
	assert.Equal(t, 1, env.hub.Nodes()[0].ActiveRuns)

	m := f.nextBackend()
	assert.Equal(t, uint64(1), m.GetSeq())
	assert.Equal(t, "r1", m.GetStartRun().GetRunId())

	f.sendEvent("r1", 1, "one")
	f.sendEvent("r1", 2, "two")
	f.sendChunk("r1", 0, "hel")
	f.sendChunk("r1", 3, "lo")
	last := f.sendResult(&runnerpb.RunResult{RunId: "r1", OutputSize: 5, SelectedTarget: &runnerpb.Target{Cli: "opencode", Model: "m1"}})

	res := mustWait(t, run)
	assert.Equal(t, "hello", string(res.Output))
	assert.Equal(t, "m1", res.RunResult.GetSelectedTarget().GetModel())
	f.waitAck(last)

	ctx := context.Background()
	for i, want := range []string{"one", "two"} {
		ev, err := run.Next(ctx)
		require.NoError(t, err, i)
		assert.Equal(t, want, ev.GetContent())
	}
	_, err = run.Next(ctx)
	require.ErrorIs(t, err, io.EOF)
	assert.Equal(t, 0, env.hub.Nodes()[0].ActiveRuns)
}

func TestResultOutputSizeMismatch(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	run, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.NoError(t, err)

	f.sendChunk("r1", 0, "abc")
	f.sendChunk("r1", 0, "abc") // replayed chunk is idempotent
	f.sendResult(&runnerpb.RunResult{RunId: "r1", OutputSize: 9})

	_, err = run.Wait(context.Background())
	require.ErrorContains(t, err, "output size mismatch")
}

func TestHeartbeatUpdatesQuota(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	seq := f.send(&runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_Heartbeat{Heartbeat: &runnerpb.Heartbeat{
		Quotas:  []*runnerpb.TargetQuota{{Cli: "opencode", Model: "m1", Remaining: 0.5, Enabled: true}},
		MaxRuns: 4,
	}}})
	f.waitAck(seq)

	n := env.hub.Nodes()[0]
	require.Len(t, n.Quotas, 1)
	assert.InDelta(t, 0.5, n.Quotas[0].GetRemaining(), 1e-9)
	assert.Equal(t, uint32(4), n.MaxRuns)
}

func TestDuplicateEventAndResultIgnored(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	run, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.NoError(t, err)

	// Same seq resent (replay duplicate) and same event_index under a new seq.
	seq := f.sendEvent("r1", 1, "one")
	require.NoError(t, f.stream.Send(&runnerpb.RunnerMessage{Seq: seq, Payload: &runnerpb.RunnerMessage_RunEvent{RunEvent: &runnerpb.RunEvent{RunId: "r1", EventIndex: 1, Content: "one-dup-seq"}}}))
	f.sendEvent("r1", 1, "one-dup-index")
	f.sendEvent("r1", 2, "two")
	f.sendResult(&runnerpb.RunResult{RunId: "r1", Error: "first"})
	last := f.sendResult(&runnerpb.RunResult{RunId: "r1", Error: "second"})
	f.waitAck(last)

	res := mustWait(t, run)
	assert.Equal(t, "first", res.RunResult.GetError())

	ctx := context.Background()
	for _, want := range []string{"one", "two"} {
		ev, err := run.Next(ctx)
		require.NoError(t, err)
		assert.Equal(t, want, ev.GetContent())
	}
	_, err = run.Next(ctx)
	require.ErrorIs(t, err, io.EOF)
}

func TestReplayWithSameRunnerBootID(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	run, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.NoError(t, err)
	require.Equal(t, uint64(1), f.nextBackend().GetSeq())

	// The runner never processed seq 1: the backend replays it.
	f.disconnect()
	f.mustConnect()
	m := f.nextBackend()
	assert.Equal(t, uint64(1), m.GetSeq())
	assert.Equal(t, "r1", m.GetStartRun().GetRunId())

	// Once the runner reports seq 1 processed, nothing is replayed and the
	// run stays pending because the runner lists it as active.
	f.lastBackendSeq = 1
	f.active = []*runnerpb.RunRef{{RunId: "r1"}}
	f.disconnect()
	f.mustConnect()
	f.expectNoBackend()

	f.sendResult(&runnerpb.RunResult{RunId: "r1"})
	mustWait(t, run)

	// New dispatches continue the seq space.
	_, err = env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r2"})
	require.NoError(t, err)
	assert.Equal(t, uint64(2), f.nextBackend().GetSeq())
}

func TestPendingRunSurvivesDisconnect(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	run, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.NoError(t, err)
	f.lastBackendSeq = f.nextBackend().GetSeq()
	f.active = []*runnerpb.RunRef{{RunId: "r1"}}

	f.disconnect()
	_, err = env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r2"})
	require.ErrorIs(t, err, ErrRunnerOffline)
	n := env.hub.Nodes()[0]
	assert.False(t, n.Online)
	assert.Equal(t, 1, n.ActiveRuns)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = run.Wait(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	f.mustConnect()
	f.sendResult(&runnerpb.RunResult{RunId: "r1"})
	mustWait(t, run)
}

func TestChangedRunnerBootIDReportsRunsLost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// acked reports whether the runner processed the StartRun before it
		// restarted (the run was executing, then died with the process).
		acked bool
	}{
		{"unacked start run is dropped", false},
		{"acked run unknown after restart", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := newTestEnv(t, nil, nil, "runner-a")
			f := env.newRunner("runner-a")
			f.mustConnect()
			run, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
			require.NoError(t, err)
			seq := f.nextBackend().GetSeq()
			if tt.acked {
				ack := &runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_Ack{Ack: &runnerpb.Ack{AckSeq: seq}}}
				require.NoError(t, f.stream.Send(ack))
			}
			f.disconnect()

			// Restarted runner: fresh boot id, counters and state.
			f.bootID = "runner-boot-2"
			f.seq = 0
			f.lastBackendSeq = 0
			f.lastBackendBoot = ""
			f.mustConnect()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err = run.Wait(ctx)
			require.ErrorIs(t, err, ErrRunLost)
			f.expectNoBackend()
			assert.Equal(t, 0, env.hub.Nodes()[0].ActiveRuns)
		})
	}
}

func TestFinalizedRunsSurviveHubRestart(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	env := newTestEnv(t, dbmodels.NewFinalizedRunRepository(testDB), nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()

	run, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.NoError(t, err)
	last := f.sendResult(&runnerpb.RunResult{RunId: "r1"})
	f.waitAck(last)
	mustWait(t, run)

	env.restartHub(dbmodels.NewFinalizedRunRepository(testDB))
	f.close()
	f.bootID = "runner-boot-1"
	f.lastBackendBoot = ""
	f.lastBackendSeq = 0
	f.mustConnect()

	// The finalized id cannot be dispatched again; a replayed result is a
	// no-op that is still acked.
	_, err = env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.ErrorIs(t, err, ErrAlreadyFinalized)
	last = f.sendResult(&runnerpb.RunResult{RunId: "r1", Error: "replayed"})
	f.waitAck(last)
}

func TestTrackedRunWithFinalizedIDStillDelivered(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	env := newTestEnv(t, store, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	run, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.NoError(t, err)

	// Simulates a crash after MarkFinalized but before delivery.
	_, err = store.MarkFinalized(context.Background(), "r1")
	require.NoError(t, err)
	f.sendResult(&runnerpb.RunResult{RunId: "r1", Error: "late"})

	res := mustWait(t, run)
	assert.Equal(t, "late", res.RunResult.GetError())
}

func TestResultFromNonOwnerIgnored(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	env := newTestEnv(t, store, nil, "runner-a", "runner-b")
	a := env.newRunner("runner-a")
	a.mustConnect()
	b := env.newRunner("runner-b")
	b.mustConnect()
	run, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.NoError(t, err)

	last := b.sendResult(&runnerpb.RunResult{RunId: "r1", Error: "imposter"})
	b.waitAck(last)
	done, err := store.IsFinalized(context.Background(), "r1")
	require.NoError(t, err)
	assert.False(t, done)

	a.sendResult(&runnerpb.RunResult{RunId: "r1", Error: "owner"})
	res := mustWait(t, run)
	assert.Equal(t, "owner", res.RunResult.GetError())
}

func TestClientCallsRunnerServiceOverYamux(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()

	client, err := env.hub.Client("runner-a")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.Tree(ctx, &runnerpb.TreeRequest{Path: "/x"})
	require.NoError(t, err)
	assert.Equal(t, "tree:/x", resp.GetPath())
}

type recordingCallbacks struct {
	runnerpb.UnimplementedAgentCallbackServiceServer
	nodeIDs chan string
}

func (c recordingCallbacks) FindPeer(ctx context.Context, _ *runnerpb.FindPeerRequest) (*runnerpb.FindPeerResponse, error) {
	id, _ := NodeIDFromContext(ctx)
	c.nodeIDs <- id
	return &runnerpb.FindPeerResponse{}, nil
}

func TestAgentCallbacks(t *testing.T) {
	t.Parallel()

	t.Run("default is unimplemented", func(t *testing.T) {
		t.Parallel()

		env := newTestEnv(t, nil, nil, "runner-a")
		f := env.newRunner("runner-a")
		f.mustConnect()
		_, err := runnerpb.NewAgentCallbackServiceClient(f.cc).AskUser(context.Background(), &runnerpb.AskUserRequest{})
		assert.Equal(t, codes.Unimplemented, status.Code(err))
	})

	t.Run("pluggable handler sees node id", func(t *testing.T) {
		t.Parallel()

		cb := recordingCallbacks{nodeIDs: make(chan string, 1)}
		env := newTestEnv(t, nil, cb, "runner-a")
		f := env.newRunner("runner-a")
		f.mustConnect()
		_, err := runnerpb.NewAgentCallbackServiceClient(f.cc).FindPeer(context.Background(), &runnerpb.FindPeerRequest{})
		require.NoError(t, err)
		assert.Equal(t, "runner-a", <-cb.nodeIDs)
	})
}

func TestSetCallbacksAppliesToEstablishedConnections(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	client := runnerpb.NewAgentCallbackServiceClient(f.cc)
	_, err := client.FindPeer(context.Background(), &runnerpb.FindPeerRequest{})
	require.Equal(t, codes.Unimplemented, status.Code(err))

	cb := recordingCallbacks{nodeIDs: make(chan string, 1)}
	env.hub.SetCallbacks(cb)
	_, err = client.FindPeer(context.Background(), &runnerpb.FindPeerRequest{})
	require.NoError(t, err)
	assert.Equal(t, "runner-a", <-cb.nodeIDs)
}

func TestActiveRun(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	_, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{
		RunId: "r1", ChatId: "chat-1", AgentId: "agent-1", Attempt: 2,
		StatusScope: &runnerpb.StatusScope{NodeId: "wf-node", RunToken: "tok"},
	})
	require.NoError(t, err)

	info, ok := env.hub.ActiveRun("r1", "runner-a")
	require.True(t, ok)
	assert.Equal(t, "chat-1", info.ChatID)
	assert.Equal(t, "agent-1", info.AgentID)
	assert.Equal(t, "wf-node", info.WorkflowNodeID)
	assert.Equal(t, "tok", info.RunToken)
	assert.Equal(t, uint32(2), info.Attempt)

	_, ok = env.hub.ActiveRun("r1", "runner-b")
	assert.False(t, ok, "another runner does not own the run")
	_, ok = env.hub.ActiveRun("nope", "runner-a")
	assert.False(t, ok)

	f.nextBackend()
	f.sendResult(&runnerpb.RunResult{RunId: "r1"})
	select {
	case <-info.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("Done was not closed when the run finished")
	}
	_, ok = env.hub.ActiveRun("r1", "runner-a")
	assert.False(t, ok, "a finished run is no longer active")
}

func TestCancelQueuesCancelRun(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	_, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.NoError(t, err)
	f.nextBackend()

	require.ErrorIs(t, env.hub.Cancel("runner-a", "nope"), ErrUnknownRun)
	require.NoError(t, env.hub.Cancel("runner-a", "r1"))
	m := f.nextBackend()
	assert.Equal(t, uint64(2), m.GetSeq())
	assert.Equal(t, "r1", m.GetCancelRun().GetRunId())
}

func TestAllowedNodesReloaded(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	env.setAllowed("runner-a", "runner-b")
	f := env.newRunner("runner-b")
	f.mustConnect()
}

func TestStopFailsPendingRuns(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	run, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.NoError(t, err)

	env.hub.Stop()
	_, err = run.Wait(context.Background())
	require.ErrorIs(t, err, ErrHubClosed)
}

func TestUntrackedActiveRunsGetCancelled(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	_, err := store.MarkFinalized(context.Background(), "done")
	require.NoError(t, err)
	env := newTestEnv(t, store, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.active = []*runnerpb.RunRef{{RunId: "ghost"}, {RunId: "done"}}
	f.mustConnect()

	m := f.nextBackend()
	assert.Equal(t, "ghost", m.GetCancelRun().GetRunId())
	f.expectNoBackend() // finalized runs are not cancelled again

	// Events, output and the result of the untracked run are ignored, but
	// the result still finalizes the id.
	f.sendEvent("ghost", 1, "x")
	last := f.sendResult(&runnerpb.RunResult{RunId: "ghost"})
	f.waitAck(last)
	done, err := store.IsFinalized(context.Background(), "ghost")
	require.NoError(t, err)
	assert.True(t, done)
}

func TestAllowedNodesShrinkClosesSession(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a", "runner-b")
	f := env.newRunner("runner-a")
	f.mustConnect()
	g := env.newRunner("runner-b")
	g.mustConnect()

	env.setAllowed("runner-b")
	_, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.ErrorIs(t, err, ErrRunnerOffline)
	_, err = env.hub.Client("runner-a")
	require.ErrorIs(t, err, ErrRunnerOffline)
	for _, n := range env.hub.Nodes() {
		if n.NodeID == "runner-a" {
			assert.False(t, n.Online)
		}
	}

	env.hub.RefreshAllowed()
	env.waitOnline("runner-b", true)
	select {
	case <-f.sess.CloseChan():
	case <-time.After(5 * time.Second):
		require.FailNow(t, "removed node's session was not closed")
	}
	_, err = env.hub.Client("runner-b")
	require.NoError(t, err)
	require.Error(t, f.connect())
}

func TestRegisterTimeoutCoversRegisterFrame(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	env.withRegisterTimeout(200 * time.Millisecond)
	f := env.newRunner("runner-a")
	f.skipRegister = true
	require.NoError(t, f.connect())

	// Connect is open but Register never arrives: the session is closed.
	done := make(chan error, 1)
	go func() {
		_, err := f.stream.Recv()
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "session was not closed")
	}
}

func TestConnectEndTearsDownSession(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()

	require.NoError(t, f.stream.CloseSend())
	env.waitOnline("runner-a", false)

	g := env.newRunner("runner-a")
	g.mustConnect()
}

func TestRejectReasonBounded(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.registerNode = strings.Repeat("x", 200<<10)
	require.NoError(t, f.connect())
	assert.False(t, f.regResult.GetAccepted())
	assert.Less(t, len(f.regResult.GetRejectReason()), 1024)
}

func TestTruncateErrorText(t *testing.T) {
	t.Parallel()

	short, cut := truncateErrorText("ok")
	assert.Equal(t, "ok", short)
	assert.False(t, cut)

	long := strings.Repeat("é", maxErrorTextSize) // 2 bytes per rune
	got, cut := truncateErrorText(long)
	assert.True(t, cut)
	assert.LessOrEqual(t, len(got), maxErrorTextSize)
	assert.True(t, utf8.ValidString(got))
}

func TestStopClosesConnectionsStuckInHandshake(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	raw, err := net.Dial("tcp", env.hub.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	// Give the accept loop time to pick the silent peer up.
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		env.hub.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		require.FailNow(t, "Stop blocked on a stalled handshake")
	}
}

type flakyListener struct {
	net.Listener
	mu    sync.Mutex
	fails int
}

type tempErr struct{}

func (tempErr) Error() string { return "temporary accept failure" }

func (l *flakyListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.fails > 0 {
		l.fails--
		l.mu.Unlock()
		return nil, tempErr{}
	}
	l.mu.Unlock()
	return l.Listener.Accept()
}

func TestAcceptLoopSurvivesTemporaryErrors(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, nil, "runner-a")
	addr := env.hub.Addr().String()
	env.hub.Stop()
	var ln net.Listener
	require.Eventually(t, func() bool {
		var err error
		ln, err = net.Listen("tcp", addr)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	env.hub = env.startHub(&flakyListener{Listener: ln, fails: 3})

	f := env.newRunner("runner-a")
	f.mustConnect()
}

type panicCallbacks struct {
	runnerpb.UnimplementedAgentCallbackServiceServer
}

func (panicCallbacks) FindPeer(context.Context, *runnerpb.FindPeerRequest) (*runnerpb.FindPeerResponse, error) {
	panic("boom")
}

func TestHandlerPanicDoesNotCrashHub(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, nil, panicCallbacks{}, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	client := runnerpb.NewAgentCallbackServiceClient(f.cc)
	_, err := client.FindPeer(context.Background(), &runnerpb.FindPeerRequest{})
	assert.Equal(t, codes.Internal, status.Code(err))
	// The connection still works.
	_, err = client.AskUser(context.Background(), &runnerpb.AskUserRequest{})
	assert.Equal(t, codes.Unimplemented, status.Code(err))
}
