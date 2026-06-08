package roles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoader_LoadAll(t *testing.T) {
	t.Parallel()

	t.Run("successfully load agents", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		agentsDir := filepath.Join(tmpDir, "agents")
		err := os.Mkdir(agentsDir, 0755)
		require.NoError(t, err)

		tests := []struct {
			name     string
			config   string
			expected AgentConfig
		}{
			{
				name: "agent1",
				config: `
name: agent1
description: Test Agent 1
cli: gemini-cli
args: ["--test"]
run_dirs: ["/tmp/run"]
allow_dirs: ["/tmp/allow"]
`,
				expected: AgentConfig{
					Name:        "agent1",
					Description: "Test Agent 1",
					CLI:         "gemini-cli",
					Args:        []string{"--test"},
					RunDirs:     []string{"/tmp/run"},
					AllowDirs:   []string{"/tmp/allow"},
				},
			},
			{
				name: "agent2",
				config: `
name: agent2
description: Test Agent 2
cli: another-cli
`,
				expected: AgentConfig{
					Name:        "agent2",
					Description: "Test Agent 2",
					CLI:         "another-cli",
				},
			},
		}

		for _, tt := range tests {
			agentPath := filepath.Join(agentsDir, tt.name)
			err = os.Mkdir(agentPath, 0755)
			require.NoError(t, err)

			err = os.WriteFile(filepath.Join(agentPath, "config.yaml"), []byte(tt.config), 0644)
			require.NoError(t, err)
		}

		loader := NewLoader(tmpDir)
		agents, err := loader.LoadAll()

		require.NoError(t, err)
		assert.Len(t, agents, len(tests))

		for _, tt := range tests {
			var found *Agent
			for _, a := range agents {
				if a.Config.Name == tt.name {
					found = a
					break
				}
			}
			require.NotNil(t, found, "agent %s should be found", tt.name)
			assert.Equal(t, tt.expected, found.Config)
		}
	})

	t.Run("skip non-directory entries", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		agentsDir := filepath.Join(tmpDir, "agents")
		err := os.Mkdir(agentsDir, 0755)
		require.NoError(t, err)

		err = os.WriteFile(filepath.Join(agentsDir, "not-a-dir"), []byte("data"), 0644)
		require.NoError(t, err)

		loader := NewLoader(tmpDir)
		agents, err := loader.LoadAll()

		require.NoError(t, err)
		assert.Empty(t, agents)
	})

	t.Run("skip directories without config.yaml", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		agentsDir := filepath.Join(tmpDir, "agents")
		err := os.Mkdir(agentsDir, 0755)
		require.NoError(t, err)

		err = os.Mkdir(filepath.Join(agentsDir, "no-config"), 0755)
		require.NoError(t, err)

		loader := NewLoader(tmpDir)
		agents, err := loader.LoadAll()

		require.NoError(t, err)
		assert.Empty(t, agents)
	})
}
