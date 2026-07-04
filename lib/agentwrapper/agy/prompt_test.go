package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadLastTranscriptContent(t *testing.T) {
	// Create a temp directory to act as HOME
	tempHome := t.TempDir()

	// Override HOME env variable
	oldHome := os.Getenv("HOME")
	err := os.Setenv("HOME", tempHome)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Setenv("HOME", oldHome)
	})

	sessionID := "test-session-transcript"
	logDir := filepath.Join(tempHome, ".gemini", "antigravity-cli", "brain", sessionID, ".system_generated", "logs")
	err = os.MkdirAll(logDir, 0755)
	require.NoError(t, err)

	transcriptPath := filepath.Join(logDir, "transcript.jsonl")

	// Write simulated transcript lines
	lines := []string{
		`{"type":"USER_INPUT","content":"Hello"}`,
		`{"type":"PLANNER_RESPONSE","content":"Hi there!"}`,
		`{"type":"CHECKPOINT","content":"Some checkpoint summary"}`,
	}

	err = os.WriteFile(transcriptPath, []byte(lines[0]+"\n"+lines[1]+"\n"+lines[2]+"\n"), 0644)
	require.NoError(t, err)

	// Call readLastTranscriptContent
	content, err := readLastTranscriptContent(sessionID)
	require.NoError(t, err)
	// The last content that is not CHECKPOINT should be "Hi there!"
	assert.Equal(t, "Hi there!", content)
}

func TestEnsureWorkspaceTrusted(t *testing.T) {
	tempHome := t.TempDir()

	oldHome := os.Getenv("HOME")
	err := os.Setenv("HOME", tempHome)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Setenv("HOME", oldHome)
	})

	cliDir := filepath.Join(tempHome, ".gemini", "antigravity-cli")
	err = os.MkdirAll(cliDir, 0755)
	require.NoError(t, err)

	settingsPath := filepath.Join(cliDir, "settings.json")

	// 1. Write an initial settings.json with a trusted workspace
	initialSettings := `{
  "model": "test-model",
  "trustedWorkspaces": [
    "/some/trusted/path"
  ]
}`
	err = os.WriteFile(settingsPath, []byte(initialSettings), 0644)
	require.NoError(t, err)

	// 2. Checking the already trusted path should succeed
	err = ensureWorkspaceTrusted("/some/trusted/path")
	require.NoError(t, err)

	// 3. Checking an untrusted path should add it to settings.json and succeed
	untrustedPath, err := filepath.Abs(".")
	require.NoError(t, err)
	untrustedPath = filepath.Clean(untrustedPath)

	err = ensureWorkspaceTrusted(untrustedPath)
	require.NoError(t, err)

	// 4. Verify settings.json was updated and contains the new path while preserving other keys
	updatedData, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var config struct {
		Model             string   `json:"model"`
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	err = json.Unmarshal(updatedData, &config)
	require.NoError(t, err)

	assert.Equal(t, "test-model", config.Model)
	assert.Contains(t, config.TrustedWorkspaces, "/some/trusted/path")
	assert.Contains(t, config.TrustedWorkspaces, untrustedPath)
}
