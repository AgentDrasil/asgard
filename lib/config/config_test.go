package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: Config{
				Debug:    true,
				DB:       "sqlite",
				DSN:      "test.db",
				AgentDir: "./agents",
				Host:     "127.0.0.1",
			},
			wantErr: false,
		},
		{
			name: "invalid db",
			config: Config{
				DB:       "mysql",
				DSN:      "test.db",
				AgentDir: "./agents",
				Host:     "127.0.0.1",
			},
			wantErr: true,
			errMsg:  "invalid db: mysql",
		},
		{
			name: "missing db",
			config: Config{
				DSN:      "test.db",
				AgentDir: "./agents",
				Host:     "127.0.0.1",
			},
			wantErr: true,
			errMsg:  "invalid db: , must be 'pg' or 'sqlite'",
		},
		{
			name: "missing dsn",
			config: Config{
				DB:       "pg",
				AgentDir: "./agents",
				Host:     "127.0.0.1",
			},
			wantErr: true,
			errMsg:  "missing dsn",
		},
		{
			name: "missing agent_dir",
			config: Config{
				DB:   "sqlite",
				DSN:  "test.db",
				Host: "127.0.0.1",
			},
			wantErr: true,
			errMsg:  "missing agent_dir",
		},
		{
			name: "missing host",
			config: Config{
				DB:       "sqlite",
				DSN:      "test.db",
				AgentDir: "./agents",
			},
			wantErr: true,
			errMsg:  "missing host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.errMsg)
				return
			}
			require.NoError(t, err)
			assert.True(t, filepath.IsAbs(tt.config.AgentDir))
		})
	}
}

func TestConfig_VerifyDirs(t *testing.T) {
	t.Parallel()

	t.Run("root dir missing", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		agentDir := filepath.Join(tempDir, "non_existent")
		cfg := Config{DB: "sqlite", AgentDir: agentDir}
		err := cfg.verifyDirs()
		require.Error(t, err)
		assert.ErrorContains(t, err, "directory verification failed")
	})

	t.Run("subdirs missing", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		agentDir := filepath.Join(tempDir, "agent_root")
		require.NoError(t, os.MkdirAll(agentDir, 0755))

		cfg := Config{DB: "sqlite", AgentDir: agentDir}
		err := cfg.verifyDirs()
		require.Error(t, err)
		assert.ErrorContains(t, err, "directory verification failed")
	})

	t.Run("required dirs exist", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		agentDir := filepath.Join(tempDir, "agent_root")
		require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "agents"), 0755))

		cfg := Config{DB: "sqlite", AgentDir: agentDir}
		require.NoError(t, cfg.verifyDirs())
	})

	t.Run("path is a file not a directory", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "not_a_dir")
		require.NoError(t, os.WriteFile(filePath, []byte("test"), 0644))

		cfg := Config{DB: "sqlite", AgentDir: filePath}
		err := cfg.verifyDirs()
		require.Error(t, err)
		assert.ErrorContains(t, err, "not a directory")
	})
}
