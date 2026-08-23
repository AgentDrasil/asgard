package simplest

import (
	"context"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/simplest"
)

// Models returns the list of available whitelisted models from simplest.
func Models(ctx context.Context, opts types.UsageOptions) ([]string, error) {
	models, err := simplest.GetAvailableModels()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(models))
	for _, m := range models {
		result = append(result, m.ID)
	}
	return result, nil
}

// Usage returns the ModelUsage list for all available whitelisted models with Remaining = 1.0.
func Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	models, err := Models(ctx, opts)
	if err != nil {
		return nil, err
	}
	result := make([]types.ModelUsage, 0, len(models))
	for _, m := range models {
		result = append(result, types.ModelUsage{
			Model:     m,
			Remaining: 1.0,
		})
	}
	return result, nil
}
