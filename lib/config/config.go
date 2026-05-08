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
	Telegram Telegram `yaml:"telegram"`
}

func (c Config) validate() error {
	if err := c.Telegram.validate(); err != nil {
		return err
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

	return cfg, nil
}
