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
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AW_CONFIG", "")
	t.Setenv("OPENCODE_AW_CONFIG", "")
	t.Setenv("ZAI_API_KEY", "")
	t.Setenv("ZHIPU_API_KEY", "")
	t.Setenv("OPENCODE_ZAI_API_KEY", "")

	t.Run("returns token from opencode.aw.yaml ZAI_API_KEY", func(t *testing.T) {
		cfgPath := filepath.Join(tempHome, "opencode-zai.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte("env:\n  ZAI_API_KEY: yaml-zai-key\n"), 0o600))
		t.Setenv("OPENCODE_AW_CONFIG", cfgPath)

		assert.Equal(t, "yaml-zai-key", loadAuthToken())
	})

	t.Run("returns token from opencode.aw.yaml ZHIPU_API_KEY", func(t *testing.T) {
		cfgPath := filepath.Join(tempHome, "opencode-zhipu.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte("ZHIPU_API_KEY: yaml-zhipu-key\n"), 0o600))
		t.Setenv("OPENCODE_AW_CONFIG", cfgPath)

		assert.Equal(t, "yaml-zhipu-key", loadAuthToken())
	})

	t.Run("returns token from opencode.aw.yaml OPENCODE_ZAI_API_KEY", func(t *testing.T) {
		cfgPath := filepath.Join(tempHome, "opencode-opencode-zai.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte("OPENCODE_ZAI_API_KEY: yaml-opencode-zai-key\n"), 0o600))
		t.Setenv("OPENCODE_AW_CONFIG", cfgPath)

		assert.Equal(t, "yaml-opencode-zai-key", loadAuthToken())
	})

	t.Run("opencode.aw.yaml takes precedence over os.Getenv", func(t *testing.T) {
		cfgPath := filepath.Join(tempHome, "opencode-precedence.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte("env:\n  ZAI_API_KEY: yaml-priority-key\n"), 0o600))
		t.Setenv("OPENCODE_AW_CONFIG", cfgPath)
		t.Setenv("ZAI_API_KEY", "env-lower-priority-key")
		t.Setenv("ZHIPU_API_KEY", "env-other-key")

		assert.Equal(t, "yaml-priority-key", loadAuthToken())
	})

	t.Run("returns token from os.Getenv when yaml does not specify it", func(t *testing.T) {
		t.Setenv("OPENCODE_AW_CONFIG", filepath.Join(tempHome, "empty.yaml"))
		require.NoError(t, os.WriteFile(filepath.Join(tempHome, "empty.yaml"), []byte("env: {}\n"), 0o600))

		t.Setenv("ZHIPU_API_KEY", "env-zhipu-key")
		assert.Equal(t, "env-zhipu-key", loadAuthToken())
	})

	t.Run("falls back to opencode.db when yaml and env are absent", func(t *testing.T) {
		t.Setenv("OPENCODE_AW_CONFIG", "")
		t.Setenv("ZAI_API_KEY", "")
		t.Setenv("ZHIPU_API_KEY", "")
		t.Setenv("OPENCODE_ZAI_API_KEY", "")

		opencodeDir := filepath.Join(tempHome, ".local", "share", "opencode")
		require.NoError(t, os.MkdirAll(opencodeDir, 0o755))
		dbPath := filepath.Join(opencodeDir, "opencode.db")

		db, err := sql.Open("sqlite", dbPath)
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })

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
			VALUES ('cred_1', 'zai-coding-plan', 'API key', '{"type":"key","key":"db-test-key"}', 1, 1000, 1000)`)
		require.NoError(t, err)

		assert.Equal(t, "db-test-key", loadAuthToken())
	})

	t.Run("returns empty when db is absent and no config provided", func(t *testing.T) {
		emptyHome := filepath.Join(tempHome, "empty-home")
		require.NoError(t, os.MkdirAll(emptyHome, 0o755))
		t.Setenv("HOME", emptyHome)
		t.Setenv("OPENCODE_AW_CONFIG", "")
		t.Setenv("ZAI_API_KEY", "")
		t.Setenv("ZHIPU_API_KEY", "")
		t.Setenv("OPENCODE_ZAI_API_KEY", "")

		assert.Empty(t, loadAuthToken())
	})
}
