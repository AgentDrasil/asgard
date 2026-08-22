package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/agentwrapper/types"
)

type mockClient struct {
	models []string
}

func (m *mockClient) Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	var usages []types.ModelUsage
	for _, model := range m.models {
		usages = append(usages, types.ModelUsage{Model: model, Remaining: 1.0})
	}
	return usages, nil
}

func (m *mockClient) Models(ctx context.Context, opts types.UsageOptions) ([]string, error) {
	return m.models, nil
}

func (m *mockClient) Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	return &types.PromptResult{}, nil
}

func TestLoader_LoadAll(t *testing.T) {
	// Setup mock clients to make tests independent of installed CLIs
	mockClients := map[string]types.CLIClient{
		"agy":      &mockClient{models: []string{"gemini-2.5-flash", "Gemini 3.5 Flash (Low)"}},
		"opencode": &mockClient{models: []string{"deepseek-chat"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	t.Run("successfully load agents", func(t *testing.T) {
		tmpDir := t.TempDir()
		agentsDir := filepath.Join(tmpDir, "agents")
		err := os.Mkdir(agentsDir, 0755)
		require.NoError(t, err)

		// Write teams.yaml
		teamsYaml := `
teams:
  - team-a
  - team-b
`
		err = os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte(teamsYaml), 0644)
		require.NoError(t, err)

		tests := []struct {
			name     string
			config   string
			expected AgentConfig
		}{
			{
				name: "agent1",
				config: `
id: agent1
name: agent1
description: Test Agent 1
team: team-a
cli:
  - cli: agy
    model: gemini-2.5-flash
args: ["--test"]
run_dirs: ["/tmp/run"]
mount_dirs:
  readonly: ["/tmp/allow"]
  readwrite: ["/tmp/rw"]
`,
				expected: AgentConfig{
					ID:          "agent1",
					Name:        "agent1",
					Description: "Test Agent 1",
					Icon:        "fluent-color:bot-24",
					Team:        "team-a",
					MainAgent:   boolPtr(true),
					CLI: []CLITarget{
						{CLI: "agy", Model: "gemini-2.5-flash"},
					},
					RunDirs: []string{"/tmp/run"},
					MountDirs: MountConfig{
						ReadOnly:  []string{"/tmp/allow"},
						ReadWrite: []string{"/tmp/rw"},
					},
				},
			},
			{
				name: "agent2",
				config: `
id: agent2
name: agent2
description: Test Agent 2
team: team-b
main_agent: false
cli:
  - cli: opencode
    model: deepseek-chat
`,
				expected: AgentConfig{
					ID:          "agent2",
					Name:        "agent2",
					Description: "Test Agent 2",
					Icon:        "fluent-color:bot-24",
					Team:        "team-b",
					MainAgent:   boolPtr(false),
					CLI: []CLITarget{
						{CLI: "opencode", Model: "deepseek-chat"},
					},
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

		// Write teams.yaml
		err = os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - team-a"), 0644)
		require.NoError(t, err)

		err = os.WriteFile(filepath.Join(agentsDir, "not-a-dir"), []byte("data"), 0644)
		require.NoError(t, err)

		loader := NewLoader(tmpDir)
		agents, err := loader.LoadAll()

		require.NoError(t, err)
		assert.Len(t, agents, 0)
	})

	t.Run("skip directories without config.yaml", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		agentsDir := filepath.Join(tmpDir, "agents")
		err := os.Mkdir(agentsDir, 0755)
		require.NoError(t, err)

		// Write teams.yaml
		err = os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - team-a"), 0644)
		require.NoError(t, err)

		err = os.Mkdir(filepath.Join(agentsDir, "no-config"), 0755)
		require.NoError(t, err)

		loader := NewLoader(tmpDir)
		agents, err := loader.LoadAll()

		require.NoError(t, err)
		assert.Len(t, agents, 0)
	})

	t.Run("returns empty slice when directory does not exist", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()

		loader := NewLoader(tmpDir)
		agents, err := loader.LoadAll()

		require.NoError(t, err)
		assert.Empty(t, agents)

		configPath := filepath.Join(tmpDir, "agents", "agentfather", "config.yaml")
		assert.NoFileExists(t, configPath)
	})

	t.Run("missing teams.yaml should fail", func(t *testing.T) {
		tmpDir := t.TempDir()
		agentsDir := filepath.Join(tmpDir, "agents")
		err := os.Mkdir(agentsDir, 0755)
		require.NoError(t, err)

		// Create an agent config
		agentPath := filepath.Join(agentsDir, "agent1")
		err = os.Mkdir(agentPath, 0755)
		require.NoError(t, err)

		config := `
id: agent1
name: agent1
description: Test Agent 1
cli:
  - cli: agy
    model: gemini-2.5-flash
`
		err = os.WriteFile(filepath.Join(agentPath, "config.yaml"), []byte(config), 0644)
		require.NoError(t, err)

		loader := NewLoader(tmpDir)
		_, err = loader.LoadAll()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read teams.yaml")
	})

	t.Run("invalid team in agent config should fail", func(t *testing.T) {
		tmpDir := t.TempDir()
		agentsDir := filepath.Join(tmpDir, "agents")
		err := os.Mkdir(agentsDir, 0755)
		require.NoError(t, err)

		// Write teams.yaml without team-b
		teamsYaml := `
teams:
  - team-a
`
		err = os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte(teamsYaml), 0644)
		require.NoError(t, err)

		agentPath := filepath.Join(agentsDir, "agent1")
		err = os.Mkdir(agentPath, 0755)
		require.NoError(t, err)

		config := `
id: agent1
name: agent1
description: Test Agent 1
team: team-b
run_dirs: ["/tmp"]
cli:
  - cli: agy
    model: gemini-2.5-flash
`
		err = os.WriteFile(filepath.Join(agentPath, "config.yaml"), []byte(config), 0644)
		require.NoError(t, err)

		loader := NewLoader(tmpDir)
		_, err = loader.LoadAll()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), `team "team-b" for agent agent1 is not defined in teams.yaml`)
	})

	t.Run("empty team in agent config should succeed", func(t *testing.T) {
		tmpDir := t.TempDir()
		agentsDir := filepath.Join(tmpDir, "agents")
		err := os.Mkdir(agentsDir, 0755)
		require.NoError(t, err)

		// Write teams.yaml
		teamsYaml := `
teams:
  - team-a
`
		err = os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte(teamsYaml), 0644)
		require.NoError(t, err)

		agentPath := filepath.Join(agentsDir, "agent1")
		err = os.Mkdir(agentPath, 0755)
		require.NoError(t, err)

		config := `
id: agent1
name: agent1
description: Test Agent 1
run_dirs: ["/tmp"]
cli:
  - cli: agy
    model: gemini-2.5-flash
`
		err = os.WriteFile(filepath.Join(agentPath, "config.yaml"), []byte(config), 0644)
		require.NoError(t, err)

		loader := NewLoader(tmpDir)
		agents, err := loader.LoadAll()
		assert.NoError(t, err)
		assert.Len(t, agents, 1)
		assert.Equal(t, "", agents[0].Config.Team)
	})

	t.Run("mismatched agent ID and directory name should fail", func(t *testing.T) {
		tmpDir := t.TempDir()
		agentsDir := filepath.Join(tmpDir, "agents")
		err := os.Mkdir(agentsDir, 0755)
		require.NoError(t, err)

		// Write teams.yaml
		teamsYaml := `
teams:
  - team-a
`
		err = os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte(teamsYaml), 0644)
		require.NoError(t, err)

		agentPath := filepath.Join(agentsDir, "agent1")
		err = os.Mkdir(agentPath, 0755)
		require.NoError(t, err)

		config := `
id: mismatched-id
name: agent1
description: Test Agent 1
run_dirs: ["/tmp"]
cli:
  - cli: agy
    model: gemini-2.5-flash
`
		err = os.WriteFile(filepath.Join(agentPath, "config.yaml"), []byte(config), 0644)
		require.NoError(t, err)

		loader := NewLoader(tmpDir)
		_, err = loader.LoadAll()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), `agent ID "mismatched-id" does not match directory name "agent1"`)
	})
}

