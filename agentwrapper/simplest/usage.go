package simplest

import (
	"context"
	"strings"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/simplest"
)

func fullModelName(provider, id string) string {
	if provider == "" {
		return id
	}
	if strings.HasPrefix(strings.ToLower(id), strings.ToLower(provider)+"/") {
		return id
	}
	return provider + "/" + id
}

// Models returns the list of available models from simplest formatted as provider/model.
func Models(ctx context.Context, opts types.UsageOptions) ([]string, error) {
	models, err := simplest.GetAvailableModels()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(models))
	for _, m := range models {
		result = append(result, fullModelName(m.Provider, m.ID))
	}
	return result, nil
}

// Usage returns the ModelUsage list for all available models from simplest.
func Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	return simplest.GetModelUsages(ctx, simplest.UsageOptions{Detailed: opts.Detailed})
}
