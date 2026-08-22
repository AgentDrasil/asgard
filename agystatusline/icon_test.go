package agystatusline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		displayName string
		wantStr     string
		wantSize    int
	}{
		{
			name:        "empty display name",
			displayName: "",
			wantStr:     "Unknown",
			wantSize:    7,
		},
		{
			name:        "Gemini 3.1 Pro (High)",
			displayName: "Gemini 3.1 Pro (High)",
			wantStr:     "3.1 Pro (High)",
			wantSize:    14,
		},
		{
			name:        "Claude Sonnet 4.6 (Thinking)",
			displayName: "Claude Sonnet 4.6 (Thinking)",
			wantStr:     "Sonnet 4.6 (Thinking)",
			wantSize:    21,
		},
		{
			name:        "Claude Opus 4.6 (Thinking)",
			displayName: "Claude Opus 4.6 (Thinking)",
			wantStr:     "Opus 4.6 (Thinking)",
			wantSize:    19,
		},
		{
			name:        "GPT-OSS 120B (Medium)",
			displayName: "GPT-OSS 120B (Medium)",
			wantStr:     "120B (Medium)",
			wantSize:    13,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := Payload{
				Model: ModelInfo{
					DisplayName: tt.displayName,
				},
			}
			gotStr, gotSize := renderModel(p)
			assert.Equal(t, tt.wantStr, gotStr)
			assert.Equal(t, tt.wantSize, gotSize)
		})
	}
}

func TestRenderPercentage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payload  Payload
		wantStr  string
		wantSize int
	}{
		{
			name: "green percentage",
			payload: Payload{
				ContextWindow: ContextWindow{
					UsedPercentage:      20.0,
					RemainingPercentage: 80.0,
				},
			},
			wantStr:  ansiGreen + "20%" + ansiReset,
			wantSize: 3,
		},
		{
			name: "yellow percentage",
			payload: Payload{
				ContextWindow: ContextWindow{
					UsedPercentage:      50.0,
					RemainingPercentage: 50.0,
				},
			},
			wantStr:  ansiYellow + "50%" + ansiReset,
			wantSize: 3,
		},
		{
			name: "red percentage",
			payload: Payload{
				ContextWindow: ContextWindow{
					UsedPercentage:      90.0,
					RemainingPercentage: 10.0,
				},
			},
			wantStr:  ansiRed + "90%" + ansiReset,
			wantSize: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotStr, gotSize := renderPercentage(tt.payload)
			assert.Equal(t, tt.wantStr, gotStr)
			assert.Equal(t, tt.wantSize, gotSize)
		})
	}
}

func TestRenderUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payload  Payload
		wantStr  string
		wantSize int
	}{
		{
			name: "small values",
			payload: Payload{
				ContextWindow: ContextWindow{
					TotalInputTokens:  100,
					ContextWindowSize: 1000,
				},
			},
			wantStr:  "(100/1000)",
			wantSize: 10,
		},
		{
			name: "K notation",
			payload: Payload{
				ContextWindow: ContextWindow{
					TotalInputTokens:  88244,
					ContextWindowSize: 200000,
				},
			},
			wantStr:  "(86K/195K)",
			wantSize: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotStr, gotSize := renderUsage(tt.payload)
			assert.Equal(t, tt.wantStr, gotStr)
			assert.Equal(t, tt.wantSize, gotSize)
		})
	}
}

func TestRenderTaskAndSubAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payload  Payload
		mode     iconMode
		wantStr  string
		wantSize int
	}{
		{
			name: "no tasks and no subagents",
			payload: Payload{
				TaskCount: 0,
				Subagents: []Subagent{},
			},
			mode:     simple,
			wantStr:  "",
			wantSize: 0,
		},
		{
			name: "only tasks - simple",
			payload: Payload{
				TaskCount: 3,
				Subagents: []Subagent{},
			},
			mode:     simple,
			wantStr:  "T: 3",
			wantSize: 4,
		},
		{
			name: "only subagents active - simple",
			payload: Payload{
				TaskCount: 0,
				Subagents: []Subagent{
					{Name: "agent1", Status: "working"},
					{Name: "agent2", Status: "idle"},
					{Name: "agent3", Status: "tool_use"},
				},
			},
			mode:     simple,
			wantStr:  "A: 2",
			wantSize: 4,
		},
		{
			name: "both tasks and active subagents - simple",
			payload: Payload{
				TaskCount: 2,
				Subagents: []Subagent{
					{Name: "agent1", Status: "thinking"},
				},
			},
			mode:     simple,
			wantStr:  "T: 2•A: 1",
			wantSize: 9,
		},
		{
			name: "only tasks - nf",
			payload: Payload{
				TaskCount: 3,
				Subagents: []Subagent{},
			},
			mode:     nf,
			wantStr:  "  3",
			wantSize: 4,
		},
		{
			name: "only subagents active - nf",
			payload: Payload{
				TaskCount: 0,
				Subagents: []Subagent{
					{Name: "agent1", Status: "working"},
					{Name: "agent2", Status: "idle"},
					{Name: "agent3", Status: "tool_use"},
				},
			},
			mode:     nf,
			wantStr:  "  2",
			wantSize: 4,
		},
		{
			name: "both tasks and active subagents - nf",
			payload: Payload{
				TaskCount: 2,
				Subagents: []Subagent{
					{Name: "agent1", Status: "thinking"},
				},
			},
			mode:     nf,
			wantStr:  "  2•  1",
			wantSize: 9,
		},
		{
			name: "only tasks - emoji",
			payload: Payload{
				TaskCount: 3,
				Subagents: []Subagent{},
			},
			mode:     emoji,
			wantStr:  "📋3",
			wantSize: 3,
		},
		{
			name: "only subagents active - emoji",
			payload: Payload{
				TaskCount: 0,
				Subagents: []Subagent{
					{Name: "agent1", Status: "working"},
					{Name: "agent2", Status: "idle"},
					{Name: "agent3", Status: "tool_use"},
				},
			},
			mode:     emoji,
			wantStr:  "🕵2",
			wantSize: 3,
		},
		{
			name: "both tasks and active subagents - emoji",
			payload: Payload{
				TaskCount: 2,
				Subagents: []Subagent{
					{Name: "agent1", Status: "thinking"},
				},
			},
			mode:     emoji,
			wantStr:  "📋2•🕵1",
			wantSize: 7,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotStr, gotSize := renderTaskAndSubAgent(tt.payload, tt.mode)
			assert.Equal(t, tt.wantStr, gotStr)
			assert.Equal(t, tt.wantSize, gotSize)
		})
	}
}

