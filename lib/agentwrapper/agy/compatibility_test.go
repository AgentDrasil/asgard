package agy

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/agentwrapper"
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

	usageList, err := Usage(ctx, agentwrapper.UsageOptions{Dir: tempDir})
	require.NoError(t, err)
	assert.NotEmpty(t, usageList)
}

func TestCompatibility_Prompt(t *testing.T) {
	skipIfNotE2E(t)

	tempDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)

	promptResult, err := Prompt(ctx, "hello, respond back with exactly 'hello'", agentwrapper.PromptOptions{
		Dir: tempDir,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, promptResult.SessionID)
	assert.NotEmpty(t, promptResult.LastContent)
}

func TestCompatibility_PromptWithModel(t *testing.T) {
	skipIfNotE2E(t)

	tempDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	// Fetch models first
	usageList, err := Usage(ctx, agentwrapper.UsageOptions{Dir: tempDir})
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

	promptWithModelResult, err := Prompt(ctx, "hello, respond back with 'world'", agentwrapper.PromptOptions{
		Dir:   tempDir,
		Model: modelToUse,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, promptWithModelResult.SessionID)
	assert.NotEmpty(t, promptWithModelResult.LastContent)
}

func TestCompatibility_PromptResume(t *testing.T) {
	skipIfNotE2E(t)

	tempDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	// 1. Start a session by prompting the agent to remember a word
	promptResult, err := Prompt(ctx, "remember this word: banana", agentwrapper.PromptOptions{
		Dir: tempDir,
	})
	require.NoError(t, err)
	require.NotEmpty(t, promptResult.SessionID)
	require.NotEmpty(t, promptResult.LastContent)

	// 2. Resume session by passing the SessionID and asking what the word was
	resumeResult, err := Prompt(ctx, "what word did I ask you to remember?", agentwrapper.PromptOptions{
		Dir:       tempDir,
		SessionID: promptResult.SessionID,
	})
	require.NoError(t, err)
	assert.Equal(t, promptResult.SessionID, resumeResult.SessionID)
	assert.Contains(t, strings.ToLower(resumeResult.LastContent), "banana")
}
