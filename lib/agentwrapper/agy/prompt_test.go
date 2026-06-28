package agy

import (
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
