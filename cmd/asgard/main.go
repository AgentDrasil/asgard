package main

import (
	"flag"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agentwrapper"
	"github.com/AgentDrasil/asgard/lib/api"
	"github.com/AgentDrasil/asgard/lib/cleanup"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/db"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
	"github.com/AgentDrasil/asgard/lib/sshagent"
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
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
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

	if err := agentwrapper.ValidateAgySetup(); err != nil {
		log.Fatal().Err(err).Msg("Agy agent setup validation failed")
	}
	if err := agentwrapper.ValidateOpencodeSetup(); err != nil {
		log.Fatal().Err(err).Msg("Opencode agent setup validation failed")
	}

	setupLogger(conf)

	if err := sshagent.SetupSSHAgent(); err != nil {
		log.Fatal().Err(err).Msg("Failed to setup SSH agent from ~/.ssh")
	}

	database, err := db.NewDB(conf)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to database")
	}

	if err := dbmodels.AutoMigrate(database); err != nil {
		log.Fatal().Err(err).Msg("Failed to migrate database")
	}

	repo := dbmodels.NewSessionRepository(database)
	scheduler, err := cleanup.NewScheduler(repo)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize cleanup scheduler")
	}
	defer func() {
		if err := scheduler.Shutdown(); err != nil {
			log.Error().Err(err).Msg("Failed to shutdown cleanup scheduler")
		}
	}()

	srv, err := api.New(conf, database)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize agents server")
	}

	if err := srv.Start(); err != nil {
		log.Fatal().Err(err).Msg("Failed to start A2A HTTP server")
	}
}
