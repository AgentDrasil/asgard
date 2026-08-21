package main

import (
	"os"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/fakebash"
	"github.com/AgentDrasil/asgard/backend/lib/logger"
)

func main() {
	logger.SetupLogger("fakebash")
	log.Debug().Interface("args", os.Args).Msg("fakebash: command requested")

	if err := fakebash.RunClient(os.Args); err != nil {
		log.Error().Err(err).Msg("fakebash client error")
		os.Exit(1)
	}
}
