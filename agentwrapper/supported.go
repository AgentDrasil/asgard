package agentwrapper

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper/agy"
	"github.com/AgentDrasil/asgard/agentwrapper/claude"
	"github.com/AgentDrasil/asgard/agentwrapper/config"
	"github.com/AgentDrasil/asgard/agentwrapper/opencode"
	"github.com/AgentDrasil/asgard/agentwrapper/simplest"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

var defaultClients = map[string]types.CLIClient{
	"agy":      &agy.Client{},
	"claude":   &claude.Client{},
	"opencode": &opencode.Client{},
	"simplest": &simplest.Client{},
}

// clientsMu guards clients. The map itself is never mutated in place, overrides
// replace it wholesale, but parallel tests override it concurrently.
var (
	clientsMu sync.RWMutex
	clients   = defaultClients
)

// SetClients overrides the CLI clients. Passing nil restores the defaults.
func SetClients(c map[string]types.CLIClient) {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	if c == nil {
		clients = defaultClients
	} else {
		clients = c
	}
}

// registeredClients returns the clients currently in effect. Callers may
// iterate the result freely: overrides replace the map instead of mutating it.
func registeredClients() map[string]types.CLIClient {
	clientsMu.RLock()
	defer clientsMu.RUnlock()

	return clients
}

// QueryRunner runs `aw <args...>` somewhere safe (e.g. inside the agent
// sandbox) and returns its stdout.
type QueryRunner func(ctx context.Context, args ...string) ([]byte, error)

var (
	queryRunnerMu sync.RWMutex
	queryRunner   QueryRunner
)

// SetQueryRunner makes models and usage queries go through r instead of
// invoking the vendor CLIs in-process. Vendor CLIs (agy, claude) implement
// these queries with non-interactive prompts, which can fire their hooks, so
// the host sets a runner that confines them to the sandbox. Passing nil
// restores in-process queries.
func SetQueryRunner(r QueryRunner) {
	queryRunnerMu.Lock()
	defer queryRunnerMu.Unlock()

	queryRunner = r
}

func currentQueryRunner() QueryRunner {
	queryRunnerMu.RLock()
	defer queryRunnerMu.RUnlock()

	return queryRunner
}

// runQuery runs `aw <args...>` through the query runner and decodes its JSON
// output into out. ok is false when no runner is set.
func runQuery(ctx context.Context, out any, args ...string) (ok bool, err error) {
	run := currentQueryRunner()
	if run == nil {
		return false, nil
	}
	raw, err := run(ctx, args...)
	if err != nil {
		return true, err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return true, fmt.Errorf("parsing aw %v output: %w", args, err)
	}
	return true, nil
}

func GetSupportedCLIsAndModels() map[string][]string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The sandboxed aw applies its own 10s per-CLI timeout after sandbox setup,
	// so the host budget must be larger or one slow CLI would discard every
	// CLI's models. --unfiltered: the host validates against the full catalog,
	// not the aw config allow-list.
	qctx, qcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer qcancel()
	var sandboxed map[string][]string
	if ok, err := runQuery(qctx, &sandboxed, "models", "--unfiltered"); ok {
		if err != nil {
			log.Error().Err(err).Msg("Failed to list models in sandbox")
			sandboxed = nil
		}
		res := make(map[string][]string, len(registeredClients()))
		for name := range registeredClients() {
			res[name] = sandboxed[name]
			if res[name] == nil {
				res[name] = []string{}
			}
		}
		return res
	}

	res := make(map[string][]string)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for name, client := range registeredClients() {
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
	var res map[string][]types.ModelUsage
	if ok, err := runQuery(ctx, &res, "usage"); ok {
		if err != nil {
			return nil, fmt.Errorf("fetching quota in sandbox: %w", err)
		}
		// The query sandbox mounts the aw config, so aw already filtered.
		return res, nil
	}

	res = make(map[string][]types.ModelUsage)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for name, client := range registeredClients() {
		wg.Add(1)
		go func(name string, client types.CLIClient) {
			defer wg.Done()
			var usages []types.ModelUsage
			if u, err := client.Usage(ctx, types.UsageOptions{Detailed: true}); err == nil {
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

	return filterQuota(res), nil
}

// filterQuota drops models the aw config does not allow.
func filterQuota(res map[string][]types.ModelUsage) map[string][]types.ModelUsage {
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

	return res
}

// GetCLIQuota returns the top-level quota of a single CLI keyed by its name,
// without the sandbox query runner. It backs `aw usage --cli`.
func GetCLIQuota(ctx context.Context, cli string) (map[string][]types.ModelUsage, error) {
	client, ok := registeredClients()[cli]
	if !ok {
		return nil, fmt.Errorf("unknown cli %q", cli)
	}
	usages, err := client.Usage(ctx, types.UsageOptions{})
	if err != nil {
		return nil, err
	}
	return map[string][]types.ModelUsage{cli: usages}, nil
}

func CheckQuota(cli string, model string) float64 {
	client, ok := registeredClients()[cli]
	if !ok {
		return 0.0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var usages []types.ModelUsage
	var err error
	all := map[string][]types.ModelUsage{}
	if ok, qerr := runQuery(ctx, &all, "usage", "--cli", cli); ok {
		usages, err = all[cli], qerr
	} else {
		usages, err = client.Usage(ctx, types.UsageOptions{})
	}
	if err != nil {
		log.Error().Err(err).Msg("Failed to check quota")
		return 0.0
	}

	for _, u := range usages {
		if MatchesModel(cli, model, u.Model) {
			return u.Remaining
		}
	}

	return 0.0
}

// GetSandboxSpec returns the SandboxSpec for the given CLI if implemented, or nil.
func GetSandboxSpec(cli string) types.SandboxSpec {
	if client, ok := registeredClients()[cli]; ok {
		if spec, ok := client.(types.SandboxSpec); ok {
			return spec
		}
	}
	return nil
}

// GetRegisteredCLIs returns all registered CLI names.
func GetRegisteredCLIs() []string {
	registered := registeredClients()
	names := make([]string, 0, len(registered))
	for name := range registered {
		names = append(names, name)
	}
	return names
}
