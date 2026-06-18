package agy

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

// usageOutput mirrors the real /usage scrollback, using actual Unicode
// block-drawing characters so the bar-detection regex fires correctly.
const usageOutput = `
  Gemini 3.5 Flash (Medium)
  ███████████ ███████████ ███████████ ███████████ ███████████ 100%
  Quota available

  Gemini 3.5 Flash (High)
  ███████████ ███████████ ███████████ ███████████ ███████████ 100%
  Quota available

  Gemini 3.5 Flash (Low)
  ███████████ ███████████ ███████████ ███████████ ███████████ 100%
  Quota available

  Gemini 3.1 Pro (Low)
  ███████████ ███████████ ███████████ ███████████ ███████████ 100%
  Quota available

  Gemini 3.1 Pro (High)
  ███████████ ███████████ ███████████ ███████████ ███████████ 100%
  Quota available

  Claude Sonnet 4.6 (Thinking)
  ███████████ ███████████ ███████████ ███████████ ░░░░░░░░░░░ 80%
  80% remaining · Refreshes in 1h 23m

  Claude Opus 4.6 (Thinking)
  ███████████ ███████████ ███████████ ███████████ ░░░░░░░░░░░ 80%
  80% remaining · Refreshes in 1h 23m

  GPT-OSS 120B (Medium)
  ███████████ ███████████ ███████████ ███████████ ░░░░░░░░░░░ 80%
  80% remaining · Refreshes in 1h 23m
`

// toLines splits a raw string into lines for ParseUsage.
func toLines(s string) []string {
	return strings.Split(s, "\n")
}

// makeBlock builds a minimal scrollback string for a single model entry.
func makeBlock(t *testing.T, model string, pct int, status string) []string {
	t.Helper()
	raw := fmt.Sprintf("\n  %s\n  ███░░░ %d%%\n  %s\n", model, pct, status)
	return toLines(raw)
}

func TestParseUsage_FullSample(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	entries, err := parseUsage(toLines(usageOutput), baseTime)
	require.NoError(t, err)
	require.Len(t, entries, 8)

	// Spot-check a fully-available model.
	assert.Equal(t, "Gemini 3.5 Flash (Medium)", entries[0].Model)
	assert.InDelta(t, 1.0, entries[0].Remaining, 1e-9)
	assert.Zero(t, entries[0].RefreshDate)

	// Spot-check a partially-used model.
	assert.Equal(t, "Claude Sonnet 4.6 (Thinking)", entries[5].Model)
	assert.InDelta(t, 0.8, entries[5].Remaining, 1e-9)
	assert.Equal(t, baseTime.Add(1*time.Hour+23*time.Minute).Unix(), entries[5].RefreshDate)
}

func TestParseUsage_TableDriven(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		input       string
		wantLen     int
		wantEntries []types.ModelUsage
	}{
		{
			name: "single fully available model",
			input: `
  My Model A
  ███████████ 100%
  Quota available
`,
			wantLen: 1,
			wantEntries: []types.ModelUsage{
				{Model: "My Model A", Remaining: 1.0},
			},
		},
		{
			name: "single partially used model",
			input: `
  My Model B
  ███████████ ░░░░░░░░░░░ 80%
  80% remaining · Refreshes in 2h 5m
`,
			wantLen: 1,
			wantEntries: []types.ModelUsage{
				{Model: "My Model B", Remaining: 0.8, RefreshDate: baseTime.Add(2*time.Hour + 5*time.Minute).Unix()},
			},
		},
		{
			name: "zero percent remaining",
			input: `
  Exhausted Model
  ░░░░░░░░░░░ 0%
  0% remaining · Refreshes in 59m
`,
			wantLen: 1,
			wantEntries: []types.ModelUsage{
				{Model: "Exhausted Model", Remaining: 0.0, RefreshDate: baseTime.Add(59 * time.Minute).Unix()},
			},
		},
		{
			name: "mixed models",
			input: `
  Full Model
  ███████████ 100%
  Quota available

  Half Model
  ██████░░░░░ 50%
  50% remaining · Refreshes in 30m
`,
			wantLen: 2,
			wantEntries: []types.ModelUsage{
				{Model: "Full Model", Remaining: 1.0},
				{Model: "Half Model", Remaining: 0.5, RefreshDate: baseTime.Add(30 * time.Minute).Unix()},
			},
		},
		{
			name:    "empty input",
			input:   "",
			wantLen: 0,
		},
		{
			name: "non-usage blocks are skipped",
			input: `
  Some header text
  without any bar characters

  Model With Bar
  ███ 100%
  Quota available
`,
			wantLen: 1,
			wantEntries: []types.ModelUsage{
				{Model: "Model With Bar", Remaining: 1.0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			entries, err := parseUsage(toLines(tt.input), baseTime)
			require.NoError(t, err)
			require.Len(t, entries, tt.wantLen)

			for i, want := range tt.wantEntries {
				assert.Equal(t, want.Model, entries[i].Model, "entries[%d].Model", i)
				assert.InDelta(t, want.Remaining, entries[i].Remaining, 1e-9, "entries[%d].Remaining", i)
				assert.Equal(t, want.RefreshDate, entries[i].RefreshDate, "entries[%d].RefreshDate", i)
			}
		})
	}
}

func TestParseUsage_RemainingFraction(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		pct      int
		expected float64
	}{
		{100, 1.0},
		{80, 0.8},
		{50, 0.5},
		{25, 0.25},
		{0, 0.0},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d%%", tt.pct), func(t *testing.T) {
			t.Parallel()

			lines := makeBlock(t, "Model X", tt.pct, "Quota available")
			entries, err := parseUsage(lines, baseTime)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.InDelta(t, tt.expected, entries[0].Remaining, 1e-9)
		})
	}
}

func TestGetModelQuota(t *testing.T) {
	t.Parallel()

	quota := map[string]QuotaEntry{
		"gemini-5h": {
			RemainingFraction: 0.9,
			ResetTime:         "2026-06-13T21:00:00Z",
			ResetInSeconds:    3600,
		},
		"gemini-weekly": {
			RemainingFraction: 0.95,
			ResetTime:         "2026-06-20T21:00:00Z",
			ResetInSeconds:    608400,
		},
		"3p-5h": {
			RemainingFraction: 0.8,
			ResetTime:         "2026-06-13T22:00:00Z",
			ResetInSeconds:    7200,
		},
		"3p-weekly": {
			RemainingFraction: 0.7,
			ResetTime:         "2026-06-20T22:00:00Z",
			ResetInSeconds:    612000,
		},
	}

	tests := []struct {
		modelName     string
		wantRemaining float64
		wantReset     int64
	}{
		{
			modelName:     "Gemini 3.5 Flash (Low)",
			wantRemaining: 0.9,
			wantReset:     parseResetTime("2026-06-13T21:00:00Z"),
		},
		{
			modelName:     "Claude Sonnet 4.6 (Thinking)",
			wantRemaining: 0.7,
			wantReset:     parseResetTime("2026-06-20T22:00:00Z"),
		},
		{
			modelName:     "GPT-OSS 120B (Medium)",
			wantRemaining: 0.7,
			wantReset:     parseResetTime("2026-06-20T22:00:00Z"),
		},
		{
			modelName:     "Unknown Model",
			wantRemaining: 0.7,
			wantReset:     parseResetTime("2026-06-20T22:00:00Z"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.modelName, func(t *testing.T) {
			rem, ref := getModelQuota(tt.modelName, quota)
			assert.InDelta(t, tt.wantRemaining, rem, 1e-9)
			assert.Equal(t, tt.wantReset, ref)
		})
	}
}
