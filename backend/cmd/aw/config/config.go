package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
)

// Config holds the allowed model patterns for each agent.
type Config struct {
	Agents []map[string][]string `yaml:"agents"`
}

// LoadConfig loads the configuration from XDG_CONFIG_HOME/aw/config.yaml or ~/.config/aw/config.yaml.
// If the config file does not exist, it returns a nil Config and no error.
func LoadConfig() (*Config, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		configHome = filepath.Join(home, ".config")
	}
	path := filepath.Join(configHome, "aw", "config.yaml")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// IsModelAllowed returns true if the model name matches the allowed patterns for the given agent.
// If the config is nil, or the agent has no entry in the config, it returns true.
func (c *Config) IsModelAllowed(agentName, modelName string) bool {
	if c == nil {
		return true
	}
	var patterns []string
	found := false
	for _, entry := range c.Agents {
		if p, ok := entry[agentName]; ok {
			patterns = p
			found = true
			break
		}
	}
	if !found {
		return true
	}
	for _, pattern := range patterns {
		p := pattern
		if !strings.HasPrefix(p, "(?i)") {
			p = "(?i)" + p
		}
		matched, err := regexp.MatchString(p, modelName)
		if err == nil && matched {
			return true
		}
	}
	return false
}
