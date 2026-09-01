package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/llms/zai"
)

func loadAuthToken() string {
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

func isZaiCodingPlan(model string) bool {
	m := strings.ToLower(model)
	return m == "zai-coding-plan" || strings.HasPrefix(m, "zai-coding-plan/")
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

// Usage runs "opencode models", parses the list of models, and returns a ModelUsage list.
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

	var hasZaiCodingPlan bool
	for _, m := range result {
		if isZaiCodingPlan(m.Model) {
			hasZaiCodingPlan = true
			break
		}
	}

	if hasZaiCodingPlan {
		token := loadAuthToken()
		if token != "" {
			log.Debug().Msg("fetching zai quota from API")
			remainingVal, refreshDate, limits, err := zai.FetchQuota(ctx, token, opts.Detailed)
			if err != nil {
				log.Debug().Err(err).Msg("failed to fetch zai quota")
			} else {
				log.Debug().Float64("remaining", remainingVal).Int64("refresh_date", refreshDate).Msg("successfully fetched zai quota limit")
				for i := range result {
					if isZaiCodingPlan(result[i].Model) {
						result[i].Remaining = remainingVal
						result[i].RefreshDate = refreshDate
						if opts.Detailed {
							result[i].Limits = limits
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
