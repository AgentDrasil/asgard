// Package agy provides programmatic interaction helpers for the agy CLI tool.
package agy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

type QuotaEntry struct {
	RemainingFraction float64 `json:"remaining_fraction"`
	ResetTime         string  `json:"reset_time"`
	ResetInSeconds    int     `json:"reset_in_seconds"`
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
			parts := strings.SplitN(line, "\t", 2)
			modelID := strings.TrimSpace(parts[0])
			if modelID != "" {
				models = append(models, modelID)
			}
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

// Usage retrieves available models and queries their current quota levels
// by executing agy with --print "/usage".
func Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	models, err := Models(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("fetching available models: %w", err)
	}

	argv := []string{"agy", "--output-format", "json", "--print", "/usage"}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running agy for usage: %w", err)
	}

	quota := make(map[string]QuotaEntry)

	type Bucket struct {
		ID                string  `json:"id"`
		RemainingFraction float64 `json:"remaining_fraction"`
		ResetTime         string  `json:"reset_time"`
	}
	type Group struct {
		Name    string   `json:"name"`
		Buckets []Bucket `json:"buckets"`
	}
	type CommandData struct {
		Groups []Group `json:"groups"`
	}
	type Command struct {
		Name string      `json:"name"`
		Data CommandData `json:"data"`
	}
	type UsageEvent struct {
		Command *Command `json:"command"`
	}

	var ev UsageEvent
	if err := json.Unmarshal(output, &ev); err == nil && ev.Command != nil && ev.Command.Name == "usage" {
		for _, g := range ev.Command.Data.Groups {
			for _, b := range g.Buckets {
				quota[b.ID] = QuotaEntry{
					RemainingFraction: b.RemainingFraction,
					ResetTime:         b.ResetTime,
				}
			}
		}
	}

	result := make([]types.ModelUsage, 0, len(models))
	for _, mName := range models {
		rem, ref := getModelQuota(mName, quota)

		var limits []types.QuotaLimit
		if opts.Detailed {
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
