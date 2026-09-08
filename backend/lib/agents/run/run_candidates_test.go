package run

import (
	"context"
	"errors"
	"testing"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func TestRunWithCandidates_ReplacesAgentList(t *testing.T) {
	tmpDir := setupTestRunEnv(t)
	agent := newTestAgent(t, tmpDir, []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-zero"},
	})
	candidates := []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-zero"}, // exhausted in the fake quota layer
		{CLI: "opencode", Model: "opencode-model-high"},
	}
	conf := &config.Config{Providers: []string{"agy", "opencode"}}

	out, target, err := RunWithCandidates(context.Background(), agent, candidates, "hello", nil, optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, conf)
	require.NoError(t, err)
	assert.Contains(t, string(out), "mock bwrap execution succeeded")
	assert.Equal(t, agentspec.CLITarget{CLI: "opencode", Model: "opencode-model-high"}, target)
}

func TestRunWithCandidates_AllExhausted_NoQuotaError(t *testing.T) {
	tmpDir := setupTestRunEnv(t)
	agent := newTestAgent(t, tmpDir, []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-high"},
	})
	candidates := []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-zero"},
	}
	conf := &config.Config{Providers: []string{"agy", "opencode"}}

	_, target, err := RunWithCandidates(context.Background(), agent, candidates, "hello", nil, optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, conf)
	require.Error(t, err)
	var nq *NoQuotaError
	require.True(t, errors.As(err, &nq))
	require.Len(t, nq.Targets, 1)
	assert.Equal(t, "agy-model-zero", nq.Targets[0].Model)
	assert.Empty(t, target)
}

func TestRunWithCandidates_ExplicitModelMatchedInCandidates(t *testing.T) {
	tmpDir := setupTestRunEnv(t)
	agent := newTestAgent(t, tmpDir, []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-high"},
	})
	candidates := []agentspec.CLITarget{
		{CLI: "opencode", Model: "opencode-model-high"},
	}
	conf := &config.Config{Providers: []string{"agy", "opencode"}}

	_, target, err := RunWithCandidates(context.Background(), agent, candidates, "hello", nil, optional.None[string](), optional.Some("opencode-model-high"), "test-chat", StatusScope{}, conf)
	require.NoError(t, err)
	assert.Equal(t, agentspec.CLITarget{CLI: "opencode", Model: "opencode-model-high"}, target)

	// A model only present in the agent's own list is no longer selectable
	// when candidates override the list.
	_, _, err = RunWithCandidates(context.Background(), agent, candidates, "hello", nil, optional.None[string](), optional.Some("agy-model-high"), "test-chat", StatusScope{}, conf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not in configured model list")
}

// TestRunWithCandidates_CLISwitchDropsForeignSession is the regression test
// for quota-driven CLI drift: a session opened by one CLI (e.g. simplest)
// must never be passed to a different CLI (e.g. opencode) selected later,
// because the new CLI cannot resume it and fails with "Session not found".
func TestRunWithCandidates_CLISwitchDropsForeignSession(t *testing.T) {
	tmpDir := setupTestRunEnv(t)
	agent := newTestAgent(t, tmpDir, []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-high"},
		{CLI: "opencode", Model: "opencode-model-high"},
	})
	conf := &config.Config{Providers: []string{"agy", "opencode"}}

	// Only simplest has a recorded session; quota selection picks opencode
	// (agy-model-high is 50% but agy-model-low would win — use an agent whose
	// agy entry is exhausted so opencode wins deterministically).
	exhaustedAgy := newTestAgent(t, tmpDir, []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-zero"},
		{CLI: "opencode", Model: "opencode-model-high"},
	})

	out, target, err := RunWithCandidates(context.Background(), exhaustedAgy, nil, "hello", SessionMap{"simplest": "simplest-session-1"}, optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, conf)
	require.NoError(t, err)
	assert.Equal(t, agentspec.CLITarget{CLI: "opencode", Model: "opencode-model-high"}, target)
	assert.Contains(t, string(out), "opencode-model-high")
	assert.NotContains(t, string(out), "simplest-session-1",
		"a foreign (simplest) session ID must not reach the opencode command line")

	// Same-CLI sessions are still resumed.
	out, _, err = RunWithCandidates(context.Background(), agent, nil, "hello", SessionMap{"agy": "agy-session-1"}, optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, conf)
	require.NoError(t, err)
	assert.Contains(t, string(out), "agy-session-1")
}
