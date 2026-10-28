package dispatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// fakeHandle replays scripted events and a result.
type fakeHandle struct {
	events []*runnerpb.RunEvent
	result *runnerhub.Result
	err    error
	// block, when set, makes Wait wait for ctx or release.
	block   bool
	release chan struct{}

	mu   sync.Mutex
	next int
}

func (h *fakeHandle) Next(ctx context.Context) (*runnerpb.RunEvent, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.next < len(h.events) {
		ev := h.events[h.next]
		h.next++
		return ev, nil
	}
	if h.block {
		h.mu.Unlock()
		<-ctx.Done()
		h.mu.Lock()
		return nil, ctx.Err()
	}
	return nil, errors.New("eof")
}

func (h *fakeHandle) Wait(ctx context.Context) (*runnerhub.Result, error) {
	if h.block {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-h.release:
		}
	}
	return h.result, h.err
}

type fakeHub struct {
	mu        sync.Mutex
	sent      []*runnerpb.StartRun
	nodes     []string
	cancelled []string
	handle    *fakeHandle
	err       error
}

func (h *fakeHub) Dispatch(_ context.Context, nodeID string, sr *runnerpb.StartRun) (Handle, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return nil, h.err
	}
	h.sent = append(h.sent, sr)
	h.nodes = append(h.nodes, nodeID)
	return h.handle, nil
}

func (h *fakeHub) Cancel(nodeID, runID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancelled = append(h.cancelled, nodeID+"/"+runID)
	if h.handle != nil && h.handle.block {
		close(h.handle.release)
	}
	return nil
}

func newTestRemote(hub *fakeHub, conf *config.Config) *Remote {
	d := NewRemote(hub, func(string) (string, error) { return "r1", nil }, conf)
	d.timeout = time.Second
	return d
}

func okResult(out string) *runnerhub.Result {
	return &runnerhub.Result{
		Output: []byte(out),
		RunResult: &runnerpb.RunResult{
			SelectedTarget: &runnerpb.Target{Cli: "opencode", Model: "m1"},
			RunDir:         "/runner/dir",
		},
	}
}

func testAgent() *agentspec.Agent {
	return &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:        "a",
			RunDirs:   []string{"/work", "/other"},
			MountDirs: agentspec.MountConfig{ReadOnly: []string{"/ro"}, ReadWrite: []string{"/rw"}},
		},
		Hash: "hash-1",
	}
}

func TestStartRun_Construction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	conf := &config.Config{Providers: []string{"opencode", "claude"}}
	d := newTestRemote(&fakeHub{}, conf)

	agent := testAgent()
	sr, err := d.StartRun(Request{
		ChatID:     "chat-1",
		Agent:      agent,
		Candidates: []agentspec.CLITarget{{CLI: "opencode", Model: "m1"}},
		Prompt:     "hello",
		Sessions:   run.SessionMap{"opencode": "s-1"},
		RunDir:     optional.Some(agents.GetSessionTmpBaseDir("chat-1") + "/sub"),
		Model:      optional.Some("m1"),
		Scope:      run.StatusScope{NodeID: "n", RunToken: "tok", Headless: true, AllowCrossSession: true},
		Attempt:    3,
	})
	require.NoError(t, err)

	assert.Equal(t, "chat-1", sr.GetChatId())
	assert.Equal(t, "a", sr.GetAgentId())
	assert.Equal(t, "hash-1", sr.GetAgentHash())
	assert.Equal(t, runnerpb.AgentSource_AGENT_SOURCE_GLOBAL, sr.GetAgentSource())
	assert.Equal(t, uint32(3), sr.GetAttempt())
	assert.Equal(t, "hello", sr.GetPrompt())
	assert.Equal(t, "m1", sr.GetModel())
	assert.Equal(t, map[string]string{"opencode": "s-1"}, sr.GetSessions())
	require.Len(t, sr.GetCandidates(), 1)
	assert.Equal(t, "opencode", sr.GetCandidates()[0].GetCli())
	assert.Equal(t, &runnerpb.StatusScope{NodeId: "n", RunToken: "tok", Headless: true, AllowCrossSession: true}, sr.GetStatusScope())

	// The per-chat tmp dir travels as a TMP ref, never as a backend host path.
	assert.Equal(t, runnerpb.FileScope_FILE_SCOPE_TMP, sr.GetRunDir().GetScope())
	assert.Equal(t, "sub", sr.GetRunDir().GetRelOrPath())

	// Workflow-effective overlay comes from the effective agent config.
	assert.Equal(t, []string{"/work", "/other"}, sr.GetRunDirs())
	assert.Equal(t, []string{"/ro"}, sr.GetMountDirsReadonly())
	assert.Equal(t, []string{"/rw"}, sr.GetMountDirsReadwrite())
	assert.ElementsMatch(t, []string{"opencode", "claude"}, sr.GetEnabledProviders())
}

