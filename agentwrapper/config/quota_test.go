package config

import (
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/llms"
)

func claudeUsage(five, weekly float64) llms.ModelUsage {
	return llms.ModelUsage{
		Model:     "claude",
		Remaining: min(five, weekly),
		Limits: []llms.QuotaLimit{
			{Name: "5h", Remaining: five},
			{Name: "weekly", Remaining: weekly},
		},
	}
}

func TestQuotaProfileEvaluate_PerWindowThresholds(t *testing.T) {
	pro := QuotaProfile{Thresholds: map[string]float64{"5h": 0.10, "weekly": 0.05}}

	tests := []struct {
		name         string
		five, weekly float64
		usable       bool
		reason       string
	}{
		{"both above", 0.50, 0.50, true, ""},
		{"weekly at 8% is fine under a 5% weekly threshold", 0.50, 0.08, true, ""},
		{"5h below its threshold", 0.09, 0.50, false, "5h"},
		{"weekly below its threshold", 0.50, 0.04, false, "weekly"},
		{"exactly at threshold is unusable", 0.10, 0.50, false, "5h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usable, reason := pro.Evaluate(claudeUsage(tt.five, tt.weekly))
			assert.Equal(t, tt.usable, usable)
			if tt.reason == "" {
				assert.Empty(t, reason)
			} else {
				assert.Contains(t, reason, tt.reason)
			}
		})
	}
}

func TestQuotaProfileEvaluate_FallbacksAndGlobs(t *testing.T) {
	p := QuotaProfile{Thresholds: map[string]float64{"*": 0.10, "weekly*": 0.02}}

	usable, _ := p.Evaluate(llms.ModelUsage{Limits: []llms.QuotaLimit{{Name: "weekly:sonnet only", Remaining: 0.03}}})
	assert.True(t, usable, "glob threshold applies")

	usable, _ = p.Evaluate(llms.ModelUsage{Limits: []llms.QuotaLimit{{Name: "daily", Remaining: 0.09}}})
	assert.False(t, usable, "star fallback applies")

	usable, _ = p.Evaluate(llms.ModelUsage{Remaining: 0.09})
	assert.False(t, usable, "no limits: headline checked against star")

	empty := QuotaProfile{}
	usable, _ = empty.Evaluate(llms.ModelUsage{Limits: []llms.QuotaLimit{{Name: "5h", Remaining: 0}}})
	assert.False(t, usable, "no threshold still rejects an exhausted limit")
	usable, _ = empty.Evaluate(llms.ModelUsage{Limits: []llms.QuotaLimit{{Name: "5h", Remaining: 0.01}}})
	assert.True(t, usable)
}

func TestQuotaProfileEvaluate_Unlimited(t *testing.T) {
	usable, _ := QuotaProfile{Unlimited: true}.Evaluate(llms.ModelUsage{Remaining: 0})
	assert.True(t, usable)
}

func TestApplyQuotaPolicy(t *testing.T) {
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte(`
quota_profiles:
  pro20:
    thresholds: {5h: 0.10, weekly: 0.05}
  default:
    thresholds: {"*": 0.10}
`), &cfg))

	usages := []llms.ModelUsage{claudeUsage(0.50, 0.08), llms.StatusUsage(assert.AnError)}
	cfg.ApplyQuotaPolicy("pro20", usages)
	require.NotNil(t, usages[0].Usable)
	assert.True(t, *usages[0].Usable)
	assert.Nil(t, usages[1].Usable, "status-only entries are left alone")

	// An unbound or unknown profile falls back to default.
	usages = []llms.ModelUsage{claudeUsage(0.50, 0.08)}
	cfg.ApplyQuotaPolicy("", usages)
	require.NotNil(t, usages[0].Usable)
	assert.False(t, *usages[0].Usable)
	assert.NotEmpty(t, usages[0].Reason)

	// No profiles configured: no verdict, callers keep their own thresholds.
	usages = []llms.ModelUsage{claudeUsage(0.01, 0.01)}
	(&Config{}).ApplyQuotaPolicy("pro20", usages)
	assert.Nil(t, usages[0].Usable)
	(*Config)(nil).ApplyQuotaPolicy("pro20", usages)
	assert.Nil(t, usages[0].Usable)
}

func TestQuotaProfileThreshold_MostSpecificGlobWins(t *testing.T) {
	p := QuotaProfile{Thresholds: map[string]float64{"*": 0.50, "5*": 0.20, "5h*": 0.10, "weekly*": 0.05}}
	assert.InDelta(t, 0.10, p.threshold("5h"), 1e-9, "5h* is more specific than 5*, though it sorts after it")
	assert.InDelta(t, 0.20, p.threshold("5d"), 1e-9)
	assert.InDelta(t, 0.05, p.threshold("weekly:sonnet only"), 1e-9)
	assert.InDelta(t, 0.50, p.threshold("daily"), 1e-9)
}

// One profile serves every model of an account, including models that lack a
// window (agy lists a limit only for the windows a model has): only the
// windows that exist are tested.
func TestApplyQuotaPolicy_OnlyTestsWindowsTheModelHas(t *testing.T) {
	cfg := &Config{QuotaProfiles: map[string]QuotaProfile{
		"agy-20": {Thresholds: map[string]float64{"5h": 0.10, "weekly": 0.05}},
	}}
	lim := func(name string, r float64) llms.QuotaLimit { return llms.QuotaLimit{Name: name, Remaining: r} }
	usages := []llms.ModelUsage{
		{Model: "both-ok", Limits: []llms.QuotaLimit{lim("5h", 0.50), lim("weekly", 0.50)}},
		{Model: "both-5h-low", Limits: []llms.QuotaLimit{lim("5h", 0.09), lim("weekly", 0.50)}},
		{Model: "weekly-only-ok", Limits: []llms.QuotaLimit{lim("weekly", 0.06)}},
		{Model: "weekly-only-low", Limits: []llms.QuotaLimit{lim("weekly", 0.04)}},
		{Model: "5h-only-ok", Limits: []llms.QuotaLimit{lim("5h", 0.11)}},
		{Model: "5h-only-low", Limits: []llms.QuotaLimit{lim("5h", 0.10)}},
	}
	cfg.ApplyQuotaPolicy("agy-20", usages)

	want := map[string]bool{
		"both-ok": true, "both-5h-low": false,
		"weekly-only-ok": true, "weekly-only-low": false,
		"5h-only-ok": true, "5h-only-low": false,
	}
	for _, u := range usages {
		require.NotNil(t, u.Usable, u.Model)
		assert.Equal(t, want[u.Model], *u.Usable, "%s: %s", u.Model, u.Reason)
	}
	assert.Contains(t, usages[3].Reason, "weekly", "the reason names the window that failed")
}
