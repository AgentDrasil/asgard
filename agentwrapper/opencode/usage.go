package opencode

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	_ "modernc.org/sqlite"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/llms/zai"
)

func loadAuthToken() string {
	// 1. Check opencode.aw.yaml
	if env, err := LoadEnv(); err == nil && env != nil {
		for _, key := range []string{"ZAI_API_KEY", "ZHIPU_API_KEY", "OPENCODE_ZAI_API_KEY"} {
			if val := strings.TrimSpace(env[key]); val != "" {
				return val
			}
		}
	}

	// 2. Check os.Getenv
	for _, key := range []string{"ZAI_API_KEY", "ZHIPU_API_KEY", "OPENCODE_ZAI_API_KEY"} {
		if val := strings.TrimSpace(os.Getenv(key)); val != "" {
			return val
		}
	}

	// 3. Fall back to reading ~/.local/share/opencode/opencode.db
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dbPath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if _, err := os.Stat(dbPath); err == nil {
		dsn := fmt.Sprintf("file:%s?mode=ro", dbPath)
		db, err := sql.Open("sqlite", dsn)
		if err == nil {
			defer func() { _ = db.Close() }()
			var rawVal string
			query := `SELECT value FROM credential WHERE integration_id = 'zai-coding-plan' AND (active = 1 OR active IS NULL) ORDER BY time_updated DESC LIMIT 1`
			if err := db.QueryRow(query).Scan(&rawVal); err == nil {
				var cred struct {
					Key string `json:"key"`
				}
				if err := json.Unmarshal([]byte(rawVal), &cred); err == nil && cred.Key != "" {
					return cred.Key
				}
			}
		}
	}
	return ""
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
	if err := injectOpencodeEnv(cmd); err != nil {
		return nil, fmt.Errorf("injecting opencode environment: %w", err)
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
