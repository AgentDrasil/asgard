package claude

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // reset times carry an IANA zone name; do not depend on the image's tzdata

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// Models returns the models selectable via `claude --model`. claude has no
// model-listing command, so this is a fixed catalog of the stable aliases.
func Models(_ context.Context, _ types.UsageOptions) ([]string, error) {
	return []string{"fable", "opus", "sonnet", "haiku"}, nil
}

// Usage runs claude's `/usage` slash command and parses the subscription
// quota out of its plain-text output. Only subscription accounts are
// supported: for any other account the output has no quota lines and Usage
// returns an error.
func Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	cmd := exec.CommandContext(ctx, "claude", "--print", "--verbose", "--output-format", "stream-json", "--no-session-persistence", "/usage")
	cmd.Env = commandEnv()
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running claude /usage: %w", err)
	}

	text, err := extractResultText(out)
	if err != nil {
		return nil, err
	}
	usage, err := parseUsageText(text, time.Now())
	if err != nil {
		return nil, err
	}

	models, _ := Models(ctx, opts)
	result := make([]types.ModelUsage, 0, len(models))
	for _, m := range models {
		// A model with no applicable tier is left out, so a quota check on
		// it fails closed instead of inheriting another model's numbers.
		if u, ok := buildModelUsage(m, usage, opts.Detailed); ok {
			result = append(result, u)
		}
	}
	return result, nil
}

type usageTier struct {
	name      string
	remaining float64
	refresh   int64
}

type modelTier struct {
	label string // lowercase scope label, e.g. "sonnet only"
	tier  usageTier
}

// parsedUsage holds the account-wide tiers plus model-scoped weekly tiers
// (from rows such as "Current week (Sonnet only)").
type parsedUsage struct {
	general  []usageTier
	perModel []modelTier
}

// buildModelUsage reports the most constrained applicable tier as the
// headline quota. ok is false when no tier applies to the model.
func buildModelUsage(model string, usage *parsedUsage, detailed bool) (types.ModelUsage, bool) {
	tiers := append([]usageTier(nil), usage.general...)
	for _, mt := range usage.perModel {
		if strings.Contains(mt.label, strings.ToLower(model)) {
			tiers = append(tiers, mt.tier)
		}
	}
	if len(tiers) == 0 {
		return types.ModelUsage{}, false
	}

	main := tiers[0]
	for _, t := range tiers[1:] {
		if t.remaining < main.remaining {
			main = t
		}
	}
	u := types.ModelUsage{Model: model, Remaining: main.remaining, RefreshDate: main.refresh}
	if detailed {
		for _, t := range tiers {
			u.Limits = append(u.Limits, types.QuotaLimit{Name: t.name, Remaining: t.remaining, RefreshDate: t.refresh})
		}
	}
	return u, true
}

var (
	sessionUsageRe = regexp.MustCompile(`(?i)Current session:\s*(\d+)% used(?:\s*·\s*resets\s+(.+))?`)
	weekUsageRe    = regexp.MustCompile(`(?i)Current week \(([^)]+)\):\s*(\d+)% used(?:\s*·\s*resets\s+(.+))?`)
)

func newTier(name, used, reset string, now time.Time) usageTier {
	pct, _ := strconv.Atoi(used)
	remaining := 1 - float64(pct)/100
	if remaining < 0 {
		remaining = 0
	}
	return usageTier{name: name, remaining: remaining, refresh: parseResetTime(strings.TrimSpace(reset), now)}
}

// parseUsageText extracts the session (5h) and weekly tiers from /usage text.
// It fails when no tier is present so callers never mistake an unparseable
// output for a full quota.
func parseUsageText(text string, now time.Time) (*parsedUsage, error) {
	p := &parsedUsage{}
	if m := sessionUsageRe.FindStringSubmatch(text); m != nil {
		p.general = append(p.general, newTier("5h", m[1], m[2], now))
	}
	for _, m := range weekUsageRe.FindAllStringSubmatch(text, -1) {
		label := strings.ToLower(strings.TrimSpace(m[1]))
		if label == "all models" {
			p.general = append(p.general, newTier("weekly", m[2], m[3], now))
			continue
		}
		p.perModel = append(p.perModel, modelTier{label: label, tier: newTier("weekly:"+label, m[2], m[3], now)})
	}
	if len(p.general) == 0 && len(p.perModel) == 0 {
		return nil, fmt.Errorf("claude /usage: no quota information found in output %q", strings.TrimSpace(text))
	}
	return p, nil
}

// resetTimeRe matches claude's reset descriptions: "Oct 28, 4:30am (UTC)",
// "Jan 2, 2027, 9am (America/New_York)" (the year appears only when it is not
// the current one) and the time-only "4:30am (UTC)". The zone is an IANA name.
var resetTimeRe = regexp.MustCompile(`^(?:([A-Za-z]{3}) (\d{1,2})(?:,? (\d{4}))?,?\s+)?(\d{1,2})(?::(\d{2}))?\s*((?i:am|pm))(?:\s*\(([^)]+)\))?`)

func resolveZone(name string) (*time.Location, bool) {
	if name == "" {
		return time.UTC, true
	}
	loc, err := time.LoadLocation(name)
	return loc, err == nil
}

// parseResetTime parses a reset description into a unix timestamp. A
// time-only form means the next occurrence of that clock time; a date without
// a year means the nearest non-past date. Returns 0 when it cannot be parsed.
func parseResetTime(s string, now time.Time) int64 {
	m := resetTimeRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	loc, ok := resolveZone(m[7])
	if !ok {
		return 0
	}
	hour, _ := strconv.Atoi(m[4])
	minute, _ := strconv.Atoi(m[5])
	if hour < 1 || hour > 12 || minute > 59 {
		return 0
	}
	hour %= 12
	if strings.EqualFold(m[6], "pm") {
		hour += 12
	}

	local := now.In(loc)
	if m[1] == "" {
		t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
		if t.Before(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t.Unix()
	}

	month, err := time.Parse("Jan", m[1])
	if err != nil {
		return 0
	}
	day, _ := strconv.Atoi(m[2])
	if m[3] != "" {
		year, _ := strconv.Atoi(m[3])
		return time.Date(year, month.Month(), day, hour, minute, 0, 0, loc).Unix()
	}
	t := time.Date(local.Year(), month.Month(), day, hour, minute, 0, 0, loc)
	if t.Before(now.Add(-24 * time.Hour)) {
		t = t.AddDate(1, 0, 0)
	}
	return t.Unix()
}