func TestAgentConfig_Validate(t *testing.T) {
	// Setup mock clients to make tests independent of installed CLIs
	mockClients := map[string]types.CLIClient{
		"agy":      &mockClient{models: []string{"gemini-2.5-flash"}},
		"opencode": &mockClient{models: []string{"deepseek-chat", "zai-coding-plan/glm-5.3"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tests := []struct {
		name    string
		config  AgentConfig
		wantErr bool
	}{
		{
			name: "valid configuration with run_dirs",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: false,
		},
		{
			name: "main_agent true without run_dirs",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				MainAgent:   boolPtr(true),
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
			},
			wantErr: true,
		},
		{
			name: "main_agent false without run_dirs",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				MainAgent:   boolPtr(false),
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
			},
			wantErr: false,
		},
		{
			name: "empty id",
			config: AgentConfig{
				ID:          "",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "uppercase id format",
			config: AgentConfig{
				ID:          "Agent-One",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "empty name",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "empty description",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "",
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "empty cli list",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI:         []CLITarget{},
				RunDirs:     []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "empty cli target name",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "unsupported cli agent",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "unsupported-cli", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "empty model",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "agy", Model: ""},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "relative run directory",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"relative/path"},
			},
			wantErr: true,
		},
		{
			name: "relative mount readonly directory",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
				MountDirs: MountConfig{
					ReadOnly: []string{"relative/path"},
				},
			},
			wantErr: true,
		},
		{
			name: "relative mount readwrite directory",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
				MountDirs: MountConfig{
					ReadWrite: []string{"relative/path"},
				},
			},
			wantErr: true,
		},
		{
			name: "valid agent type",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				Type:        "agent",
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: false,
		},
		{
			name: "valid opencode model without variant",
			config: AgentConfig{
				ID:          "agent-opencode-1",
				Name:        "agent-opencode",
				Description: "Test Agent Opencode",
				CLI: []CLITarget{
					{CLI: "opencode", Model: "deepseek-chat"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: false,
		},
		{
			name: "valid opencode model with variant",
			config: AgentConfig{
				ID:          "agent-opencode-2",
				Name:        "agent-opencode",
				Description: "Test Agent Opencode",
				CLI: []CLITarget{
					{CLI: "opencode", Model: "deepseek-chat/low"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: false,
		},
		{
			name: "valid opencode model with provider slash in base model",
			config: AgentConfig{
				ID:          "agent-opencode-base",
				Name:        "agent-opencode",
				Description: "Test Agent Opencode",
				CLI: []CLITarget{
					{CLI: "opencode", Model: "zai-coding-plan/glm-5.3"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: false,
		},
		{
			name: "valid opencode model with variant matching base",
			config: AgentConfig{
				ID:          "agent-opencode-4",
				Name:        "agent-opencode",
				Description: "Test Agent Opencode",
				CLI: []CLITarget{
					{CLI: "opencode", Model: "zai-coding-plan/glm-5.3/high"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: false,
		},
		{
			name: "invalid opencode model with variant",
			config: AgentConfig{
				ID:          "agent-opencode-5",
				Name:        "agent-opencode",
				Description: "Test Agent Opencode",
				CLI: []CLITarget{
					{CLI: "opencode", Model: "unsupported-provider/model/high"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "invalid opencode model",
			config: AgentConfig{
				ID:          "agent-opencode-3",
				Name:        "agent-opencode",
				Description: "Test Agent Opencode",
				CLI: []CLITarget{
					{CLI: "opencode", Model: "unknown-model"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "invalid agent type",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				Type:        "invalid_type",
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestAgentConfig_ValidateWithCLIs(t *testing.T) {
	t.Parallel()

	supportedCLIs := map[string][]string{
		"agy":      {"gemini-2.5-flash"},
		"opencode": {"deepseek-chat", "zai-coding-plan/glm-5.3"},
	}

	tests := []struct {
		name    string
		config  AgentConfig
		wantErr bool
	}{
		{
			name: "valid configuration with pre-fetched supported CLIs",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "agy", Model: "gemini-2.5-flash"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: false,
		},
		{
			name: "unsupported CLI with pre-fetched supported CLIs",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "unsupported-cli", Model: "some-model"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "unsupported model with pre-fetched supported CLIs",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "agy", Model: "unknown-model"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "valid opencode base model with slash in name",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "opencode", Model: "zai-coding-plan/glm-5.3"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: false,
		},
		{
			name: "valid opencode variant model matching supported base model",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "opencode", Model: "zai-coding-plan/glm-5.3/high"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: false,
		},
		{
			name: "unsupported opencode variant model checked against supported CLIs",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "opencode", Model: "unsupported-provider/model/high"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
		{
			name: "agy variant model still checked against supported CLIs",
			config: AgentConfig{
				ID:          "agent-one",
				Name:        "agent1",
				Description: "Test Agent 1",
				CLI: []CLITarget{
					{CLI: "agy", Model: "unknown-model-low"},
				},
				RunDirs: []string{"/tmp/run"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.ValidateWithCLIs(supportedCLIs)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func boolPtr(b bool) *bool {
	return &b
}
