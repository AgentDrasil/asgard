package api

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

type stubDispatcher struct {
	mu       sync.Mutex
	requests []dispatch.Request
	step     func(n int, req dispatch.Request) (dispatch.Result, error)
}

func (d *stubDispatcher) Run(_ context.Context, req dispatch.Request) (dispatch.Result, error) {
	d.mu.Lock()
	n := len(d.requests)
	d.requests = append(d.requests, req)
	d.mu.Unlock()
	return d.step(n, req)
}

func dispatchTestAgent() *agentspec.Agent {
	return &agentspec.Agent{
		Config: agentspec.AgentConfig{ID: "da", Name: "Dispatch Agent", CLI: []agentspec.CLITarget{{CLI: "opencode", Model: "m1"}}},
		Hash:   "h",
	}
}

func TestSingleAgentExecutor_RunEventsFlowIntoSessionWithRunnerArtifacts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)
	server.conf = &config.Config{}
	server.dispatcher = &stubDispatcher{step: func(_ int, req dispatch.Request) (dispatch.Result, error) {
		req.OnEvent(&runnerpb.RunEvent{
			RunId: "r", ChatId: req.ChatID, RunToken: req.Scope.RunToken, EventIndex: 1, StepIndex: 1,
			Source: "aw", EntryType: "tool_call", Content: "writing report",
			// The backend must trust the runner's artifacts, not re-derive them from target_files.
			MetadataJson: []byte(`{"target_files":["/tmp/not-an-artifact.txt"]}`),
			Artifacts:    []*runnerpb.Artifact{{ViewerPath: "/tmp/report.md"}},
		})
		return dispatch.Result{
			Output: []byte(`{"last_content":"all done","session_id":"sess-9"}`),
			Target: agentspec.CLITarget{CLI: "opencode", Model: "m1"},
		}, nil
	}}

	exec := NewSingleAgentExecutor(dispatchTestAgent(), server.conf, repo, server, nil)
	out, err := exec.Execute(context.Background(), SingleAgentRunParams{ChatID: "chat-ev", Prompt: "write it", RunDir: "/work"})
	require.NoError(t, err)
	assert.Equal(t, "all done", out)

	sess, err := repo.GetSession("chat-ev")
	require.NoError(t, err)
	var step, final bool
	for _, m := range sess.Messages {
		switch {
		case m.Content == "writing report":
			step = true
			assert.Equal(t, []string{"/tmp/report.md"}, m.ArtifactFiles)
		case m.Role == "assistant" && m.Content == "all done":
			final = true
		}
	}
	assert.True(t, step, "the RunEvent is recorded like a /agent-status update")
	assert.True(t, final)
	assert.Contains(t, sess.Artifacts, "/tmp/report.md")
	// The target and session id of the runner's result are persisted.
	require.NotEmpty(t, sess.Agents)
	assert.Equal(t, "sess-9", sess.Agents[0].Sessions["opencode"])
}

func TestSingleAgentExecutor_NoQuotaFromRunnerSuspendsAndRetriesWithNewAttempt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)
	disp := &stubDispatcher{step: func(n int, req dispatch.Request) (dispatch.Result, error) {
		if n == 0 {
			return dispatch.Result{}, &run.NoQuotaError{AgentID: "da", MinThreshold: 0.1, Targets: []run.QuotaTargetStatus{{CLI: "opencode", Model: "m1", Enabled: true}}}
		}
		return dispatch.Result{Output: []byte(`{"last_content":"ok"}`), Target: agentspec.CLITarget{CLI: "opencode", Model: "m1"}}, nil
	}}
	server.dispatcher = disp
	exec := NewSingleAgentExecutor(dispatchTestAgent(), server.conf, repo, server, nil)
	suspends := 0
	exec.suspendQuota = func(context.Context, string, string, string, []string) (string, error) {
		suspends++
		return "Wait for quota recovery, then continue", nil
	}
	out, err := exec.Execute(context.Background(), SingleAgentRunParams{ChatID: "chat-nq", Prompt: "p", RunDir: "/work"})
	require.NoError(t, err)
	assert.Equal(t, "ok", out)
	assert.Equal(t, 1, suspends)
	require.Len(t, disp.requests, 2)
	assert.Equal(t, uint32(1), disp.requests[0].Attempt)
	assert.Equal(t, uint32(2), disp.requests[1].Attempt)
	assert.NotEqual(t, disp.requests[0].Scope.RunToken, disp.requests[1].Scope.RunToken)
}

func TestSingleAgentExecutor_RunnerOfflineIsNotAnExecutionFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)
	server.dispatcher = &stubDispatcher{step: func(int, dispatch.Request) (dispatch.Result, error) {
		return dispatch.Result{}, runnerhub.ErrRunnerOffline
	}}
	exec := NewSingleAgentExecutor(dispatchTestAgent(), server.conf, repo, server, nil)
	_, err := exec.Execute(context.Background(), SingleAgentRunParams{ChatID: "chat-offl", Prompt: "p", RunDir: "/work"})
	require.ErrorIs(t, err, runnerhub.ErrRunnerOffline)
	var runErr *agentRunError
	assert.NotErrorAs(t, err, &runErr, "an offline runner must not clear the queue")
}
