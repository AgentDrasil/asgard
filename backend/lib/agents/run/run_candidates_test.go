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

	out, target, err := RunWithCandidates(context.Background(), agent, candidates, "hello", optional.None[string](), optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, conf)
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

	_, target, err := RunWithCandidates(context.Background(), agent, candidates, "hello", optional.None[string](), optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, conf)
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

	_, target, err := RunWithCandidates(context.Background(), agent, candidates, "hello", optional.None[string](), optional.None[string](), optional.Some("opencode-model-high"), "test-chat", StatusScope{}, conf)
	require.NoError(t, err)
	assert.Equal(t, agentspec.CLITarget{CLI: "opencode", Model: "opencode-model-high"}, target)

	// A model only present in the agent's own list is no longer selectable
	// when candidates override the list.
	_, _, err = RunWithCandidates(context.Background(), agent, candidates, "hello", optional.None[string](), optional.None[string](), optional.Some("agy-model-high"), "test-chat", StatusScope{}, conf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not in configured model list")
}
