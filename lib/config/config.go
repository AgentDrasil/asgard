package config

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

type Telegram struct {
	BotToken       string `yaml:"bot_token"`
	AllowedSenders []int  `yaml:"allowed_senders"`
}

func (s Telegram) validate() error {
	if s.BotToken == "" {
		return fmt.Errorf("missing bot_token")
	}

	return nil
}

type Config struct {
	DB       string   `yaml:"db"`
	DSN      string   `yaml:"dsn"`
	AgentDir string   `yaml:"agent_dir"`
	Telegram Telegram `yaml:"telegram"`
}

func (c Config) validate() error {
	if c.DB != "pg" && c.DB != "sqlite" {
		return fmt.Errorf("invalid db: %s, must be 'pg' or 'sqlite'", c.DB)
	}
	if c.DSN == "" {
		return fmt.Errorf("missing dsn")
	}
	if c.AgentDir == "" {
		return fmt.Errorf("missing agent_dir")
	}
	if err := c.Telegram.validate(); err != nil {
		return err
	}

	return nil
}

func (c Config) verifyDirs() error {
	dirs := []string{
		c.AgentDir,
		fmt.Sprintf("%s/agents", c.AgentDir),
		fmt.Sprintf("%s/auths", c.AgentDir),
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

	if err := cfg.verifyDirs(); err != nil {
		return nil, err
	}

	return cfg, nil
}
