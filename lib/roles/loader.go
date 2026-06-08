package roles

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

type Loader struct {
	AgentsDir string
}

func NewLoader(agentsDir string) *Loader {
	return &Loader{
		AgentsDir: agentsDir,
	}
}

// LoadAll scans the AgentsDir/agents/ directory for agent configurations.
func (l *Loader) LoadAll() ([]*Agent, error) {
	agentsPath := filepath.Join(l.AgentsDir, "agents")
	entries, err := os.ReadDir(agentsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read agents directory: %w", err)
	}

	var agents []*Agent
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		agentPath := filepath.Join(agentsPath, entry.Name())
		configPath := filepath.Join(agentPath, "config.yaml")

		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			continue
		}

		configData, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config for agent %s: %w", entry.Name(), err)
		}

		var cfg AgentConfig
		if err := yaml.Unmarshal(configData, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config for agent %s: %w", entry.Name(), err)
		}

		// Validation: Name in config should match directory name? 
		// Or just use the name in config. Let's use the name in config.
		if cfg.Name == "" {
			cfg.Name = entry.Name()
		}

		agents = append(agents, &Agent{
			Config: cfg,
			Path:   agentPath,
		})
	}

	return agents, nil
}
