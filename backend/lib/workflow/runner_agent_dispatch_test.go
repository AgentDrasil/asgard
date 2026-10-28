package workflow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// fanout is a status listener that also accepts published updates.
type fanout struct {
	mu        sync.Mutex
	listeners []fanoutListener
}

type fanoutListener struct {
	ch    chan AgentStatusUpdate
	match func(AgentStatusUpdate) bool
}

func (f *fanout) AddStatusListener(_ string, match func(AgentStatusUpdate) bool) (<-chan AgentStatusUpdate, func()) {
	l := fanoutListener{ch: make(chan AgentStatusUpdate, 64), match: match}
	f.mu.Lock()
	f.listeners = append(f.listeners, l)
	f.mu.Unlock()
	return l.ch, func() {}
}

func (f *fanout) PublishStatusUpdate(u AgentStatusUpdate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.listeners {
		if l.match == nil || l.match(u) {
			l.ch <- u
		}
	}
}

// scriptedDispatcher records requests and replays events/results per call.
type scriptedDispatcher struct {
	mu       sync.Mutex
	requests []dispatch.Request
	steps    []func(req dispatch.Request) (dispatch.Result, error)
}

func (d *scriptedDispatcher) Run(_ context.Context, req dispatch.Request) (dispatch.Result, error) {
	d.mu.Lock()
	i := len(d.requests)
	d.requests = append(d.requests, req)
	step := d.steps[i]
	d.mu.Unlock()
	return step(req)
}

func agentRunEvent(req dispatch.Request, idx uint64, content string, artifacts ...string) *runnerpb.RunEvent {
	ev := &runnerpb.RunEvent{
		RunId: "r", ChatId: req.ChatID, NodeId: req.Scope.NodeID, RunToken: req.Scope.RunToken,
		EventIndex: idx, StepIndex: int32(idx), Source: "aw", EntryType: "tool_call", Content: content,
		MetadataJson: []byte(`{"target_files":["/tmp/ignored-by-backend.md"]}`),
	}
	for _, a := range artifacts {
		ev.Artifacts = append(ev.Artifacts, &runnerpb.Artifact{ViewerPath: a})
	}
	return ev
}

func newDispatchTestRunner(t *testing.T, d dispatch.Dispatcher, l AgentStatusListener, agent *agentspec.Agent) NodeRunner {
	t.Helper()
	r := NewAgentRunnerWithDispatcher(nil, nil, l, d, nil)
	r.(interface{ SetAgents([]*agentspec.Agent) }).SetAgents([]*agentspec.Agent{agent})
	return r
}

func TestStatusUpdateFromRunEvent(t *testing.T) {
	u := StatusUpdateFromRunEvent(&runnerpb.RunEvent{
		ChatId: "c", NodeId: "n", RunToken: "t", StepIndex: 4, Source: "aw", EntryType: "tool_call", Content: "x",
		MetadataJson: []byte(`{"target_files":["a"],"model":"m"}`),
		Artifacts:    []*runnerpb.Artifact{{ViewerPath: "/tmp/a.md"}, {ViewerPath: ""}},
	})
	assert.Equal(t, "c", u.ChatID)
	assert.Equal(t, "n", u.NodeID)
	assert.Equal(t, "t", u.RunToken)
	assert.Equal(t, 4, u.StepIndex)
	assert.Equal(t, "x", u.Content)
	assert.Equal(t, "m", u.Metadata["model"])
	assert.True(t, u.ArtifactsKnown)
	assert.Equal(t, []string{"/tmp/a.md"}, u.Artifacts)

	bad := StatusUpdateFromRunEvent(&runnerpb.RunEvent{MetadataJson: []byte(`{`)})
	assert.Nil(t, bad.Metadata)
}

func TestAgentRunner_DispatchesAndUsesRunnerArtifacts(t *testing.T) {
	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{ID: "a", Name: "A", CLI: []agentspec.CLITarget{{CLI: "opencode", Model: "m1"}}},
		Hash:   "h",
	}
	listener := &fanout{}
	disp := &scriptedDispatcher{}
	disp.steps = []func(dispatch.Request) (dispatch.Result, error){
		func(req dispatch.Request) (dispatch.Result, error) {
			req.OnEvent(agentRunEvent(req, 1, "step one", "/tmp/out.md"))
			req.OnEvent(agentRunEvent(req, 2, "step two", "/tmp/out.md", "/session/notes.md"))
			return dispatch.Result{Output: []byte(`{"last_content":"done","session_id":"s-1"}`), Target: agentspec.CLITarget{CLI: "opencode", Model: "m1"}}, nil
		},
	}
	runner := newDispatchTestRunner(t, disp, listener, agent)

	var mu sync.Mutex
	var emitted []WorkflowEvent
	nctx := &NodeContext{
		SessionID:         "chat-1",
		RunDir:            "/work",
		TmpDir:            t.TempDir(),
		Input:             "go",
		Headless:          true,
		AllowCrossSession: true,
		Node:              &workflowspec.NodeSpec{ID: "n1", Type: workflowspec.NodeTypeAgent, AgentID: "a", Entry: true},
		Values:            &RunValues{},
		WorkflowRunDirs:   []string{"/work"},
		EventEmitter: func(ev WorkflowEvent) {
			mu.Lock()
			emitted = append(emitted, ev)
			mu.Unlock()
		},
	}
	res, err := runner.Run(context.Background(), nctx)
	require.NoError(t, err)

	assert.Equal(t, workflowspec.StatusSucceeded, res.Status)
	assert.Equal(t, "done", res.Output)
	assert.Equal(t, "opencode", res.CLI)
	assert.Equal(t, map[string]string{"/tmp/out.md": "/tmp/out.md", "/session/notes.md": "/session/notes.md"}, res.Artifacts)

	require.Len(t, disp.requests, 1)
	req := disp.requests[0]
	assert.Equal(t, "chat-1", req.ChatID)
	assert.Equal(t, uint32(1), req.Attempt)
	assert.Equal(t, "n1", req.Scope.NodeID)
	assert.NotEmpty(t, req.Scope.RunToken)
	assert.True(t, req.Scope.Headless)
	assert.True(t, req.Scope.AllowCrossSession)
	assert.Equal(t, "go", req.Prompt)
	assert.Equal(t, []string{"/work"}, req.Agent.Config.RunDirs, "the effective agent carries the workflow run_dirs overlay")
	assert.Equal(t, "/work", req.RunDir.Unwrap())

	// Every status event reached the emitter with the runner's artifacts.
	mu.Lock()
	defer mu.Unlock()
	var steps []WorkflowEvent
	for _, ev := range emitted {
		if ev.Type == EventNodeStatusUpdate {
			steps = append(steps, ev)
		}
	}
	require.Len(t, steps, 2)
	assert.Equal(t, []string{"/tmp/out.md"}, steps[0].Artifacts)
	assert.Equal(t, []string{"/tmp/out.md", "/session/notes.md"}, steps[1].Artifacts)
}

