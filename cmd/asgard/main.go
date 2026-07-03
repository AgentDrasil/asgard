package main

import (
	"flag"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agentwrapper"
	"github.com/AgentDrasil/asgard/lib/api/a2aagent"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/db"
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
	if err := agentwrapper.ValidateAgySetup(); err != nil {
		log.Fatal().Err(err).Msg("Agy agent setup validation failed")
	}
	if err := agentwrapper.ValidateOpencodeSetup(); err != nil {
		log.Fatal().Err(err).Msg("Opencode agent setup validation failed")
	}

	flag.Parse()

	conf, err := config.LoadConfig(*configPathFlag)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load config")
	}

	setupLogger(conf)

	_, err = db.NewDB(conf)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to database")
	}

	srv, err := a2aagent.New(conf)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize agents server")
	}

	if err := srv.Start(); err != nil {
		log.Fatal().Err(err).Msg("Failed to start A2A HTTP server")
	}
}
