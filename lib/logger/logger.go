package logger

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func IsDebugEnabled() bool {
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		if _, err := os.Stat("/home/user/config.yaml"); err == nil {
			configPath = "/home/user/config.yaml"
		} else {
			configPath = "config.yaml"
		}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	var cfg struct {
		Debug bool `yaml:"debug"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return cfg.Debug
}

func SetupLogger(appName string) {
	home, err := os.UserHomeDir()
	var logDest io.Writer = os.Stderr
	if err == nil {
		logDir := filepath.Join(home, "logs")
		_ = os.MkdirAll(logDir, 0755)
		logFile, err := os.OpenFile(filepath.Join(logDir, appName+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			logDest = logFile
		}
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: logDest, TimeFormat: time.RFC3339})

	if IsDebugEnabled() {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Warn().Msgf("Debug mode is enabled in %s", appName)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}
