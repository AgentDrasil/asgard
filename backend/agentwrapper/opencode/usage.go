package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/agentwrapper/types"
)

type zaiQuotaResponse struct {
	Success bool   `json:"success"`
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Data    struct {
		Limits []struct {
			Type          string  `json:"type"`
			Usage         float64 `json:"usage"`
			Remaining     float64 `json:"remaining"`
			Percentage    float64 `json:"percentage"`
			NextResetTime int64   `json:"nextResetTime"`
		} `json:"limits"`
	} `json:"data"`
}

func loadZaiToken() string {
	if t := os.Getenv("ZAI_TOKEN"); t != "" {
		return t
	}
	if t := os.Getenv("ZAI_API_TOKEN"); t != "" {
		return t
	}
	if t := os.Getenv("ZAI_API_KEY"); t != "" {
		return t
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	var auth struct {
		ZaiCodingPlan struct {
			Key string `json:"key"`
		} `json:"zai-coding-plan"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return ""
	}
	return auth.ZaiCodingPlan.Key
}

// Models runs "opencode models", parses the list of models, and returns them.
func Models(ctx context.Context, opts types.UsageOptions) ([]string, error) {
	cmd := exec.CommandContext(ctx, "opencode", "models")
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running opencode models: %w", err)
	}

	var result []string
	lines := strings.Split(out.String(), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
	}
	return result, nil
}

// Usage runs "opencode models", parses the list of models, and returns a ModelUsage list with Remaining = 1.0.
func Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	models, err := Models(ctx, opts)
	if err != nil {
		return nil, err
	}

	var result []types.ModelUsage
	for _, m := range models {
		result = append(result, types.ModelUsage{
			Model:     m,
			Remaining: 1.0,
		})
	}

	var hasZai bool
	for _, m := range result {
		if strings.HasPrefix(m.Model, "zai-coding-plan") {
			hasZai = true
			break
		}
	}

	if hasZai {
		token := loadZaiToken()
		if token != "" {
			log.Debug().Msg("fetching zai quota from API")
			req, err := http.NewRequestWithContext(ctx, "GET", "https://api.z.ai/api/monitor/usage/quota/limit", nil)
			if err != nil {
				log.Debug().Err(err).Msg("failed to create http request for zai quota")
			} else {
				req.Header.Set("Authorization", "Bearer "+token)
				client := &http.Client{Timeout: 10 * time.Second}
				resp, err := client.Do(req)
				if err != nil {
					log.Debug().Err(err).Msg("failed to execute http request for zai quota")
				} else {
					defer func() { _ = resp.Body.Close() }()
					var qr zaiQuotaResponse
					if err := json.NewDecoder(resp.Body).Decode(&qr); err != nil {
						log.Debug().Err(err).Msg("failed to decode zai quota response JSON")
					} else if !qr.Success && qr.Code != 200 && (qr.Code != 0 || len(qr.Data.Limits) == 0) {
						log.Debug().Int("code", qr.Code).Str("msg", qr.Msg).Msg("zai quota API returned failure")
					} else {
						remainingVal, refreshDate, limits := parseZaiLimits(qr.Data.Limits, opts.Detailed)
						log.Debug().Float64("remaining", remainingVal).Int64("refresh_date", refreshDate).Msg("successfully fetched zai quota limit")

						// If we fetched limits, apply them to the matching model(s)
						for i := range result {
							if strings.HasPrefix(result[i].Model, "zai-coding-plan") {
								result[i].Remaining = remainingVal
								result[i].RefreshDate = refreshDate
								if opts.Detailed {
									result[i].Limits = limits
								}
							}
						}
					}
				}
			}
		} else {
			log.Debug().Msg("zai token not found, skipping quota fetch")
		}
	}

	return result, nil
}

func parseZaiLimits(limitsData []struct {
	Type          string  `json:"type"`
	Usage         float64 `json:"usage"`
	Remaining     float64 `json:"remaining"`
	Percentage    float64 `json:"percentage"`
	NextResetTime int64   `json:"nextResetTime"`
}, detailed bool) (float64, int64, []types.QuotaLimit) {
	var remainingVal = 1.0
	var refreshDate int64 = 0
	var foundTokensLimit bool
	var foundAnyLimit bool

	var limits []types.QuotaLimit
	for _, limit := range limitsData {
		remVal := 1.0 - (limit.Percentage / 100.0)
		if remVal < 0 {
			remVal = 0
		} else if remVal > 1.0 {
			remVal = 1.0
		}
		refDate := limit.NextResetTime / 1000

		if detailed {
			limits = append(limits, types.QuotaLimit{
				Name:        limit.Type,
				Remaining:   remVal,
				RefreshDate: refDate,
			})
		}

		if limit.Type == "TOKENS_LIMIT" {
			if !foundTokensLimit || remVal < remainingVal {
				remainingVal = remVal
				refreshDate = refDate
				foundTokensLimit = true
			}
		} else if !foundTokensLimit && !foundAnyLimit {
			remainingVal = remVal
			refreshDate = refDate
			foundAnyLimit = true
		}
	}

	return remainingVal, refreshDate, limits
}
