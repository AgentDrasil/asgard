package agents

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

type teamsConfig struct {
	Teams []string `yaml:"teams"`
}

// LoadAll scans the AgentsDir/agents/ directory for agent configurations.
func (l *Loader) LoadAll() ([]*Agent, error) {
	agentsPath := filepath.Join(l.AgentsDir, "agents")
	entries, err := os.ReadDir(agentsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read agents directory: %w", err)
	}

	teamsPath := filepath.Join(l.AgentsDir, "teams.yaml")
	teamsData, err := os.ReadFile(teamsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read teams.yaml: %w", err)
	}

	var tCfg teamsConfig
	if err := yaml.Unmarshal(teamsData, &tCfg); err != nil {
		return nil, fmt.Errorf("failed to parse teams.yaml: %w", err)
	}

	if len(tCfg.Teams) == 0 {
		var teamsList []string
		if err := yaml.Unmarshal(teamsData, &teamsList); err == nil && len(teamsList) > 0 {
			tCfg.Teams = teamsList
		}
	}

	validTeams := make(map[string]bool)
	for _, t := range tCfg.Teams {
		validTeams[t] = true
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

		if cfg.ID != entry.Name() {
			return nil, fmt.Errorf("agent ID %q does not match directory name %q", cfg.ID, entry.Name())
		}

		// Validation: Name in config should match directory name?
		// Or just use the name in config. Let's use the name in config.
		if cfg.Name == "" {
			cfg.Name = entry.Name()
		}

		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("invalid config for agent %s: %w", entry.Name(), err)
		}

		if cfg.Team != "" && !validTeams[cfg.Team] {
			return nil, fmt.Errorf("team %q for agent %s is not defined in teams.yaml", cfg.Team, entry.Name())
		}

		agents = append(agents, &Agent{
			Config: cfg,
			Path:   agentPath,
		})
	}

	return agents, nil
}
