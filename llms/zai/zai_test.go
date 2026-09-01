package zai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/llms"
)

func TestParseLimits_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		limitsData      []Limit
		detailed        bool
		wantRemaining   float64
		wantRefreshDate int64
		wantLimits      []llms.QuotaLimit
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
			limitsData: []Limit{
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
			wantLimits: []llms.QuotaLimit{
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
			limitsData: []Limit{
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
			limitsData: []Limit{
				{
					Type:          "TIME_LIMIT",
					Percentage:    30,
					NextResetTime: 1770000000000,
				},
			},
			detailed:        true,
			wantRemaining:   0.70,
			wantRefreshDate: 1770000000,
			wantLimits: []llms.QuotaLimit{
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

			rem, ref, limits := ParseLimits(tt.limitsData, tt.detailed)

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

func TestFetchQuota_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		resp := quotaResponse{
			Success: true,
			Code:    200,
			Data: struct {
				Limits []Limit `json:"limits"`
			}{
				Limits: []Limit{
					{
						Type:          "TOKENS_LIMIT",
						Percentage:    25.0,
						NextResetTime: 1750000000000,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	rem, ref, limits, err := FetchQuota(ctx, "test-token", true, server.URL)
	require.NoError(t, err)
	assert.InDelta(t, 0.75, rem, 0.0001)
	assert.Equal(t, int64(1750000000), ref)
	require.Len(t, limits, 1)
	assert.Equal(t, "TOKENS_LIMIT", limits[0].Name)
}

func TestFetchQuota_EmptyToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, _, _, err := FetchQuota(ctx, "", false)
	assert.Error(t, err)
}
