package exec

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// TestBackendDispatcherEndToEnd drives the backend's Remote dispatcher against
// a real runnerhub and runner connection executing in the fake sandbox.
func TestBackendDispatcherEndToEnd(t *testing.T) {
	setupFakeSandbox(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required by the fake sandbox")
	}
	require.NoError(t, os.MkdirAll(paths.ConfigDir(), 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigFile(), []byte("secret"), 0o600))

	const chatID = "chat-int"
	tmp := agents.GetSessionTmpBaseDir(chatID)
	require.NoError(t, os.MkdirAll(tmp, 0o755))

	hub := startStack(t, newIntegrationExecutor(t))
	remote := dispatch.NewRemote(dispatch.FromHub(hub), func(string) (string, error) { return testNode, nil }, nil)

	// The effective agent as the backend knows it: same id/hash as the
	// runner's definition, run_dirs overlay allowing the chat's tmp dir.
	agent := testAgent("a", "h1", "")
	agent.Config.RunDirs = []string{tmp}

	var mu sync.Mutex
	var events []*runnerpb.RunEvent
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := remote.Run(ctx, dispatch.Request{
		ChatID:     chatID,
		Agent:      agent,
		Candidates: []agentspec.CLITarget{{CLI: "opencode", Model: "m1"}},
		Prompt:     "do STATUSME please",
		RunDir:     optional.Some(tmp),
		Scope:      run.StatusScope{RunToken: "tok-1"},
		Attempt:    1,
		OnEvent: func(ev *runnerpb.RunEvent) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})
	require.NoError(t, err)
	assert.Contains(t, string(res.Output), "fake agent output")
	assert.Equal(t, agentspec.CLITarget{CLI: "opencode", Model: "m1"}, res.Target)
	assert.Equal(t, tmp, res.RunDir)

	mu.Lock()
	require.Len(t, events, 1, "the status update reached the backend as a RunEvent before Run returned")
	assert.Equal(t, "working", events[0].GetContent())
	assert.Equal(t, "tok-1", events[0].GetRunToken())
	require.Len(t, events[0].GetArtifacts(), 1)
	assert.Equal(t, "/tmp/out.md", events[0].GetArtifacts()[0].GetViewerPath())
	mu.Unlock()

	// Quota exhaustion on the runner comes back as the backend's typed error,
	// and a second Run is a fresh attempt.
	_, err = remote.Run(ctx, dispatch.Request{
		ChatID:     chatID,
		Agent:      agent,
		Candidates: []agentspec.CLITarget{{CLI: "opencode", Model: "m0"}},
		Prompt:     "again",
		RunDir:     optional.Some(tmp),
		Attempt:    2,
	})
	var nq *run.NoQuotaError
	require.ErrorAs(t, err, &nq)
	assert.Equal(t, "a", nq.AgentID)
}

// TestBackendDispatcherCancel checks that cancelling the context of Run stops
// the run on the runner through Hub.Cancel.
func TestBackendDispatcherCancel(t *testing.T) {
	setupFakeSandbox(t)
	const chatID = "chat-int"
	tmp := agents.GetSessionTmpBaseDir(chatID)
	require.NoError(t, os.MkdirAll(tmp, 0o755))

	hub := startStack(t, newIntegrationExecutor(t))
	remote := dispatch.NewRemote(dispatch.FromHub(hub), func(string) (string, error) { return testNode, nil }, nil)
	agent := testAgent("a", "h1", "")
	agent.Config.RunDirs = []string{tmp}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := remote.Run(ctx, dispatch.Request{
			ChatID: chatID, Agent: agent, Prompt: "HOLD", RunDir: optional.Some(tmp), Attempt: 1,
			Candidates: []agentspec.CLITarget{{CLI: "opencode", Model: "m1"}},
		})
		done <- err
	}()
	require.Eventually(t, func() bool {
		for _, n := range hub.Nodes() {
			if n.NodeID == testNode && n.ActiveRuns > 0 {
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	// The runner finished the cancelled run: nothing stays active.
	require.Eventually(t, func() bool {
		for _, n := range hub.Nodes() {
			if n.NodeID == testNode && n.ActiveRuns > 0 {
				return false
			}
		}
		return true
	}, 10*time.Second, 20*time.Millisecond)
}
