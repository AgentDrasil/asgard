// Package agy provides programmatic interaction helpers for the agy CLI tool.
package agy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"

	"github.com/AgentDrasil/asgard/lib/term"
)

const (
	termCols uint16 = 220
	termRows uint16 = 50
)

type QuotaEntry struct {
	RemainingFraction float64 `json:"remaining_fraction"`
	ResetTime         string  `json:"reset_time"`
	ResetInSeconds    int     `json:"reset_in_seconds"`
}

type StatuslineQuota struct {
	Quota map[string]QuotaEntry `json:"quota"`
}

func Models(ctx context.Context, opts types.UsageOptions) ([]string, error) {
	cmd := exec.CommandContext(ctx, "agy", "models")
	cmd.Dir = opts.Dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running agy models: %w", err)
	}
	var models []string
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			models = append(models, line)
		}
	}
	return models, nil
}

func getModelQuota(modelName string, quota map[string]QuotaEntry) (remaining float64, refreshDate int64) {
	remaining = 1.0
	refreshDate = 0

	isGemini := strings.HasPrefix(strings.ToLower(strings.TrimSpace(modelName)), "gemini")
	var q5h, qWeekly QuotaEntry
	var has5h, hasWeekly bool

	if isGemini {
		q5h, has5h = quota["gemini-5h"]
		qWeekly, hasWeekly = quota["gemini-weekly"]
	} else {
		q5h, has5h = quota["3p-5h"]
		qWeekly, hasWeekly = quota["3p-weekly"]
	}

	if has5h && hasWeekly {
		if q5h.RemainingFraction < qWeekly.RemainingFraction {
			remaining = q5h.RemainingFraction
			refreshDate = parseResetTime(q5h.ResetTime)
		} else if qWeekly.RemainingFraction < q5h.RemainingFraction {
			remaining = qWeekly.RemainingFraction
			refreshDate = parseResetTime(qWeekly.ResetTime)
		} else {
			remaining = q5h.RemainingFraction
			if remaining < 1.0 {
				t5h := parseResetTime(q5h.ResetTime)
				tWeekly := parseResetTime(qWeekly.ResetTime)
				if t5h > 0 {
					refreshDate = t5h
				} else {
					refreshDate = tWeekly
				}
			} else {
				refreshDate = 0
			}
		}
	} else if has5h {
		remaining = q5h.RemainingFraction
		if remaining < 1.0 {
			refreshDate = parseResetTime(q5h.ResetTime)
		}
	} else if hasWeekly {
		remaining = qWeekly.RemainingFraction
		if remaining < 1.0 {
			refreshDate = parseResetTime(qWeekly.ResetTime)
		}
	}

	return remaining, refreshDate
}

func parseResetTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// Usage launches agy in a headless terminal, retrieves available models via agy models,
// waits for the statusline JSON to report idle, and parses the quota mapping.
//
// The sequence performed is:
//  1. Run `agy models` to fetch the list of available models.
//  2. Open a headless PTY-backed terminal (220×50).
//  3. Launch `agy`.
//  4. Poll the statusline JSON every 200 ms until the statusbar last line's first
//     token is "idle" (or StartupDelay elapses).
//  5. Read the statusline JSON and parse the model quota info.
//  6. Send /exit + Enter to exit cleanly.
//  7. Map each model to its corresponding quota group (gemini or 3p) and return.
func Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	models, err := Models(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("fetching available models: %w", err)
	}

	t := term.NewTerm(termCols, termRows)
	defer t.Close()

	argv := []string{"agy"}

	awSessionID := uuid.NewString()
	done, err := t.RunCommandInDir(context.Background(), argv, opts.Dir, []string{"AW_SESSION_ID=" + awSessionID})
	if err != nil {
		return nil, fmt.Errorf("launching agy/usage: %w", err)
	}

	handleErr := func(err error) error {
		if ctx.Err() != nil {
			CleanExit(t, done)
			return ctx.Err()
		}
		return err
	}

	// Poll until the statusbar last line reports "idle", up to startupDelay.
	log.Debug().Msg("agy/usage: waiting for state=idle")
	timedOut, err := pollUntilIdle(ctx, awSessionID, done, opts.StartupDelayOrDefault())
	if err != nil {
		return nil, handleErr(err)
	}
	if timedOut {
		log.Debug().Msg("agy/usage: startup idle timed out — proceeding anyway")
	} else {
		log.Debug().Msg("agy/usage: state=idle")
	}

	filePath := filepath.Join(statuslineDir, awSessionID+".json")
	var quota map[string]QuotaEntry
	var lastErr error

	for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(100 * time.Millisecond) {
		var jsonData []byte
		jsonData, lastErr = os.ReadFile(filePath)
		if lastErr == nil {
			var sq StatuslineQuota
			if lastErr = json.Unmarshal(jsonData, &sq); lastErr == nil && len(sq.Quota) > 0 {
				quota = sq.Quota
				break
			}
		}
	}
	if len(quota) == 0 {
		log.Warn().Err(lastErr).Msg("failed to read or parse populated statusline JSON for quota")
	}

	// Exit: /exit + Enter.
	CleanExit(t, done)

	result := make([]types.ModelUsage, 0, len(models))
	for _, mName := range models {
		rem, ref := getModelQuota(mName, quota)

		var limits []types.QuotaLimit
		isGemini := strings.HasPrefix(strings.ToLower(strings.TrimSpace(mName)), "gemini")
		var q5h, qWeekly QuotaEntry
		var has5h, hasWeekly bool

		if isGemini {
			q5h, has5h = quota["gemini-5h"]
			qWeekly, hasWeekly = quota["gemini-weekly"]
		} else {
			q5h, has5h = quota["3p-5h"]
			qWeekly, hasWeekly = quota["3p-weekly"]
		}

		if has5h {
			limits = append(limits, types.QuotaLimit{
				Name:        "5h",
				Remaining:   q5h.RemainingFraction,
				RefreshDate: parseResetTime(q5h.ResetTime),
			})
		}
		if hasWeekly {
			limits = append(limits, types.QuotaLimit{
				Name:        "weekly",
				Remaining:   qWeekly.RemainingFraction,
				RefreshDate: parseResetTime(qWeekly.ResetTime),
			})
		}

		result = append(result, types.ModelUsage{
			Model:       mName,
			Remaining:   rem,
			RefreshDate: ref,
			Limits:      limits,
		})
	}

	return result, nil
}
