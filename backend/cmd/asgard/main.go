package main

import (
	"context"
	"flag"
	"os"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/app"
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

func main() {
	flag.Parse()

	ctx := context.Background()
	if err := app.Run(ctx, app.WithConfigPath(*configPathFlag)); err != nil {
		log.Fatal().Err(err).Msg("Asgard server stopped with error")
	}
}
