package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/llms/zai"
)

func TestParseLimits_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		limitsData      []zai.Limit
		detailed        bool
		wantRemaining   float64
		wantRefreshDate int64
		wantLimitsCount int
	}{
		{
			name:            "empty limits",
			limitsData:      nil,
			detailed:        true,
			wantRemaining:   1.0,
			wantRefreshDate: 0,
			wantLimitsCount: 0,
		},
		{
			name: "prioritizes TOKENS_LIMIT",
			limitsData: []zai.Limit{
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
			wantLimitsCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rem, ref, limits := zai.ParseLimits(tt.limitsData, tt.detailed)

			assert.InDelta(t, tt.wantRemaining, rem, 0.0001)
			assert.Equal(t, tt.wantRefreshDate, ref)
			assert.Len(t, limits, tt.wantLimitsCount)
		})
	}
}

func TestLoadAuthToken(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	opencodeDir := filepath.Join(tempHome, ".local", "share", "opencode")
	require.NoError(t, os.MkdirAll(opencodeDir, 0755))
	authJSON := `{"zai-coding-plan":{"key":"test-key"}}`
	require.NoError(t, os.WriteFile(filepath.Join(opencodeDir, "auth.json"), []byte(authJSON), 0600))

	assert.Equal(t, "test-key", loadAuthToken())
}
