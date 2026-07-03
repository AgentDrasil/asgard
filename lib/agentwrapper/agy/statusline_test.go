package agy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStatusLineFromSession(t *testing.T) {
	oldDir := statuslineDir
	statuslineDir = t.TempDir()
	t.Cleanup(func() {
		statuslineDir = oldDir
	})

	sessionID := "test-session-statusline-parse"
	filePath := filepath.Join(statuslineDir, sessionID+".json")

	content := `{
		"session_id": "real-session-xyz",
		"context_window": {
			"total_input_tokens": 12345,
			"context_window_size": 100000,
			"remaining_percentage": 87.654
		}
	}`

	err := os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err)

	gotSession, gotInput, gotMax, gotRemaining := parseStatusLineFromSession(sessionID)
	assert.Equal(t, "real-session-xyz", gotSession)
	assert.Equal(t, 12345, gotInput)
	assert.Equal(t, 100000, gotMax)
	assert.InDelta(t, 0.87654, gotRemaining, 1e-9)
}
