package agy

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agentwrapper/types"
)

func skipIfNotE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_TEST") != "true" {
		t.Skip("Skipping compatibility test; set E2E_TEST=true to run it.")
	}
}

func TestCompatibility_Usage(t *testing.T) {
	skipIfNotE2E(t)

	tempDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)

	usageList, err := Usage(ctx, types.UsageOptions{Dir: tempDir})
	require.NoError(t, err)
	assert.NotEmpty(t, usageList)
}

func TestCompatibility_Prompt(t *testing.T) {
	skipIfNotE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)

	promptResult, err := Prompt(ctx, "hello, respond back with exactly 'hello'", types.PromptOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, promptResult.SessionID)
	assert.NotEmpty(t, promptResult.LastContent)
}

func TestCompatibility_PromptWithModel(t *testing.T) {
	skipIfNotE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	// Fetch models first
	usageList, err := Usage(ctx, types.UsageOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, usageList)

	var modelToUse string
	for _, entry := range usageList {
		if entry.Model != "" {
			modelToUse = entry.Model
			break
		}
	}
	require.NotEmpty(t, modelToUse, "No model found in usage to test prompt with model")

	promptWithModelResult, err := Prompt(ctx, "hello, respond back with 'world'", types.PromptOptions{
		Model: modelToUse,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, promptWithModelResult.SessionID)
	assert.NotEmpty(t, promptWithModelResult.LastContent)
}

func TestCompatibility_PromptResume(t *testing.T) {
	skipIfNotE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	// 1. Start a session by prompting the agent to remember a word
	promptResult, err := Prompt(ctx, "remember this word: banana", types.PromptOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, promptResult.SessionID)
	require.NotEmpty(t, promptResult.LastContent)

	// 2. Resume session by passing the SessionID and asking what the word was
	resumeResult, err := Prompt(ctx, "what word did I ask you to remember?", types.PromptOptions{
		SessionID: promptResult.SessionID,
	})
	require.NoError(t, err)
	assert.Equal(t, promptResult.SessionID, resumeResult.SessionID)
	assert.Contains(t, strings.ToLower(resumeResult.LastContent), "banana")
}

func TestCompatibility_StreamParserGoVersion(t *testing.T) {
	skipIfNotE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	type reportedCall struct {
		stepIndex int
		source    string
		entryType string
		content   string
		metadata  map[string]any
	}

	var calls []reportedCall
	cb := func(stepIndex int, source, entryType, content string, metadata map[string]any) {
		calls = append(calls, reportedCall{stepIndex, source, entryType, content, metadata})
	}

	prompt := "what is the go version in path, and go version in current project"
	res, err := Prompt(ctx, prompt, types.PromptOptions{
		ReportCallback: cb,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, res.SessionID)
	assert.NotEmpty(t, res.LastContent)

	// Check that stream events were captured
	require.NotEmpty(t, calls, "expected stream events in ReportCallback")

	// Expect agent run command `go version`
	var ranGoVersion bool
	for _, call := range calls {
		if strings.Contains(strings.ToLower(call.content), "go version") {
			ranGoVersion = true
			break
		}
	}
	assert.True(t, ranGoVersion, "expected agent to run command 'go version' or emit 'go version' tool call")

	// Expect response to contain go version information
	lowerLast := strings.ToLower(res.LastContent)
	assert.Contains(t, lowerLast, "go")
}
