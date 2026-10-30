package agentspec

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
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

func TestMountPathEntry_UnmarshalYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		yamlData string
		want     MountPathEntry
		wantErr  bool
	}{
		{
			name:     "scalar string",
			yamlData: `"/data/src"`,
			want:     MountPathEntry{Path: "/data/src"},
		},
		{
			name: "mapping without mask rules",
			yamlData: `
path: /data/src
`,
			want: MountPathEntry{Path: "/data/src"},
		},
		{
			name: "mapping with mask rules",
			yamlData: `
path: /data/src
mask_rules:
  mode: mask_others
  keep_rw:
    - .pnpm-store
`,
			want: MountPathEntry{
				Path: "/data/src",
				MaskRules: &SubdirMaskRule{
					Base:   "/data/src",
					Mode:   MaskOthers,
					KeepRW: []string{".pnpm-store"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var entry MountPathEntry
			err := yaml.Unmarshal([]byte(tt.yamlData), &entry)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, entry)
		})
	}
}

func TestMountConfig_UnmarshalYAML(t *testing.T) {
	t.Parallel()

	t.Run("backward-compatible scalar strings in readwrite and readonly", func(t *testing.T) {
		t.Parallel()

		yamlData := `
readonly:
  - /data/ro1
  - /data/ro2
readwrite:
  - /data/rw1
  - /data/rw2
`
		var mc MountConfig
		err := yaml.Unmarshal([]byte(yamlData), &mc)
		require.NoError(t, err)
		assert.Equal(t, []string{"/data/ro1", "/data/ro2"}, mc.ReadOnly)
		assert.Equal(t, []string{"/data/rw1", "/data/rw2"}, mc.ReadWrite)
		assert.Nil(t, mc.MaskRules)

		entries := mc.EffectiveReadWriteEntries()
		require.Len(t, entries, 2)
		assert.Equal(t, "/data/rw1", entries[0].Path)
		assert.Nil(t, entries[0].MaskRules)
		assert.Equal(t, "/data/rw2", entries[1].Path)
		assert.Nil(t, entries[1].MaskRules)
		assert.Nil(t, mc.MaskRuleFor("/data/rw1"))
	})

	t.Run("mapping items in readonly", func(t *testing.T) {
		t.Parallel()

		yamlData := `
readonly:
  - path: /data/ro1
  - /data/ro2
`
		var mc MountConfig
		err := yaml.Unmarshal([]byte(yamlData), &mc)
		require.NoError(t, err)
		assert.Equal(t, []string{"/data/ro1", "/data/ro2"}, mc.ReadOnly)
	})

	t.Run("inline mask_rules in readwrite", func(t *testing.T) {
		t.Parallel()

		yamlData := `
readwrite:
  - /data/rw1
  - path: /data/src
    mask_rules:
      mode: mask_others
      keep_rw:
        - .pnpm-store
`
		var mc MountConfig
		err := yaml.Unmarshal([]byte(yamlData), &mc)
		require.NoError(t, err)
		assert.Equal(t, []string{"/data/rw1", "/data/src"}, mc.ReadWrite)

		rule := mc.MaskRuleFor("/data/src")
		require.NotNil(t, rule)
		assert.Equal(t, "/data/src", rule.Base)
		assert.Equal(t, MaskOthers, rule.Mode)
		assert.Equal(t, []string{".pnpm-store"}, rule.KeepRW)

		assert.Nil(t, mc.MaskRuleFor("/data/rw1"))

		entries := mc.EffectiveReadWriteEntries()
		require.Len(t, entries, 2)
		assert.Equal(t, "/data/rw1", entries[0].Path)
		assert.Nil(t, entries[0].MaskRules)
		assert.Equal(t, "/data/src", entries[1].Path)
		require.NotNil(t, entries[1].MaskRules)
		assert.Equal(t, MaskOthers, entries[1].MaskRules.Mode)
	})

	t.Run("top-level mask_rules list with base matching readwrite", func(t *testing.T) {
		t.Parallel()

		yamlData := `
readwrite:
  - /data/src
mask_rules:
  - base: /data/src
    mode: mask_others
    keep_rw:
      - .pnpm-store
`
		var mc MountConfig
		err := yaml.Unmarshal([]byte(yamlData), &mc)
		require.NoError(t, err)
		assert.Equal(t, []string{"/data/src"}, mc.ReadWrite)

		rule := mc.MaskRuleFor("/data/src")
		require.NotNil(t, rule)
		assert.Equal(t, "/data/src", rule.Base)
		assert.Equal(t, MaskOthers, rule.Mode)
		assert.Equal(t, []string{".pnpm-store"}, rule.KeepRW)

		entries := mc.EffectiveReadWriteEntries()
		require.Len(t, entries, 1)
		assert.Equal(t, "/data/src", entries[0].Path)
		require.NotNil(t, entries[0].MaskRules)
		assert.Equal(t, MaskOthers, entries[0].MaskRules.Mode)
	})

	t.Run("top-level mask_rules list as alternative syntax without readwrite", func(t *testing.T) {
		t.Parallel()

		yamlData := `
mask_rules:
  - base: /data/src
    mode: mask_others
    keep_rw:
      - .pnpm-store
`
		var mc MountConfig
		err := yaml.Unmarshal([]byte(yamlData), &mc)
		require.NoError(t, err)
		assert.Equal(t, []string{"/data/src"}, mc.ReadWrite)

		rule := mc.MaskRuleFor("/data/src")
		require.NotNil(t, rule)
		assert.Equal(t, "/data/src", rule.Base)

		entries := mc.EffectiveReadWriteEntries()
		require.Len(t, entries, 1)
		assert.Equal(t, "/data/src", entries[0].Path)
		assert.NotNil(t, entries[0].MaskRules)
	})

	t.Run("mask_rules as map with deterministic key sorting", func(t *testing.T) {
		t.Parallel()

		yamlData := `
mask_rules:
  "/z/path":
    mode: mask_others
  "/a/path":
    mode: mask_others
  "/m/path":
    mode: mask_others
`
		var mc MountConfig
		err := yaml.Unmarshal([]byte(yamlData), &mc)
		require.NoError(t, err)
		assert.Equal(t, []string{"/a/path", "/m/path", "/z/path"}, mc.ReadWrite)
	})

	t.Run("null value in mask_rules map rejected", func(t *testing.T) {
		t.Parallel()

		yamlData := `
mask_rules:
  "/a/path": null
`
		var mc MountConfig
		err := yaml.Unmarshal([]byte(yamlData), &mc)
		require.Error(t, err)
		assert.ErrorContains(t, err, "mask_rules for \"/a/path\" cannot be null")
	})

	t.Run("readonly mount with mask_rules rejected", func(t *testing.T) {
		t.Parallel()

		yamlData := `
readonly:
  - path: /ro
    mask_rules:
      mode: mask_others
`
		var mc MountConfig
		err := yaml.Unmarshal([]byte(yamlData), &mc)
		require.Error(t, err)
		assert.ErrorContains(t, err, "readonly mount \"/ro\" cannot have mask_rules")
	})

	t.Run("duplicate mask_rules for same base in top-level list rejected", func(t *testing.T) {
		t.Parallel()

		yamlData := `
mask_rules:
  - base: /data/src
    mode: mask_others
  - base: /data/src
    mode: mask_matched
    mask_ro: ["secret"]
`
		var mc MountConfig
		err := yaml.Unmarshal([]byte(yamlData), &mc)
		require.Error(t, err)
		assert.ErrorContains(t, err, "duplicate mask_rules for base \"/data/src\"")
	})

	t.Run("duplicate mask_rules between inline and top-level rejected", func(t *testing.T) {
		t.Parallel()

		yamlData := `
readwrite:
  - path: /data/src
    mask_rules:
      mode: mask_others
mask_rules:
  - base: /data/src
    mode: mask_matched
    mask_ro: ["secret"]
`
		var mc MountConfig
		err := yaml.Unmarshal([]byte(yamlData), &mc)
		require.Error(t, err)
		assert.ErrorContains(t, err, "duplicate mask_rules for base \"/data/src\"")
	})

	t.Run("top-level mask_rules with empty base preserved for Validate", func(t *testing.T) {
		t.Parallel()

		yamlData := `
mask_rules:
  - base: ""
    mode: mask_others
`
		var mc MountConfig
		err := yaml.Unmarshal([]byte(yamlData), &mc)
		require.NoError(t, err)
		assert.Contains(t, mc.MaskRules, "")

		cfg := AgentConfig{
			ID:          "test-agent",
			Name:        "test-agent",
			Description: "desc",
			CLI:         []CLITarget{{CLI: "opencode", Model: "deepseek-chat"}},
			RunDirs:     []string{"/tmp/run"},
			MountDirs:   mc,
		}
		err = cfg.ValidateWithCLIs(map[string][]string{"opencode": {"deepseek-chat"}})
		require.Error(t, err)
		assert.ErrorContains(t, err, "mask rule base directory cannot be empty")
	})
}

