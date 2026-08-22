package opencode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

func TestParseZaiLimits_TableDriven(t *testing.T) {
	t.Parallel()

	type limitItem struct {
		Type          string  `json:"type"`
		Usage         float64 `json:"usage"`
		Remaining     float64 `json:"remaining"`
		Percentage    float64 `json:"percentage"`
		NextResetTime int64   `json:"nextResetTime"`
	}

	tests := []struct {
		name            string
		limitsData      []limitItem
		detailed        bool
		wantRemaining   float64
		wantRefreshDate int64
		wantLimits      []types.QuotaLimit
	}{
		{
			name:            "empty limits",
			limitsData:      nil,
			detailed:        true,
			wantRemaining:   1.0,
			wantRefreshDate: 0,
			wantLimits:      nil,
		},
		{
			name: "prioritizes TOKENS_LIMIT over TIME_LIMIT",
			limitsData: []limitItem{
				{
					Type:          "TIME_LIMIT",
					Percentage:    0,
					NextResetTime: 1800000000000,
				},
				{
					Type:          "TOKENS_LIMIT",
					Percentage:    15,
					NextResetTime: 1750000000000,
				},
			},
			detailed:        true,
			wantRemaining:   0.85,
			wantRefreshDate: 1750000000,
			wantLimits: []types.QuotaLimit{
				{
					Name:        "TIME_LIMIT",
					Remaining:   1.0,
					RefreshDate: 1800000000,
				},
				{
					Name:        "TOKENS_LIMIT",
					Remaining:   0.85,
					RefreshDate: 1750000000,
				},
			},
		},
		{
			name: "multiple TOKENS_LIMIT chooses the lowest remaining",
			limitsData: []limitItem{
				{
					Type:          "TOKENS_LIMIT",
					Percentage:    20,
					NextResetTime: 1750000000000,
				},
				{
					Type:          "TOKENS_LIMIT",
					Percentage:    60,
					NextResetTime: 1760000000000,
				},
			},
			detailed:        false,
			wantRemaining:   0.40,
			wantRefreshDate: 1760000000,
			wantLimits:      nil,
		},
		{
			name: "fallback to TIME_LIMIT if no TOKENS_LIMIT",
			limitsData: []limitItem{
				{
					Type:          "TIME_LIMIT",
					Percentage:    30,
					NextResetTime: 1770000000000,
				},
			},
			detailed:        true,
			wantRemaining:   0.70,
			wantRefreshDate: 1770000000,
			wantLimits: []types.QuotaLimit{
				{
					Name:        "TIME_LIMIT",
					Remaining:   0.70,
					RefreshDate: 1770000000,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var input []struct {
				Type          string  `json:"type"`
				Usage         float64 `json:"usage"`
				Remaining     float64 `json:"remaining"`
				Percentage    float64 `json:"percentage"`
				NextResetTime int64   `json:"nextResetTime"`
			}
			for _, item := range tt.limitsData {
				input = append(input, struct {
					Type          string  `json:"type"`
					Usage         float64 `json:"usage"`
					Remaining     float64 `json:"remaining"`
					Percentage    float64 `json:"percentage"`
					NextResetTime int64   `json:"nextResetTime"`
				}{
					Type:          item.Type,
					Usage:         item.Usage,
					Remaining:     item.Remaining,
					Percentage:    item.Percentage,
					NextResetTime: item.NextResetTime,
				})
			}

			rem, ref, limits := parseZaiLimits(input, tt.detailed)

			assert.InDelta(t, tt.wantRemaining, rem, 0.0001)
			assert.Equal(t, tt.wantRefreshDate, ref)
			if tt.wantLimits == nil {
				assert.Nil(t, limits)
			} else {
				require.Len(t, limits, len(tt.wantLimits))
				for i := range tt.wantLimits {
					assert.Equal(t, tt.wantLimits[i].Name, limits[i].Name)
					assert.InDelta(t, tt.wantLimits[i].Remaining, limits[i].Remaining, 0.0001)
					assert.Equal(t, tt.wantLimits[i].RefreshDate, limits[i].RefreshDate)
				}
			}
		})
	}
}
