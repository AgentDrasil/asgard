package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantErr     bool
		wantContain []string
	}{
		{
			name: "idle with high remaining (green)",
			input: `{
				"agent_state": "idle",
				"context_window": {
					"total_input_tokens": 88244,
					"context_window_size": 200000,
					"remaining_percentage": 91.58
				}
			}`,
			wantContain: []string{"IDLE | 88244/200000 (", "92%", ansiGreen},
		},
		{
			name: "thinking with medium remaining (yellow)",
			input: `{
				"agent_state": "thinking",
				"context_window": {
					"total_input_tokens": 500000,
					"context_window_size": 1000000,
					"remaining_percentage": 52.0
				}
			}`,
			wantContain: []string{"THINKING | 500000/1000000 (", "52%", ansiYellow},
		},
		{
			name: "working with low remaining (red)",
			input: `{
				"agent_state": "working",
				"context_window": {
					"total_input_tokens": 990000,
					"context_window_size": 1048576,
					"remaining_percentage": 5.5
				}
			}`,
			wantContain: []string{"WORKING | 990000/1048576 (", "6%", ansiRed},
		},
		{
			name: "exactly 80 percent remaining (green)",
			input: `{
				"agent_state": "tool_use",
				"context_window": {
					"total_input_tokens": 200000,
					"context_window_size": 250000,
					"remaining_percentage": 80.0
				}
			}`,
			wantContain: []string{"TOOL_USE | 200000/250000 (", "80%", ansiGreen},
		},
		{
			name: "exactly 50 percent remaining (yellow)",
			input: `{
				"agent_state": "initializing",
				"context_window": {
					"total_input_tokens": 524288,
					"context_window_size": 1048576,
					"remaining_percentage": 50.0
				}
			}`,
			wantContain: []string{"INITIALIZING | 524288/1048576 (", "50%", ansiYellow},
		},
		{
			name: "working with background tasks",
			input: `{
				"agent_state": "working",
				"context_window": {
					"total_input_tokens": 100000,
					"context_window_size": 1048576,
					"remaining_percentage": 90.0
				},
				"background_tasks": [
					{"name": "build", "status": "running", "index": 1},
					{"name": "test",  "status": "running", "index": 2}
				]
			}`,
			wantContain: []string{"WORKING | 100000/1048576 (", "90%"},
		},
		{
			name: "thinking with active subagents",
			input: `{
				"agent_state": "thinking",
				"context_window": {
					"total_input_tokens": 100000,
					"context_window_size": 1048576,
					"remaining_percentage": 90.0
				},
				"subagents": [
					{"name": "research", "role": "Researcher", "status": "working"},
					{"name": "coder",    "role": "Coder",      "status": "idle"}
				]
			}`,
			wantContain: []string{"THINKING | 100000/1048576 (", "90%"},
		},
		{
			name: "idle with all subagents idle",
			input: `{
				"agent_state": "idle",
				"context_window": {
					"total_input_tokens": 100000,
					"context_window_size": 1048576,
					"remaining_percentage": 90.0
				},
				"subagents": [
					{"name": "research", "role": "Researcher", "status": "idle"},
					{"name": "coder",    "role": "Coder",      "status": "idle"}
				]
			}`,
			wantContain: []string{"IDLE | 100000/1048576 (", "90%"},
		},
		{
			name: "idle with model display name",
			input: `{
				"agent_state": "idle",
				"context_window": {
					"total_input_tokens": 100000,
					"context_window_size": 1048576,
					"remaining_percentage": 90.0
				},
				"model": {
					"id": "gemini-1.5-pro",
					"display_name": "Gemini 1.5 Pro"
				}
			}`,
			wantContain: []string{"IDLE | 100000/1048576 (", "90%", "Gemini 1.5 Pro"},
		},
		{
			name: "idle with model id only",
			input: `{
				"agent_state": "idle",
				"context_window": {
					"total_input_tokens": 100000,
					"context_window_size": 1048576,
					"remaining_percentage": 90.0
				},
				"model": {
					"id": "gemini-1.5-flash"
				}
			}`,
			wantContain: []string{"IDLE | 100000/1048576 (", "90%", "gemini-1.5-flash"},
		},
		{
			name:    "invalid JSON",
			input:   `not json`,
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   ``,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, _, err := run([]byte(tt.input))

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			for _, want := range tt.wantContain {
				assert.Contains(t, got, want)
			}
		})
	}
}

func TestRemainingColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pct  float64
		want string
	}{
		{100, ansiGreen},
		{80, ansiGreen},
		{79.9, ansiYellow},
		{50, ansiYellow},
		{49.9, ansiRed},
		{0, ansiRed},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%.1f%%", tt.pct), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, remainingColor(tt.pct))
		})
	}
}

func TestPayloadSessionID(t *testing.T) {
	t.Parallel()
	input := `{"session_id": "test-session-123", "agent_state": "idle"}`
	var p payload
	err := json.Unmarshal([]byte(input), &p)
	require.NoError(t, err)
	assert.Equal(t, "test-session-123", p.SessionID)
}
