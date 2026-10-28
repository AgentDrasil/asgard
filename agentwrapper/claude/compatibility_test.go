package claude

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

func skipIfNotE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_TEST") != "true" {
		t.Skip("Skipping compatibility test; set E2E_TEST=true to run it.")
	}
}

func TestCompatibility_ToolCallAndResume(t *testing.T) {
	skipIfNotE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	type call struct {
		entryType, content string
		meta               map[string]any
	}
	var calls []call
	cb := func(_ int, _, entryType, content string, meta map[string]any) {
		calls = append(calls, call{entryType, content, meta})
	}

	model := os.Getenv("CLAUDE_MODEL")
	if model == "" {
		model = "haiku"
	}
	dir := t.TempDir()

	res, err := Prompt(ctx, "Run `echo marker-7431` with the Bash tool, then reply with the word done.", types.PromptOptions{
		Dir: dir, Model: model, ReportCallback: cb,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, res.SessionID)
	assert.NotEmpty(t, res.LastContent)
	assert.Positive(t, res.InputTokens)

	var sawToolUse, sawToolResult bool
	for _, c := range calls {
		if c.entryType != "tool_call" || c.meta["tool_name"] != "Bash" {
			continue
		}
		if strings.Contains(c.content, "echo marker-7431") {
			sawToolUse = true
		}
		if strings.TrimSpace(c.content) == "marker-7431" {
			sawToolResult = true
		}
	}
	assert.True(t, sawToolUse, "expected a Bash tool_use entry, got %+v", calls)
	assert.True(t, sawToolResult, "expected a Bash tool_result entry, got %+v", calls)

	// Resume the same session: the model must remember the marker.
	res2, err := Prompt(ctx, "What exact string did the echo command print? Reply with only that string.", types.PromptOptions{
		Dir: dir, Model: model, SessionID: res.SessionID,
	})
	require.NoError(t, err)
	assert.Equal(t, res.SessionID, res2.SessionID)
	assert.Contains(t, res2.LastContent, "marker-7431")
}
