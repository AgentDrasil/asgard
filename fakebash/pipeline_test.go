package fakebash

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPipeline_FastPath_BothExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		totalBytes int64
		lineCount  int
		exitCode   int
		expected   bool
	}{
		{
			name:       "short success command e.g. pwd",
			totalBytes: 50,
			lineCount:  1,
			exitCode:   0,
			expected:   true,
		},
		{
			name:       "short error command e.g. ls /nonexistent",
			totalBytes: 80,
			lineCount:  2,
			exitCode:   2,
			expected:   true,
		},
		{
			name:       "boundary 255 bytes and 5 lines",
			totalBytes: 255,
			lineCount:  5,
			exitCode:   1,
			expected:   true,
		},
		{
			name:       "boundary 256 bytes - should not short-circuit",
			totalBytes: 256,
			lineCount:  3,
			exitCode:   0,
			expected:   false,
		},
		{
			name:       "exceeds line count 6 lines - should not short-circuit",
			totalBytes: 100,
			lineCount:  6,
			exitCode:   0,
			expected:   false,
		},
		{
			name:       "large output 1000 bytes",
			totalBytes: 1000,
			lineCount:  20,
			exitCode:   0,
			expected:   false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ShouldShortCircuit(tc.totalBytes, tc.lineCount, tc.exitCode)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestPipeline_FormatFooter_HumanReadable(t *testing.T) {
	t.Parallel()

	footer := FormatFooter(15000, 420, "c-12345-1-abc")
	assert.Contains(t, footer, "15KB -> 420B")
	assert.Contains(t, footer, "show-output c-12345-1-abc")
	assert.Contains(t, footer, "(or 'show-output' for the latest)")

	// Test FormatDropOnSuccess
	dropMsg := FormatDropOnSuccess(15000, "c-12345-1-abc")
	assert.Contains(t, dropMsg, "[fakebash: command succeeded with exit code 0. Verbose output truncated by sandbox]")
	assert.Contains(t, dropMsg, "15KB ->")
}
