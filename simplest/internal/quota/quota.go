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

// loadZaiCodingPlanProvider returns the API key and base URL configured for
// the zai-coding-plan provider (either may be empty when unconfigured).
func loadZaiCodingPlanProvider(cfg *config.Config) (apiKey, baseURL string) {
	if cfg != nil {
		if prov, ok := cfg.Providers["zai-coding-plan"]; ok {
			return prov.APIKey, prov.BaseURL
		}
	}
	return "", ""
}

// zaiQuotaEndpoint derives the quota endpoint URL from a provider base URL.
// It returns an empty string when no base URL is configured, letting the
// caller fall back to the default public endpoint.
func zaiQuotaEndpoint(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	return base + "/api/monitor/usage/quota/limit"
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
	var hasDeepSeek bool
	for _, m := range available {
		if isZaiCodingPlan(m.Provider, m.ID) {
			hasZaiCodingPlan = true
		} else if isDeepSeek(m.Provider, m.ID) {
			hasDeepSeek = true
		}
	}

	if hasZaiCodingPlan {
		token, baseURL := loadZaiCodingPlanProvider(cfg)
		if token != "" {
			// Explicit endpoint argument wins; otherwise a provider base URL
			// override takes precedence over the default public endpoint.
			endpointArgs := endpoint
			if len(endpointArgs) == 0 || endpointArgs[0] == "" {
				if ep := zaiQuotaEndpoint(baseURL); ep != "" {
					endpointArgs = []string{ep}
				}
			}
			rem, ref, limits, err := zai.FetchQuota(ctx, token, opts.Detailed, endpointArgs...)
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

	// DeepSeek is a prepaid (pay-as-you-go) provider: its "quota" is the
	// account balance reported by GET /user/balance. Fetch it once per
	// provider and attach it to every model backed by that account.
	if hasDeepSeek {
		apiKey, baseURL := deepSeekEndpoint(cfg)
		if apiKey != "" {
			balance, err := fetchDeepSeekBalance(ctx, apiKey, baseURL)
			if err == nil && balance != nil {
				remaining := 0.0
				if balance.IsAvailable {
					remaining = 1.0
					// Low-balance guard: surface a 20% "remaining" warning so
					// the UI reminds the user to top up the account.
					if isBalanceLow(balance) {
						remaining = lowBalanceRemaining
					}
				}
				for i, m := range available {
					if isDeepSeek(m.Provider, m.ID) {
						result[i].Balance = balance
						result[i].Remaining = remaining
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
