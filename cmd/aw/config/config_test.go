package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_IsModelAllowed(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *Config
		agent     string
		model     string
		wantAllow bool
	}{
		{
			name:      "nil config",
			cfg:       nil,
			agent:     "agy",
			model:     "Gemini 3.5 Flash",
			wantAllow: true,
		},
		{
			name: "agent not found",
			cfg: &Config{
				Agents: []map[string][]string{
					{"other-agent": {"Claude.*"}},
				},
			},
			agent:     "agy",
			model:     "Gemini 3.5 Flash",
			wantAllow: true,
		},
		{
			name: "model matches pattern",
			cfg: &Config{
				Agents: []map[string][]string{
					{"agy": {"Gemini.*", "Claude.*"}},
				},
			},
			agent:     "agy",
			model:     "Gemini 3.5 Flash (Medium)",
			wantAllow: true,
		},
		{
			name: "model does not match pattern",
			cfg: &Config{
				Agents: []map[string][]string{
					{"agy": {"Gemini.*", "Claude.*"}},
				},
			},
			agent:     "agy",
			model:     "GPT-4o",
			wantAllow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.IsModelAllowed(tt.agent, tt.model)
			assert.Equal(t, tt.wantAllow, got)
		})
	}
}

func TestLoadConfig_NonExistent(t *testing.T) {
	// Set XDG_CONFIG_HOME to a non-existent path
	t.Setenv("XDG_CONFIG_HOME", "/nonexistent-path-12345")
	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestLoadConfig_Valid(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	err := os.MkdirAll(filepath.Join(tmpDir, "aw"), 0755)
	require.NoError(t, err)

	content := `
agents:
  - agy:
    - Gemini.*
    - Claude.*
`
	err = os.WriteFile(filepath.Join(tmpDir, "aw", "config.yaml"), []byte(content), 0644)
	require.NoError(t, err)

	cfg, err := LoadConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, cfg.IsModelAllowed("agy", "Gemini 3.5 Flash"))
	assert.False(t, cfg.IsModelAllowed("agy", "GPT-4"))
}
