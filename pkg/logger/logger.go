package logger

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/paths"
)

func IsDebugEnabled() bool {
	data, err := os.ReadFile(paths.ConfigFile())
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
	logDir := paths.LogsDir()
	var logDest io.Writer = os.Stderr
	if err := os.MkdirAll(logDir, 0755); err == nil {
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
