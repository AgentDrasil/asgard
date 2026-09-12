package main

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/app"

	// Register notebook workflow functions in the default registry.
	_ "github.com/AgentDrasil/asgard/plugins/notebook"
)

func main() {
	ctx := context.Background()
	if err := app.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("Asgard server stopped with error")
	}
}
