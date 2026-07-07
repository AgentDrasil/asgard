package main

import (
	"os"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/fakebash"
)

func main() {
	fakebash.SetupLogger("fakebashd")
	log.Info().Msg("fakebashd: started main")

	if err := fakebash.RunDaemon(); err != nil {
		log.Error().Err(err).Msg("fakebashd daemon error")
		os.Exit(1)
	}
}
