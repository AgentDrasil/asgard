package main

import (
	"context"
	"flag"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/telegram"
)

func defaultConfigPath() string {
	path := os.Getenv("CONFIG_PATH")
	if path != "" {
		return path
	}

	return "config.yaml"
}

var (
	configPathFlag = flag.String("config", defaultConfigPath(), "path to config file")
)

func setupLogger(conf *config.Config) {
	if conf.Debug {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
		log.Warn().Msg("Debug mode is enabled")
	}
}

func main() {
	flag.Parse()

	conf, err := config.LoadConfig(*configPathFlag)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load config")
	}

	setupLogger(conf)

	if len(conf.Telegram.AllowedSenders) == 0 {
		if err := telegram.StartHelper(context.Background(), conf.Telegram.BotToken); err != nil {
			log.Fatal().Err(err).Msg("Failed to start helper bot")
		}
	}
}
