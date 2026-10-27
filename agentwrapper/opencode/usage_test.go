package opencode

import (
	"database/sql"
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
	dbPath := filepath.Join(opencodeDir, "opencode.db")

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.Exec(`CREATE TABLE credential (
		id text PRIMARY KEY,
		integration_id text,
		label text NOT NULL,
		value text NOT NULL,
		connector_id text,
		method_id text,
		active integer,
		time_created integer NOT NULL,
		time_updated integer NOT NULL
	)`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO credential (id, integration_id, label, value, active, time_created, time_updated)
		VALUES ('cred_1', 'zai-coding-plan', 'API key', '{"type":"key","key":"test-key"}', 1, 1000, 1000)`)
	require.NoError(t, err)

	assert.Equal(t, "test-key", loadAuthToken())
}
