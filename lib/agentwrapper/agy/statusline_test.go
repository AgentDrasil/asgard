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

	gotInput, gotMax, gotRemaining := parseStatusLineFromSession(sessionID)
	assert.Equal(t, 12345, gotInput)
	assert.Equal(t, 100000, gotMax)
	assert.InDelta(t, 0.87654, gotRemaining, 1e-9)
}

func TestExtractSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name: "standard exit instruction",
			lines: []string{
				"Exiting...",
				"Resume: agy --conversation=94f0e306-4718-4c38-8883-4f2982f20176 (or -c)",
			},
			want: "94f0e306-4718-4c38-8883-4f2982f20176",
		},
		{
			name: "exit instruction with terminal padding",
			lines: []string{
				"Resume: agy --conversation=abcd-1234-efab (or -c)",
				"  ",
				"",
			},
			want: "abcd-1234-efab",
		},
		{
			name: "no exit instruction",
			lines: []string{
				"Process terminated cleanly",
			},
			want: "",
		},
		{
			name:  "empty lines",
			lines: []string{},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, extractSessionID(tt.lines))
		})
	}
}
