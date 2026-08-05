package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Debug                   bool   `yaml:"debug"`
	DB                      string `yaml:"db"`
	DSN                     string `yaml:"dsn"`
	AgentDir                string `yaml:"agent_dir"`
	Port                    int    `yaml:"port"`
	InternalPort            int    `yaml:"internal_port"`
	Host                    string `yaml:"host"`
	WebUIPath               string `yaml:"webui_path"`
	GeminiAPIKey            string `yaml:"gemini_api_key"`
	GeminiModelForChatTitle string `yaml:"gemini_model_for_chat_title"`
}

func (c *Config) APIHost() string {
	if c == nil || c.Port <= 0 {
		return "http://127.0.0.1:8080"
	}
	return fmt.Sprintf("http://127.0.0.1:%d", c.Port)
}

func (c *Config) InternalAPIHost() string {
	if c == nil || c.InternalPort <= 0 {
		return "http://127.0.0.1:8081"
	}
	return fmt.Sprintf("http://127.0.0.1:%d", c.InternalPort)
}

func (c *Config) StatusURL() string {
	return c.InternalAPIHost() + "/agent-status"
}

func (c *Config) validate() error {
	if c.Host == "" {
		return fmt.Errorf("missing host")
	}
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

	if c.GeminiAPIKey == "" {
		return fmt.Errorf("missing gemini_api_key")
	}
	if c.GeminiModelForChatTitle == "" {
		return fmt.Errorf("missing gemini_model_for_chat_title")
	}

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

	if cfg.Port <= 0 {
		cfg.Port = 8080
	}

	if cfg.InternalPort <= 0 {
		cfg.InternalPort = 8081
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	if err := cfg.verifyDirs(); err != nil {
		return nil, err
	}

	return cfg, nil
}