func TestStartRun_RunnerAgentSourceAndDirRefs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newTestRemote(&fakeHub{}, nil)
	agent := testAgent()
	agent.NodeID = "r1"

	cases := []struct {
		name   string
		dir    string
		scope  runnerpb.FileScope
		rel    string
		isNull bool
	}{
		{"tmp root", agents.GetSessionTmpBaseDir("c"), runnerpb.FileScope_FILE_SCOPE_TMP, "", false},
		{"session sub", agents.GetSessionScopedBaseDir("session", "c") + "/x/y", runnerpb.FileScope_FILE_SCOPE_SESSION, "x/y", false},
		{"absolute", "/workspace/proj", runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, "/workspace/proj", false},
		{"other chat tmp is not rewritten", agents.GetSessionTmpBaseDir("other"), runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, agents.GetSessionTmpBaseDir("other"), false},
		{"none", "", 0, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{ChatID: "c", Agent: agent}
			if tc.dir != "" {
				req.RunDir = optional.Some(tc.dir)
			}
			sr, err := d.StartRun(req)
			require.NoError(t, err)
			assert.Equal(t, runnerpb.AgentSource_AGENT_SOURCE_RUNNER, sr.GetAgentSource())
			if tc.isNull {
				assert.Nil(t, sr.GetRunDir())
				return
			}
			assert.Equal(t, tc.scope, sr.GetRunDir().GetScope())
			assert.Equal(t, tc.rel, sr.GetRunDir().GetRelOrPath())
		})
	}
}

func TestStartRun_NoProviderEnabledFails(t *testing.T) {
	d := newTestRemote(&fakeHub{}, &config.Config{Providers: []string{"unsupported"}})
	_, err := d.StartRun(Request{ChatID: "c", Agent: testAgent()})
	require.Error(t, err)
}

