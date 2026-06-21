package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Debug    bool     `yaml:"debug"`
	DB       string   `yaml:"db"`
	DSN      string   `yaml:"dsn"`
	AgentDir string   `yaml:"agent_dir"`
	Port     int      `yaml:"port"`
}

func (c *Config) validate() error {
	if c.DB != "pg" && c.DB != "sqlite" {
		return fmt.Errorf("invalid db: %s, must be 'pg' or 'sqlite'", c.DB)
	}
	if c.DSN == "" {
		return fmt.Errorf("missing dsn")
	}
	if c.AgentDir == "" {
		return fmt.Errorf("missing agent_dir")
	}

	absDir, err := filepath.Abs(c.AgentDir)
	if err != nil {
		return fmt.Errorf("failed to make agent_dir absolute: %w", err)
	}
	c.AgentDir = absDir

	return nil
}

func (c Config) verifyDirs() error {
	dirs := []string{
		c.AgentDir,
		fmt.Sprintf("%s/agents", c.AgentDir),
	}

	for _, d := range dirs {
		info, err := os.Stat(d)
		if err != nil {
			return fmt.Errorf("directory verification failed: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", d)
		}
	}

	return nil
}

func LoadConfig(path string) (*Config, error) {
	cfg := &Config{}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	err = yaml.Unmarshal(data, cfg)
	if err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	if cfg.Port <= 0 {
		cfg.Port = 8080
	}

	if err := cfg.verifyDirs(); err != nil {
		return nil, err
	}

	return cfg, nil
}
