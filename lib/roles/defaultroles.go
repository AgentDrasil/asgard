package roles

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

func defaultRoleAgentFatherWithDir(agentsDir string) *AgentConfig {
	return &AgentConfig{
		ID:          "agentfather",
		Name:        "Agent Father",
		Description: "The agent creates other agents.",
		CLI: []CLITarget{
			{
				CLI:   "agy",
				Model: "Gemini 3.5 Flash (Low)",
			},
		},
		RunDirs: []string{agentsDir},
	}
}

func (l *Loader) initializeDefaultRolesIfMissingAgentFather(agentsDir string) error {
	absAgentsDir, err := filepath.Abs(agentsDir)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute path of agentsDir: %w", err)
	}

	agentCfg := defaultRoleAgentFatherWithDir(absAgentsDir)

	absLoaderAgentsDir, err := filepath.Abs(l.AgentsDir)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute path of l.AgentsDir: %w", err)
	}
	agentDir := filepath.Join(absLoaderAgentsDir, "agents", agentCfg.ID)

	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return fmt.Errorf("failed to create agent directory: %w", err)
	}

	yamlData, err := yaml.Marshal(agentCfg)
	if err != nil {
		return fmt.Errorf("failed to marshal default role agent father config: %w", err)
	}

	configFile := filepath.Join(agentDir, "config.yaml")
	if err := os.WriteFile(configFile, yamlData, 0644); err != nil {
		return fmt.Errorf("failed to write default role agent father config: %w", err)
	}

	return nil
}