func TestAgentConfig_EffectiveLockModeAndIsReadOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		lockMode     LockMode
		wantLockMode LockMode
		wantReadOnly bool
	}{
		{
			name:         "empty defaults to exclusive",
			lockMode:     "",
			wantLockMode: LockModeExclusive,
			wantReadOnly: false,
		},
		{
			name:         "exclusive",
			lockMode:     LockModeExclusive,
			wantLockMode: LockModeExclusive,
			wantReadOnly: false,
		},
		{
			name:         "readonly",
			lockMode:     LockModeReadOnly,
			wantLockMode: LockModeReadOnly,
			wantReadOnly: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &AgentConfig{LockMode: tt.lockMode}
			assert.Equal(t, tt.wantLockMode, cfg.EffectiveLockMode())
			assert.Equal(t, tt.wantReadOnly, cfg.IsReadOnly())
		})
	}
}

func TestAgentConfig_Validate_LockModeAndMaskRules(t *testing.T) {
	t.Parallel()

	validCLI := []CLITarget{{CLI: "opencode", Model: "deepseek-chat"}}
	supportedCLIs := map[string][]string{
		"opencode": {"deepseek-chat"},
	}

	baseConfig := func() AgentConfig {
		return AgentConfig{
			ID:          "test-agent",
			Name:        "test-agent",
			Description: "Test agent description",
			CLI:         validCLI,
			RunDirs:     []string{"/tmp/run"},
		}
	}

	tests := []struct {
		name      string
		modify    func(cfg *AgentConfig)
		wantErr   bool
		errSubstr string
	}{
		{
			name: "valid exclusive lock_mode without mounts",
			modify: func(cfg *AgentConfig) {
				cfg.LockMode = LockModeExclusive
			},
			wantErr: false,
		},
		{
			name: "valid exclusive lock_mode with rw mounts",
			modify: func(cfg *AgentConfig) {
				cfg.LockMode = LockModeExclusive
				cfg.MountDirs.ReadWrite = []string{"/tmp/rw"}
			},
			wantErr: false,
		},
		{
			name: "valid readonly lock_mode with ro mounts",
			modify: func(cfg *AgentConfig) {
				cfg.LockMode = LockModeReadOnly
				cfg.MountDirs.ReadOnly = []string{"/tmp/ro"}
			},
			wantErr: false,
		},
		{
			name: "invalid lock_mode string",
			modify: func(cfg *AgentConfig) {
				cfg.LockMode = "invalid_mode"
			},
			wantErr:   true,
			errSubstr: "invalid lock_mode",
		},
		{
			name: "lock_mode readonly rejected when readwrite mounts present",
			modify: func(cfg *AgentConfig) {
				cfg.LockMode = LockModeReadOnly
				cfg.MountDirs.ReadWrite = []string{"/tmp/rw"}
			},
			wantErr:   true,
			errSubstr: "lock_mode \"readonly\" cannot have readwrite",
		},
		{
			name: "lock_mode readonly rejected when mask_rules present",
			modify: func(cfg *AgentConfig) {
				cfg.LockMode = LockModeReadOnly
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"/tmp/rw": {
						Base:   "/tmp/rw",
						Mode:   MaskOthers,
						KeepRW: []string{".pnpm-store"},
					},
				}
			},
			wantErr:   true,
			errSubstr: "lock_mode \"readonly\" cannot have readwrite",
		},
		{
			name: "valid inline mask_rules mask_others with keep_rw",
			modify: func(cfg *AgentConfig) {
				cfg.MountDirs.ReadWrite = []string{"/data/src"}
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"/data/src": {
						Base:   "/data/src",
						Mode:   MaskOthers,
						KeepRW: []string{".pnpm-store", `cache-[0-9]+`},
					},
				}
			},
			wantErr: false,
		},
		{
			name: "valid mask_matched with mask_ro regex",
			modify: func(cfg *AgentConfig) {
				cfg.MountDirs.ReadWrite = []string{"/data/src"}
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"/data/src": {
						Base:   "/data/src",
						Mode:   MaskMatched,
						MaskRO: []string{`secret.*`, `\.env`},
					},
				}
			},
			wantErr: false,
		},
		{
			name: "invalid mask_rules mode",
			modify: func(cfg *AgentConfig) {
				cfg.MountDirs.ReadWrite = []string{"/data/src"}
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"/data/src": {
						Base:   "/data/src",
						Mode:   "unknown_mode",
						KeepRW: []string{".pnpm-store"},
					},
				}
			},
			wantErr:   true,
			errSubstr: "invalid mask rule mode",
		},
		{
			name: "empty mask_rules mode",
			modify: func(cfg *AgentConfig) {
				cfg.MountDirs.ReadWrite = []string{"/data/src"}
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"/data/src": {
						Base:   "/data/src",
						Mode:   "",
						KeepRW: []string{".pnpm-store"},
					},
				}
			},
			wantErr:   true,
			errSubstr: "invalid mask rule mode",
		},
		{
			name: "relative path in mask_rules base",
			modify: func(cfg *AgentConfig) {
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"relative/path": {
						Base:   "relative/path",
						Mode:   MaskOthers,
						KeepRW: []string{".pnpm-store"},
					},
				}
			},
			wantErr:   true,
			errSubstr: "mask rule base directory must be an absolute path",
		},
		{
			name: "empty mask_rules base",
			modify: func(cfg *AgentConfig) {
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"": {
						Base:   "",
						Mode:   MaskOthers,
						KeepRW: []string{".pnpm-store"},
					},
				}
			},
			wantErr:   true,
			errSubstr: "base directory cannot be empty",
		},
		{
			name: "mask_matched with empty mask_ro rejected",
			modify: func(cfg *AgentConfig) {
				cfg.MountDirs.ReadWrite = []string{"/data/src"}
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"/data/src": {
						Base:   "/data/src",
						Mode:   MaskMatched,
						MaskRO: nil,
					},
				}
			},
			wantErr:   true,
			errSubstr: "requires at least one mask_ro pattern",
		},
		{
			name: "empty pattern in keep_rw rejected",
			modify: func(cfg *AgentConfig) {
				cfg.MountDirs.ReadWrite = []string{"/data/src"}
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"/data/src": {
						Base:   "/data/src",
						Mode:   MaskOthers,
						KeepRW: []string{""},
					},
				}
			},
			wantErr:   true,
			errSubstr: "keep_rw pattern cannot be empty",
		},
		{
			name: "invalid regex in keep_rw rejected",
			modify: func(cfg *AgentConfig) {
				cfg.MountDirs.ReadWrite = []string{"/data/src"}
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"/data/src": {
						Base:   "/data/src",
						Mode:   MaskOthers,
						KeepRW: []string{"[unclosed"},
					},
				}
			},
			wantErr:   true,
			errSubstr: "invalid keep_rw pattern",
		},
		{
			name: "empty pattern in mask_ro rejected",
			modify: func(cfg *AgentConfig) {
				cfg.MountDirs.ReadWrite = []string{"/data/src"}
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"/data/src": {
						Base:   "/data/src",
						Mode:   MaskMatched,
						MaskRO: []string{""},
					},
				}
			},
			wantErr:   true,
			errSubstr: "mask_ro pattern cannot be empty",
		},
		{
			name: "invalid regex in mask_ro rejected",
			modify: func(cfg *AgentConfig) {
				cfg.MountDirs.ReadWrite = []string{"/data/src"}
				cfg.MountDirs.MaskRules = map[string]*SubdirMaskRule{
					"/data/src": {
						Base:   "/data/src",
						Mode:   MaskMatched,
						MaskRO: []string{"(?P<unclosed"},
					},
				}
			},
			wantErr:   true,
			errSubstr: "invalid mask_ro pattern",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := baseConfig()
			tt.modify(&cfg)

			err := cfg.ValidateWithCLIs(supportedCLIs)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errSubstr != "" {
					assert.ErrorContains(t, err, tt.errSubstr)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestMountConfig_Clone(t *testing.T) {
	t.Parallel()

	orig := MountConfig{
		ReadOnly:  []string{"/ro1", "/ro2"},
		ReadWrite: []string{"/rw1", "/rw2"},
		ReadWriteEntries: []MountPathEntry{
			{
				Path: "/rw1",
				MaskRules: &SubdirMaskRule{
					Base:   "/rw1",
					Mode:   MaskOthers,
					KeepRW: []string{".pnpm-store"},
					MaskRO: []string{".env"},
				},
			},
		},
		MaskRules: map[string]*SubdirMaskRule{
			"/rw1": {
				Base:   "/rw1",
				Mode:   MaskOthers,
				KeepRW: []string{".pnpm-store"},
				MaskRO: []string{".env"},
			},
		},
	}

	clone := orig.Clone()

	assert.Equal(t, orig.ReadOnly, clone.ReadOnly)
	assert.Equal(t, orig.ReadWrite, clone.ReadWrite)
	assert.Equal(t, orig.MaskRules["/rw1"].KeepRW, clone.MaskRules["/rw1"].KeepRW)

	clone.ReadOnly[0] = "/mutated-ro"
	clone.ReadWrite[0] = "/mutated-rw"
	clone.ReadWriteEntries[0].Path = "/mutated-entry"
	clone.ReadWriteEntries[0].MaskRules.KeepRW[0] = "mutated-keep"
	clone.MaskRules["/rw1"].KeepRW[0] = "mutated-keep2"
	clone.MaskRules["/rw1"].MaskRO[0] = "mutated-mask"

	assert.Equal(t, "/ro1", orig.ReadOnly[0])
	assert.Equal(t, "/rw1", orig.ReadWrite[0])
	assert.Equal(t, "/rw1", orig.ReadWriteEntries[0].Path)
	assert.Equal(t, ".pnpm-store", orig.ReadWriteEntries[0].MaskRules.KeepRW[0])
	assert.Equal(t, ".pnpm-store", orig.MaskRules["/rw1"].KeepRW[0])
	assert.Equal(t, ".env", orig.MaskRules["/rw1"].MaskRO[0])
}
