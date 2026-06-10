package agystatusline

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	t.Parallel()

	validInput := `{
		"agent_state": "idle",
		"context_window": {
			"total_input_tokens": 100,
			"context_window_size": 1000
		}
	}`

	tests := []struct {
		name        string
		input       string
		icon        string
		wantErr     bool
		wantContain []string
		wantExact   string
	}{
		{
			name:        "routes to renderSimple by default",
			input:       validInput,
			icon:        "",
			wantContain: []string{"IDLE | 100/1000"},
		},
		{
			name:        "routes to renderNF",
			input:       validInput,
			icon:        "nf",
			wantContain: []string{"Idle", "[", "]"},
		},
		{
			name:        "routes to renderEmoji",
			input:       validInput,
			icon:        "emoji",
			wantContain: []string{"Idle", "⬛"},
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

			got, _, err := Run([]byte(tt.input), tt.icon)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			if tt.wantExact != "" {
				assert.Equal(t, tt.wantExact, got)
			}
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
	var p Payload
	err := json.Unmarshal([]byte(input), &p)
	require.NoError(t, err)
	assert.Equal(t, "test-session-123", p.SessionID)
}

func TestFormatTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		val  int
		want string
	}{
		{0, "0"},
		{500, "500"},
		{1023, "1023"},
		{1024, "1K"},
		{1500, "1K"},
		{88244, "86K"},
		{200000, "195K"},
		{524288, "512K"},
		{1048576, "1M"},
		{1572864, "1M"},
		{2097152, "2M"},
		{-5, "-5"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.val), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, formatTokens(tt.val))
		})
	}
}

func TestRenderProgressBar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payload  Payload
		mode     iconMode
		size     int
		wantStr  string
		wantSize int
	}{
		{
			name: "nf 0 percent used",
			payload: Payload{
				ContextWindow: ContextWindow{
					UsedPercentage:      0.0,
					RemainingPercentage: 100.0,
				},
			},
			mode:     nf,
			size:     10,
			wantStr:  "[▒▒▒▒▒▒▒▒]",
			wantSize: 10,
		},
		{
			name: "nf 50 percent used",
			payload: Payload{
				ContextWindow: ContextWindow{
					UsedPercentage:      50.0,
					RemainingPercentage: 50.0,
				},
			},
			mode:     nf,
			size:     10,
			wantStr:  "[████▒▒▒▒]",
			wantSize: 10,
		},
		{
			name: "nf 100 percent used",
			payload: Payload{
				ContextWindow: ContextWindow{
					UsedPercentage:      100.0,
					RemainingPercentage: 0.0,
				},
			},
			mode:     nf,
			size:     10,
			wantStr:  "[████████]",
			wantSize: 10,
		},
		{
			name: "emoji 0 percent used (green/blue zone)",
			payload: Payload{
				ContextWindow: ContextWindow{
					UsedPercentage:      0.0,
					RemainingPercentage: 100.0,
				},
			},
			mode:     emoji,
			size:     10,
			wantStr:  "⬛⬛⬛⬛⬛",
			wantSize: 10,
		},
		{
			name: "emoji 40 percent used (blue zone, 2 blocks filled)",
			payload: Payload{
				ContextWindow: ContextWindow{
					UsedPercentage:      40.0,
					RemainingPercentage: 60.0, // rem >= 50
				},
			},
			mode:     emoji,
			size:     10,
			wantStr:  "🟨🟨⬛⬛⬛",
			wantSize: 10,
		},
		{
			name: "emoji 80 percent used (red zone, 4 blocks filled)",
			payload: Payload{
				ContextWindow: ContextWindow{
					UsedPercentage:      80.0,
					RemainingPercentage: 20.0, // rem < 50
				},
			},
			mode:     emoji,
			size:     10,
			wantStr:  "🟥🟥🟥🟥⬛",
			wantSize: 10,
		},
		{
			name: "emoji 100 percent used (red zone, 5 blocks filled)",
			payload: Payload{
				ContextWindow: ContextWindow{
					UsedPercentage:      100.0,
					RemainingPercentage: 0.0,
				},
			},
			mode:     emoji,
			size:     10,
			wantStr:  "🟥🟥🟥🟥🟥",
			wantSize: 10,
		},
		{
			name: "emoji 80 percent remaining (blue zone, 20% used)",
			payload: Payload{
				ContextWindow: ContextWindow{
					UsedPercentage:      20.0,
					RemainingPercentage: 80.0,
				},
			},
			mode:     emoji,
			size:     10,
			wantStr:  "🟦⬛⬛⬛⬛",
			wantSize: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotStr, gotSize := renderProgressBar(tt.payload, tt.mode, tt.size)
			assert.Equal(t, tt.wantStr, gotStr)
			assert.Equal(t, tt.wantSize, gotSize)
		})
	}
}