func TestRun_FreshRunIDPerAttemptAndEventsBeforeReturn(t *testing.T) {
	hub := &fakeHub{handle: &fakeHandle{
		events: []*runnerpb.RunEvent{{EventIndex: 1, Content: "a"}, {EventIndex: 2, Content: "b"}},
		result: okResult("out"),
	}}
	d := newTestRemote(hub, nil)

	var got []string
	res, err := d.Run(context.Background(), Request{
		ChatID: "c", Agent: testAgent(), Attempt: 1,
		OnEvent: func(ev *runnerpb.RunEvent) { got = append(got, ev.GetContent()) },
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, got, "all events are delivered, in order, before Run returns")
	assert.Equal(t, "out", string(res.Output))
	assert.Equal(t, agentspec.CLITarget{CLI: "opencode", Model: "m1"}, res.Target)
	assert.Equal(t, "/runner/dir", res.RunDir)

	hub.handle = &fakeHandle{result: okResult("again")}
	_, err = d.Run(context.Background(), Request{ChatID: "c", Agent: testAgent(), Attempt: 2})
	require.NoError(t, err)

	require.Len(t, hub.sent, 2)
	assert.NotEmpty(t, hub.sent[0].GetRunId())
	assert.NotEqual(t, hub.sent[0].GetRunId(), hub.sent[1].GetRunId(), "every attempt gets a fresh run id")
	assert.Equal(t, uint32(1), hub.sent[0].GetAttempt())
	assert.Equal(t, uint32(2), hub.sent[1].GetAttempt())
	assert.Equal(t, []string{"r1", "r1"}, hub.nodes)
}

func TestRun_QuotaRetryLoopUsesFreshRunIDs(t *testing.T) {
	hub := &fakeHub{}
	d := newTestRemote(hub, nil)
	noQuota := &runnerhub.Result{RunResult: &runnerpb.RunResult{
		Error: "no quota",
		NoQuota: &runnerpb.NoQuota{
			AgentId: "a", ExplicitModel: "m1", MinThreshold: 0.1,
			Targets:     []*runnerpb.TargetQuota{{Cli: "opencode", Model: "m1", Remaining: 0.05, Enabled: true}},
			PairingNote: "note",
		},
	}}
	hub.handle = &fakeHandle{result: noQuota}
	_, err := d.Run(context.Background(), Request{ChatID: "c", Agent: testAgent(), Attempt: 1})
	var nq *run.NoQuotaError
	require.ErrorAs(t, err, &nq)
	assert.Equal(t, "a", nq.AgentID)
	assert.Equal(t, "m1", nq.ExplicitModel)
	assert.Equal(t, "note", nq.PairingNote)
	assert.Equal(t, []run.QuotaTargetStatus{{CLI: "opencode", Model: "m1", Remaining: 0.05, Enabled: true}}, nq.Targets)

	hub.handle = &fakeHandle{result: okResult("fine")}
	_, err = d.Run(context.Background(), Request{ChatID: "c", Agent: testAgent(), Attempt: 2})
	require.NoError(t, err)
	assert.NotEqual(t, hub.sent[0].GetRunId(), hub.sent[1].GetRunId())
}

func TestRun_RunnerErrorKeepsOutputAndTarget(t *testing.T) {
	res := okResult("partial")
	res.RunResult.Error = "agent crashed"
	hub := &fakeHub{handle: &fakeHandle{result: res}}
	out, err := newTestRemote(hub, nil).Run(context.Background(), Request{ChatID: "c", Agent: testAgent()})
	require.EqualError(t, err, "agent crashed")
	assert.Equal(t, "partial", string(out.Output))
	assert.Equal(t, "m1", out.Target.Model)
}

func TestRun_ContextCancelCancelsOnRunner(t *testing.T) {
	handle := &fakeHandle{block: true, release: make(chan struct{}), result: okResult("")}
	hub := &fakeHub{handle: handle}
	d := newTestRemote(hub, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := d.Run(ctx, Request{ChatID: "c", Agent: testAgent()})
		done <- err
	}()
	require.Eventually(t, func() bool { hub.mu.Lock(); defer hub.mu.Unlock(); return len(hub.sent) == 1 }, 5*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	require.Len(t, hub.cancelled, 1)
	assert.Equal(t, "r1/"+hub.sent[0].GetRunId(), hub.cancelled[0])
}

func TestRun_DispatchErrors(t *testing.T) {
	hub := &fakeHub{err: runnerhub.ErrRunnerOffline}
	_, err := newTestRemote(hub, nil).Run(context.Background(), Request{ChatID: "c", Agent: testAgent()})
	assert.ErrorIs(t, err, runnerhub.ErrRunnerOffline)

	hub = &fakeHub{handle: &fakeHandle{err: runnerhub.ErrRunLost}}
	_, err = newTestRemote(hub, nil).Run(context.Background(), Request{ChatID: "c", Agent: testAgent()})
	assert.ErrorIs(t, err, runnerhub.ErrRunLost)

	d := NewRemote(&fakeHub{}, func(string) (string, error) { return "", errors.New("not bound") }, nil)
	_, err = d.Run(context.Background(), Request{ChatID: "c", Agent: testAgent()})
	assert.EqualError(t, err, "run not dispatched: not bound")
	assert.ErrorIs(t, err, ErrNotDispatched)

	d = NewRemote(&fakeHub{}, func(string) (string, error) { return "", nil }, nil)
	_, err = d.Run(context.Background(), Request{ChatID: "c", Agent: testAgent()})
	assert.ErrorIs(t, err, ErrNotDispatched, "an unbound chat never reaches a runner")

	hub = &fakeHub{err: runnerhub.ErrRunnerOffline}
	_, err = newTestRemote(hub, nil).Run(context.Background(), Request{ChatID: "c", Agent: testAgent()})
	assert.ErrorIs(t, err, ErrNotDispatched)
	assert.ErrorIs(t, err, runnerhub.ErrRunnerOffline)

	_, err = Unavailable{Err: errors.New("no runner")}.Run(context.Background(), Request{})
	assert.ErrorIs(t, err, ErrNotDispatched)
}
