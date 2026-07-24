package agentwrapper

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/agy"
	"github.com/AgentDrasil/asgard/lib/agentwrapper/opencode"
	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

var defaultClients = map[string]types.CLIClient{
	"agy":      &agy.Client{},
	"opencode": &opencode.Client{},
}

var clients = defaultClients

// SetClients allows overriding the CLI clients, useful for testing.
func SetClients(c map[string]types.CLIClient) {
	if c == nil {
		clients = defaultClients
	} else {
		clients = c
	}
}

func GetSupportedCLIsAndModels() map[string][]string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res := make(map[string][]string)

	for name, client := range clients {
		if models, err := client.Models(ctx, types.UsageOptions{}); err == nil {
			res[name] = models
		} else {
			res[name] = []string{}
		}
	}

	return res
}

func GetQuota(ctx context.Context) (map[string][]types.ModelUsage, error) {
	res := make(map[string][]types.ModelUsage)
	for name, client := range clients {
		if usages, err := client.Usage(ctx, types.UsageOptions{}); err == nil {
			res[name] = usages
		} else {
			log.Error().Err(err).Str("cli", name).Msg("Failed to check quota for CLI")
			res[name] = []types.ModelUsage{}
		}
	}
	return res, nil
}

func CheckQuota(cli string, model string) float64 {
	client, ok := clients[cli]
	if !ok {
		return 0.0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	usages, err := client.Usage(ctx, types.UsageOptions{})
	if err != nil {
		log.Error().Err(err).Msg("Failed to check quota")
		return 0.0
	}

	for _, u := range usages {
		if u.Model == model {
			return u.Remaining
		}
	}

	return 0.0
}
