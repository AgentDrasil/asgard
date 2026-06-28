package agy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStatusLineFromSession(t *testing.T) {
	t.Parallel()
	// Create mock files in the /tmp/agystatusline equivalent structure or override it.
	// Since parseStatusLineFromSession is hardcoded to /tmp/agystatusline, let's write to it.
	// Or we can mock the environment/filepath if needed. Let's write to /tmp/agystatusline for real.
	err := os.MkdirAll("/tmp/agystatusline", 0755)
	require.NoError(t, err)

	sessionID := "test-session-statusline-parse"
	filePath := filepath.Join("/tmp/agystatusline", sessionID+".json")

	content := `{
		"session_id": "real-session-xyz",
		"context_window": {
			"total_input_tokens": 12345,
			"context_window_size": 100000,
			"remaining_percentage": 87.654
		}
	}`

	err = os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Remove(filePath)
	})

	gotSession, gotInput, gotMax, gotRemaining := parseStatusLineFromSession(sessionID)
	assert.Equal(t, "real-session-xyz", gotSession)
	assert.Equal(t, 12345, gotInput)
	assert.Equal(t, 100000, gotMax)
	assert.InDelta(t, 0.87654, gotRemaining, 1e-9)
}
