package main

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/lib/app"
	"github.com/AgentDrasil/asgard/backend/lib/bwrap"

	// Register notebook workflow functions in the default registry.
	_ "github.com/AgentDrasil/asgard/plugins/notebook"
)

func main() {
	// Models/usage queries invoke vendor CLIs non-interactively, which can
	// fire their hooks; run them inside the agent sandbox, not on the host.
	agentwrapper.SetQueryRunner(bwrap.RunQuery)

	ctx := context.Background()
	if err := app.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("Asgard server stopped with error")
	}
}
