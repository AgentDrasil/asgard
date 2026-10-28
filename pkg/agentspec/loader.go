package agentspec

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"

	"github.com/AgentDrasil/asgard/agentwrapper"
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

	teams, err := parseTeams(teamsData)
	if err != nil {
		return nil, err
	}
	validTeams := teamSet(teams)

	supportedCLIs := agentwrapper.GetSupportedCLIsAndModels()

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

		cfg, err := parseAgentConfig(entry.Name(), configData, validTeams, supportedCLIs)
		if err != nil {
			return nil, err
		}

		agent := &Agent{
			Config: cfg,
			Path:   agentPath,
		}

		if cfg.Type == "workflow" {
			workflowPath := filepath.Join(agentPath, "workflow.yaml")
			if _, err := os.Stat(workflowPath); err != nil {
				return nil, fmt.Errorf("agent %s is of type workflow but workflow.yaml is missing: %w", entry.Name(), err)
			}
			agent.WorkflowPath = workflowPath
		}

		agents = append(agents, agent)
	}

	return agents, nil
}

// parseTeams parses teams.yaml, accepting both a {teams: [...]} mapping and a
// bare list.
func parseTeams(data []byte) ([]string, error) {
	var tCfg teamsConfig
	if err := yaml.Unmarshal(data, &tCfg); err != nil {
		return nil, fmt.Errorf("failed to parse teams.yaml: %w", err)
	}

	if len(tCfg.Teams) == 0 {
		var teamsList []string
		if err := yaml.Unmarshal(data, &teamsList); err == nil && len(teamsList) > 0 {
			tCfg.Teams = teamsList
		}
	}
	return tCfg.Teams, nil
}

func teamSet(teams []string) map[string]bool {
	valid := make(map[string]bool, len(teams))
	for _, t := range teams {
		valid[t] = true
	}
	return valid
}

// parseAgentConfig parses and validates the config.yaml of the agent stored
// under directory name id. Shared by the directory Loader and the file-set
// parser so both apply identical validation.
func parseAgentConfig(id string, data []byte, validTeams map[string]bool, supportedCLIs map[string][]string) (AgentConfig, error) {
	var cfg AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse config for agent %s: %w", id, err)
	}

	if cfg.ID != id {
		return cfg, fmt.Errorf("agent ID %q does not match directory name %q", cfg.ID, id)
	}

	if cfg.Name == "" {
		cfg.Name = id
	}

	if err := cfg.ValidateWithCLIs(supportedCLIs); err != nil {
		return cfg, fmt.Errorf("invalid config for agent %s: %w", id, err)
	}

	if cfg.Team != "" && !validTeams[cfg.Team] {
		return cfg, fmt.Errorf("team %q for agent %s is not defined in teams.yaml", cfg.Team, id)
	}
	return cfg, nil
}
