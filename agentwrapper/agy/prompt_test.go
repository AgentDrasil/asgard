package agy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

func TestSplitModelVariant(t *testing.T) {
	tests := []struct {
		input     string
		wantModel string
		wantVar   string
	}{
		{"gemini-3.7-flash-low", "gemini-3.7-flash", "low"},
		{"gemini-3.7-flash-medium", "gemini-3.7-flash", "medium"},
		{"gemini-3.7-flash-high", "gemini-3.7-flash", "high"},
		{"gemini-3.7-flash/low", "gemini-3.7-flash", "low"},
		{"gemini-3.7-flash", "gemini-3.7-flash", ""},
		{"claude-3-7-sonnet-high", "claude-3-7-sonnet", "high"},
		// "-thinking" is a distinct agy model-name suffix, not an effort:
		// splitting it off produced `--effort thinking`, which agy rejects.
		{"claude-opus-4-6-thinking", "claude-opus-4-6-thinking", ""},
		{"claude-opus-4-6-thinking/high", "claude-opus-4-6-thinking", "high"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			base, variant := SplitModelVariant(tt.input)
			assert.Equal(t, tt.wantModel, base)
			assert.Equal(t, tt.wantVar, variant)
		})
	}
}

func TestBuildPromptArgv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		runDir   string
		opts     types.PromptOptions
		wantArgv []string
	}{
		{
			name:   "basic options without AddTmpToDir",
			runDir: "/workspace",
			opts: types.PromptOptions{
				SessionID: "sess-123",
				Model:     "gemini-3.7-flash",
			},
			wantArgv: []string{
				"agy", "--dangerously-skip-permissions",
				"--input-format", "stream-json",
				"--output-format", "stream-json",
				"--add-dir", "/workspace",
				"--conversation=sess-123",
				"--model", "gemini-3.7-flash",
			},
		},
		{
			name:   "with AddTmpToDir enabled",
			runDir: "/workspace",
			opts: types.PromptOptions{
				SessionID:   "sess-123",
				Model:       "gemini-3.7-flash-high",
				AddTmpToDir: true,
			},
			wantArgv: []string{
				"agy", "--dangerously-skip-permissions",
				"--input-format", "stream-json",
				"--output-format", "stream-json",
				"--add-dir", "/workspace",
				"--add-dir", "/tmp",
				"--conversation=sess-123",
				"--model", "gemini-3.7-flash",
				"--effort", "high",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			argv := buildPromptArgv(tt.runDir, tt.opts)
			assert.Equal(t, tt.wantArgv, argv)
		})
	}
}

func TestFormatPromptInput(t *testing.T) {
	t.Parallel()

	prompt := "Reply with exactly the word: apple. Nothing else."
	data, err := formatPromptInput(prompt)
	require.NoError(t, err)

	var parsed streamUserInput
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	assert.Equal(t, "user", parsed.Event)
	assert.Equal(t, prompt, parsed.Message.Content)
	assert.True(t, strings.HasSuffix(string(data), "\n"))
}