func TestAgentRunner_QuotaRetryGetsNewAttempt(t *testing.T) {
	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{ID: "a", Name: "A", CLI: []agentspec.CLITarget{{CLI: "opencode", Model: "m1"}}},
	}
	disp := &scriptedDispatcher{}
	disp.steps = []func(dispatch.Request) (dispatch.Result, error){
		func(dispatch.Request) (dispatch.Result, error) {
			return dispatch.Result{}, &run.NoQuotaError{AgentID: "a", MinThreshold: 0.1, Targets: []run.QuotaTargetStatus{{CLI: "opencode", Model: "m1", Enabled: true}}}
		},
		func(dispatch.Request) (dispatch.Result, error) {
			return dispatch.Result{Output: []byte(`{"last_content":"ok"}`), Target: agentspec.CLITarget{CLI: "opencode", Model: "m1"}}, nil
		},
	}
	runner := newDispatchTestRunner(t, disp, &fanout{}, agent)

	suspends := 0
	nctx := &NodeContext{
		SessionID: "chat-q", RunDir: "/work", TmpDir: t.TempDir(), Input: "go",
		Node:   &workflowspec.NodeSpec{ID: "n1", Type: workflowspec.NodeTypeAgent, AgentID: "a", Entry: true},
		Values: &RunValues{},
		SuspendQuota: func(string, []string) (string, error) {
			suspends++
			return "Wait for quota recovery, then continue", nil
		},
	}
	res, err := runner.Run(context.Background(), nctx)
	require.NoError(t, err)
	assert.Equal(t, workflowspec.StatusSucceeded, res.Status)
	assert.Equal(t, 1, suspends)
	require.Len(t, disp.requests, 2)
	assert.Equal(t, uint32(1), disp.requests[0].Attempt)
	assert.Equal(t, uint32(2), disp.requests[1].Attempt, "the re-check after a quota decision is a new attempt")
	assert.Equal(t, disp.requests[0].Scope.RunToken, disp.requests[1].Scope.RunToken, "same logical node invocation")
}

func TestAgentRunner_NoQuotaFailsFastWhenHeadless(t *testing.T) {
	agent := &agentspec.Agent{Config: agentspec.AgentConfig{ID: "a", Name: "A", CLI: []agentspec.CLITarget{{CLI: "opencode", Model: "m1"}}}}
	disp := &scriptedDispatcher{steps: []func(dispatch.Request) (dispatch.Result, error){
		func(dispatch.Request) (dispatch.Result, error) {
			return dispatch.Result{}, &run.NoQuotaError{AgentID: "a"}
		},
	}}
	runner := newDispatchTestRunner(t, disp, &fanout{}, agent)
	res, err := runner.Run(context.Background(), &NodeContext{
		SessionID: "c", RunDir: "/w", TmpDir: t.TempDir(), Input: "x", Headless: true,
		Node: &workflowspec.NodeSpec{ID: "n", Type: workflowspec.NodeTypeAgent, AgentID: "a", Entry: true}, Values: &RunValues{},
	})
	require.NoError(t, err)
	assert.Equal(t, workflowspec.StatusFailed, res.Status)
	var nq *run.NoQuotaError
	require.ErrorAs(t, res.Error, &nq)
}

func TestAgentRunner_LookupUsesChatsRunnerAgents(t *testing.T) {
	global := &agentspec.Agent{Config: agentspec.AgentConfig{ID: "g"}}
	own := &agentspec.Agent{Config: agentspec.AgentConfig{ID: "own"}, NodeID: "r1"}
	r := NewAgentRunnerWithDispatcher(nil, nil, nil, &scriptedDispatcher{}, func(sessionID string) ([]*agentspec.Agent, bool) {
		if sessionID == "on-r1" {
			return []*agentspec.Agent{global, own}, true
		}
		return nil, false
	}).(*agentRunner)
	r.SetAgents([]*agentspec.Agent{global})

	got, err := r.lookupFor("on-r1", "own")
	require.NoError(t, err)
	assert.Equal(t, "r1", got.NodeID)

	_, err = r.lookupFor("other-chat", "own")
	assert.Error(t, err, "a chat without that runner does not see its agents")

	got, err = r.lookupFor("other-chat", "g")
	require.NoError(t, err)
	assert.Equal(t, "g", got.Config.ID)
}
