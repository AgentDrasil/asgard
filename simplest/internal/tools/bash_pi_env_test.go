package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// runBashCommand executes a one-shot bash command through BashTool and
// returns the text result.
func runBashCommand(t *testing.T, tool *BashTool, command string) string {
	t.Helper()
	args, err := json.Marshal(map[string]any{"command": command})
	require.NoError(t, err)
	res, err := tool.Execute(context.Background(), "call-1", args, nil)
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	tc, ok := res.Content[0].(types.TextContent)
	require.True(t, ok, "unexpected content type %T", res.Content[0])
	return tc.Text
}

// Port of pi 005af57d8: run metadata is exposed as PI_* environment variables.
func TestBashTool_PIEnvironmentVariablesSet(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	tool.SetRunEnvironment(types.RunEnvironment{
		SessionID:     "sess-1",
		SessionFile:   "/tmp/sess-1.jsonl",
		Provider:      "acme",
		Model:         "model-x",
		ThinkingLevel: "high",
	})

	out := runBashCommand(t, tool, `printf '%s|%s|%s|%s|%s' "$PI_SESSION_ID" "$PI_SESSION_FILE" "$PI_PROVIDER" "$PI_MODEL" "$PI_REASONING_LEVEL"`)
	assert.Equal(t, "sess-1|/tmp/sess-1.jsonl|acme|model-x|high", out)
}

// Port of pi 005af57d8: empty fields are omitted.
func TestBashTool_PIEnvironmentVariablesEmptyOmitted(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	tool.SetRunEnvironment(types.RunEnvironment{
		Provider: "acme",
	})

	out := runBashCommand(t, tool, `printf '%s|%s|%s|%s|%s' "$PI_SESSION_ID" "$PI_SESSION_FILE" "$PI_PROVIDER" "$PI_MODEL" "$PI_REASONING_LEVEL"`)
	assert.Equal(t, "||acme||", out)
}

// Port of pi 005af57d8: only the five PI_* names are removed from the
// inherited environment; every other variable must survive untouched.
func TestBashTool_PIEnvironmentNonPIVariablesKept(t *testing.T) {
	t.Setenv("PI_ENV_CANARY", "kept")

	tool := NewBashTool(t.TempDir())
	out := runBashCommand(t, tool, `printf '%s' "$PI_ENV_CANARY"`)
	assert.Equal(t, "kept", out)
}

// Port of pi 005af57d8: stale inherited PI_* values are always deleted, even
// when no run environment is set.
func TestBashTool_PIEnvironmentVariablesStaleDeleted(t *testing.T) {
	t.Setenv("PI_SESSION_ID", "stale-id")
	t.Setenv("PI_SESSION_FILE", "stale-file")
	t.Setenv("PI_PROVIDER", "stale-provider")
	t.Setenv("PI_MODEL", "stale-model")
	t.Setenv("PI_REASONING_LEVEL", "stale-level")

	tool := NewBashTool(t.TempDir())
	out := runBashCommand(t, tool, `printf '%s|%s|%s|%s|%s' "$PI_SESSION_ID" "$PI_SESSION_FILE" "$PI_PROVIDER" "$PI_MODEL" "$PI_REASONING_LEVEL"`)
	assert.Equal(t, "||||", out)
}

// Exact-name matching: names merely prefixed by a PI_* name must survive.
func TestBashTool_PIEnvironmentExactNameMatching(t *testing.T) {
	t.Setenv("PI_MODEL_EXTRA", "survives")

	tool := NewBashTool(t.TempDir())
	out := runBashCommand(t, tool, `printf '%s' "$PI_MODEL_EXTRA"`)
	assert.Equal(t, "survives", out)
}