func TestRenderIcon(t *testing.T) {
	t.Parallel()

	// Prepare a typical payload
	p := Payload{
		AgentState: "working", // "Gen" (size 3) + " 󰜎" (size 2) = 5
		ContextWindow: ContextWindow{
			TotalInputTokens:    100000,
			ContextWindowSize:   1000000,
			RemainingPercentage: 90.0,
			UsedPercentage:      10.0,
		},
		Model: ModelInfo{
			ID:          "gemini-1.5-pro",
			DisplayName: "Gemini 1.5 Pro", // renderModel returns "1.5 Pro" (size 7)
		},
		TaskCount: 2,
		Subagents: []Subagent{
			{Name: "sub", Status: "working"}, // active subagent
		},
	}

	// 1. Multiline Mode: TerminalWidth < 40 (e.g. 35)
	t.Run("multiline mode - fits usage", func(t *testing.T) {
		payload := p
		payload.TerminalWidth = 35
		// state: "󰜎 Gen" (size 5)
		// pct: "10%" (size 3)
		// usage: "(97K/976K)" (size 10)
		// line 1: "󰜎 Gen" + " " + "10%" + " " + "(97K/976K)" -> size 5 + 1 + 3 + 1 + 10 = 20 <= 35
		// model: "1.5 Pro" (size 7)
		// taskSub: "  2•  1" (size 9)
		// line 2: "1.5 Pro" + " " + "  2•  1" -> size 7 + 1 + 9 = 17 <= 35
		got := renderIcon(payload, nf)
		lines := strings.Split(got, "\n")
		assert.Len(t, lines, 2)
		assert.Contains(t, lines[0], "Gen")
		assert.Contains(t, lines[0], "10%")
		assert.Contains(t, lines[0], "(97K/976K)")
		assert.Contains(t, lines[1], colorPrint(ansiBrightGray, "1.5 Pro")+" "+colorPrint(ansiBrightGray, "  2•  1"))
	})

	t.Run("multiline mode - omit usage", func(t *testing.T) {
		payload := p
		payload.TerminalWidth = 15 // extremely narrow
		got := renderIcon(payload, nf)
		lines := strings.Split(got, "\n")
		// Line 1 should only be state + percentage: "󰜎 Gen 10%" (size 9 <= 15)
		assert.Contains(t, lines[0], "Gen")
		assert.Contains(t, lines[0], "10%")
		assert.NotContains(t, lines[0], "(97K/976K)")
		// Line 2 & 3: model (size 7), taskSub (size 9) split since 7 + 1 + 9 = 17 > 15
		assert.Len(t, lines, 3)
		assert.Equal(t, colorPrint(ansiBrightGray, "1.5 Pro"), lines[1])
		assert.Equal(t, colorPrint(ansiBrightGray, "  2•  1"), lines[2])
	})

	// 2. Single line mode: large width (e.g. 80)
	t.Run("single line - fully fitted", func(t *testing.T) {
		payload := p
		payload.TerminalWidth = 80
		got := renderIcon(payload, nf)
		assert.NotContains(t, got, "\n")
		assert.Contains(t, got, "Gen")
		assert.Contains(t, got, "10%")
		assert.Contains(t, got, "(97K/976K)")
		assert.Contains(t, got, colorPrint(ansiBrightGray, "1.5 Pro"))
		assert.Contains(t, got, colorPrint(ansiBrightGray, "  2•  1"))
		// should contain progress bar brackets
		assert.Contains(t, got, "[")
		assert.Contains(t, got, "]")
	})

	// 3. Single line mode: taskSub pushed to line 2 (align right)
	t.Run("single line - taskSub pushed to second line", func(t *testing.T) {
		payload := p
		// Let's set a width where taskSub does not fit on line 1, but fits if pushed to line 2.
		// stateColWidth = 11.
		// pct = 3. usage = 10. model = 7. taskSub = 9.
		// With taskSub on line 1: neededWithoutPB = 11 + 3 + 11 + 1 + (7 + 1 + 9) = 43.
		// Without taskSub on line 1: neededWithoutPB = 11 + 3 + 11 + 1 + 7 = 33.
		// Let's set W = 40.
		// 43 > 40, so Option 1/2/5/6 won't fit on line 1.
		// Option 3 (taskSub on line 2, has usage, has progressbar):
		// neededWithoutPB = 33. leftover = 40 - 33 = 7. maxPBLen = 7 - 1 = 6.
		// pbLen = 6 >= 5. So it fits!
		payload.TerminalWidth = 40
		got := renderIcon(payload, nf)
		lines := strings.Split(got, "\n")
		assert.Len(t, lines, 2)
		// Line 1 has model, but no taskSub
		assert.Contains(t, lines[0], "1.5 Pro")
		assert.NotContains(t, lines[0], "")
		// Line 2 has taskSub aligned right (spaces = 40 - 9 = 31)
		assert.Equal(t, strings.Repeat(" ", 31)+colorPrint(ansiBrightGray, "  2•  1"), lines[1])
	})
}
