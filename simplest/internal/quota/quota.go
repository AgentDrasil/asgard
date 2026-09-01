// Package quota provides quota and model usage inspection for simplest.
package quota

import (
	"context"
	"strings"

	"github.com/AgentDrasil/asgard/llms/zai"
	"github.com/AgentDrasil/asgard/simplest/internal/config"
	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// LoadZaiCodingPlanToken resolves the Z.AI Coding Plan token from the provided configuration.
func LoadZaiCodingPlanToken(cfg *config.Config) string {
	if cfg != nil {
		if prov, ok := cfg.Providers["zai-coding-plan"]; ok && prov.APIKey != "" {
			return prov.APIKey
		}
	}
	return ""
}

func isZaiCodingPlan(provider, modelID string) bool {
	p := strings.ToLower(provider)
	if p == "zai-coding-plan" {
		return true
	}
	id := strings.ToLower(modelID)
	return id == "zai-coding-plan" || strings.HasPrefix(id, "zai-coding-plan/")
}

// GetModelUsages computes quota and usage for all configured models in cfg.
func GetModelUsages(ctx context.Context, cfg *config.Config, opts types.UsageOptions, endpoint ...string) ([]types.ModelUsage, error) {
	if cfg == nil {
		return nil, nil
	}

	available := cfg.GetAvailableModels()
	result := make([]types.ModelUsage, 0, len(available))

	for _, m := range available {
		result = append(result, types.ModelUsage{
			Model:     config.FullModelName(m.Provider, m.ID),
			Remaining: 1.0,
		})
	}

	var hasZaiCodingPlan bool
	for _, m := range available {
		if isZaiCodingPlan(m.Provider, m.ID) {
			hasZaiCodingPlan = true
			break
		}
	}

	if hasZaiCodingPlan {
		token := LoadZaiCodingPlanToken(cfg)
		if token != "" {
			rem, ref, limits, err := zai.FetchQuota(ctx, token, opts.Detailed, endpoint...)
			if err == nil {
				for i, m := range available {
					if isZaiCodingPlan(m.Provider, m.ID) {
						result[i].Remaining = rem
						result[i].RefreshDate = ref
						if opts.Detailed {
							result[i].Limits = limits
						}
					}
				}
			}
		}
	}

	return result, nil
}

// GetModelUsagesFromGlobal gets model usages from the global configuration singleton.
func GetModelUsagesFromGlobal(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	cfg, err := config.GetGlobalConfig()
	if err != nil {
		return nil, err
	}
	return GetModelUsages(ctx, cfg, opts)
}
