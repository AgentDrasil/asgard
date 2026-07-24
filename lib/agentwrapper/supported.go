package agentwrapper

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/cmd/aw/config"
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
	var mu sync.Mutex
	var wg sync.WaitGroup

	for name, client := range clients {
		wg.Add(1)
		go func(name string, client types.CLIClient) {
			defer wg.Done()
			var models []string
			if m, err := client.Models(ctx, types.UsageOptions{}); err == nil {
				models = m
			} else {
				models = []string{}
			}
			mu.Lock()
			res[name] = models
			mu.Unlock()
		}(name, client)
	}

	wg.Wait()
	return res
}

func GetQuota(ctx context.Context) (map[string][]types.ModelUsage, error) {
	res := make(map[string][]types.ModelUsage)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for name, client := range clients {
		wg.Add(1)
		go func(name string, client types.CLIClient) {
			defer wg.Done()
			var usages []types.ModelUsage
			if u, err := client.Usage(ctx, types.UsageOptions{}); err == nil {
				usages = u
			} else {
				log.Error().Err(err).Str("cli", name).Msg("Failed to check quota for CLI")
				usages = []types.ModelUsage{}
			}
			mu.Lock()
			res[name] = usages
			mu.Unlock()
		}(name, client)
	}

	wg.Wait()

	cfg, err := config.LoadConfig()
	if err != nil {
		log.Error().Err(err).Msg("Failed to load aw config for quota filtering")
	}

	for agentName, usages := range res {
		filtered := make([]types.ModelUsage, 0, len(usages))
		for _, u := range usages {
			if cfg.IsModelAllowed(agentName, u.Model) {
				filtered = append(filtered, u)
			}
		}
		res[agentName] = filtered
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
