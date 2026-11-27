package agentwrapper

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/llms"
)

func boolPtr(v bool) *bool { return &v }

func TestUsageExhausted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		usage types.ModelUsage
		want  bool
	}{
		{"usable true overrides zero remaining", types.ModelUsage{Remaining: 0, Usable: boolPtr(true)}, false},
		{"usable false overrides high remaining", types.ModelUsage{Remaining: 0.95, Usable: boolPtr(false)}, true},
		{"usable true overrides exhausted limit", types.ModelUsage{Remaining: 0.9, Limits: []types.QuotaLimit{{Name: "5h", Remaining: 0}}, Usable: boolPtr(true)}, false},
		{"no verdict positive remaining no limits", types.ModelUsage{Remaining: 0.01}, false},
		{"no verdict zero remaining", types.ModelUsage{Remaining: 0}, true},
		{"no verdict negative remaining", types.ModelUsage{Remaining: -0.1}, true},
		{"no verdict one exhausted limit", types.ModelUsage{Remaining: 0.8, Limits: []types.QuotaLimit{{Name: "5h", Remaining: 0.8}, {Name: "weekly", Remaining: 0}}}, true},
		{"no verdict all limits positive", types.ModelUsage{Remaining: 0.8, Limits: []types.QuotaLimit{{Name: "5h", Remaining: 0.5}, {Name: "weekly", Remaining: 0.2}}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, UsageExhausted(tt.usage))
		})
	}
}

func TestModelAvailability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cli         string
		usages      []types.ModelUsage
		model       string
		wantAvail   bool
		wantRefresh int64
	}{
		{
			name:      "no matching entry counts as available",
			cli:       "agy",
			usages:    []types.ModelUsage{{Model: "other", Remaining: 0}},
			model:     "gemini-3",
			wantAvail: true,
		},
		{
			name:      "exact match with remaining quota",
			cli:       "agy",
			usages:    []types.ModelUsage{{Model: "gemini-3", Remaining: 0.5}},
			model:     "gemini-3",
			wantAvail: true,
		},
		{
			name:      "case-insensitive and provider-stripped match",
			cli:       "agy",
			usages:    []types.ModelUsage{{Model: "gemini/gemini-4", Remaining: 0.2}},
			model:     "google/GEMINI-4",
			wantAvail: true,
		},
		{
			name:        "claude cli matches opus request against claude usage",
			cli:         "claude",
			usages:      []types.ModelUsage{{Model: "claude", Remaining: 0, RefreshDate: 200}},
			model:       "opus",
			wantAvail:   false,
			wantRefresh: 200,
		},
		{
			name: "exhausted picks earliest positive refresh",
			cli:  "agy",
			usages: []types.ModelUsage{
				{Model: "m", Remaining: 0, RefreshDate: 300},
				{Model: "m", Remaining: 0, RefreshDate: 0},
				{Model: "m", Remaining: 0, RefreshDate: 150},
			},
			model:       "m",
			wantAvail:   false,
			wantRefresh: 150,
		},
		{
			name: "one usable entry among exhausted entries wins",
			cli:  "agy",
			usages: []types.ModelUsage{
				{Model: "m", Remaining: 0, RefreshDate: 100},
				{Model: "m", Remaining: 0.4},
			},
			model:     "m",
			wantAvail: true,
		},
		{
			name:        "exhausted without refresh date returns 0",
			cli:         "agy",
			usages:      []types.ModelUsage{{Model: "m", Remaining: 0}},
			model:       "m",
			wantAvail:   false,
			wantRefresh: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			avail, refresh := ModelAvailability(tt.cli, tt.usages, tt.model)
			assert.Equal(t, tt.wantAvail, avail)
			assert.Equal(t, tt.wantRefresh, refresh)
		})
	}
}

func TestSelectAccount(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")

	tests := []struct {
		name           string
		cli            string
		model          string
		pinned         string
		accounts       []AccountQuotaState
		wantOK         bool
		wantDegraded   bool
		wantAlias      string
		wantReasonPart string
	}{
		{
			name:           "pinned override wins over everything",
			cli:            "agy",
			model:          "m",
			pinned:         "b",
			accounts:       []AccountQuotaState{{Alias: "a", Usages: []types.ModelUsage{{Model: "m", Remaining: 1}}}},
			wantOK:         true,
			wantAlias:      "b",
			wantReasonPart: "pinned",
		},
		{
			name:           "no accounts configured",
			cli:            "agy",
			model:          "m",
			accounts:       nil,
			wantOK:         false,
			wantReasonPart: "no accounts",
		},
		{
			name:           "single account chosen regardless of usage",
			cli:            "agy",
			model:          "m",
			accounts:       []AccountQuotaState{{Alias: "only"}},
			wantOK:         true,
			wantAlias:      "only",
			wantReasonPart: "single account",
		},
		{
			name:  "first available account in order wins",
			cli:   "agy",
			model: "m",
			accounts: []AccountQuotaState{
				{Alias: "a", Usages: []types.ModelUsage{{Model: "m", Remaining: 0}}},
				{Alias: "b", Usages: []types.ModelUsage{{Model: "m", Remaining: 0.2}}},
				{Alias: "c", Usages: []types.ModelUsage{{Model: "m", Remaining: 0.9}}},
			},
			wantOK:         true,
			wantAlias:      "b",
			wantReasonPart: "remaining quota",
		},
		{
			name:  "claude cli matches opus against claude usage",
			cli:   "claude",
			model: "opus",
			accounts: []AccountQuotaState{
				{Alias: "a", Usages: []types.ModelUsage{{Model: "claude", Remaining: 0, RefreshDate: 100}}},
				{Alias: "b", Usages: []types.ModelUsage{{Model: "claude", Remaining: 0.5}}},
			},
			wantOK:         true,
			wantAlias:      "b",
			wantReasonPart: "remaining quota",
		},
		{
			name:  "policy verdict overrides raw remaining",
			cli:   "agy",
			model: "m",
			accounts: []AccountQuotaState{
				{Alias: "a", Usages: []types.ModelUsage{{Model: "m", Remaining: 0.8, Usable: boolPtr(false)}}},
				{Alias: "b", Usages: []types.ModelUsage{{Model: "m", Remaining: 0.04, Usable: boolPtr(true)}}},
			},
			wantOK:    true,
			wantAlias: "b",
		},
		{
			name:  "errored account skipped when later account has quota",
			cli:   "agy",
			model: "m",
			accounts: []AccountQuotaState{
				{Alias: "a", Err: boom},
				{Alias: "b", Usages: []types.ModelUsage{{Model: "m", Remaining: 0.5}}},
			},
			wantOK:    true,
			wantAlias: "b",
		},
		{
			name:  "errored account with others exhausted falls back to first",
			cli:   "agy",
			model: "m",
			accounts: []AccountQuotaState{
				{Alias: "a", Usages: []types.ModelUsage{{Model: "m", Remaining: 0, RefreshDate: 10}}},
				{Alias: "b", Err: boom},
			},
			wantOK:         true,
			wantDegraded:   true,
			wantAlias:      "a",
			wantReasonPart: "some quota unknown",
		},
		{
			name:  "all errored falls back to first",
			cli:   "agy",
			model: "m",
			accounts: []AccountQuotaState{
				{Alias: "a", Err: boom},
				{Alias: "b", Err: boom},
			},
			wantOK:         true,
			wantDegraded:   true,
			wantAlias:      "a",
			wantReasonPart: "some quota unknown",
		},
		{
			name:  "all exhausted picks earliest positive refresh",
			cli:   "agy",
			model: "m",
			accounts: []AccountQuotaState{
				{Alias: "a", Usages: []types.ModelUsage{{Model: "m", Remaining: 0, RefreshDate: 300}}},
				{Alias: "b", Usages: []types.ModelUsage{{Model: "m", Remaining: 0, RefreshDate: 0}}},
				{Alias: "c", Usages: []types.ModelUsage{{Model: "m", Remaining: 0, RefreshDate: 100}}},
			},
			wantOK:         true,
			wantDegraded:   true,
			wantAlias:      "c",
			wantReasonPart: "earliest refresh",
		},
		{
			name:  "all exhausted tie-breaks equal positive refresh to first sorted alias",
			cli:   "agy",
			model: "m",
			accounts: []AccountQuotaState{
				{Alias: "a", Usages: []types.ModelUsage{{Model: "m", Remaining: 0, RefreshDate: 200}}},
				{Alias: "b", Usages: []types.ModelUsage{{Model: "m", Remaining: 0, RefreshDate: 200}}},
			},
			wantOK:         true,
			wantDegraded:   true,
			wantAlias:      "a",
			wantReasonPart: "earliest refresh",
		},
		{
			name:  "all exhausted with all-zero refresh picks first sorted alias",
			cli:   "agy",
			model: "m",
			accounts: []AccountQuotaState{
				{Alias: "a", Usages: []types.ModelUsage{{Model: "m", Remaining: 0, RefreshDate: 0}}},
				{Alias: "b", Usages: []types.ModelUsage{{Model: "m", Remaining: 0, RefreshDate: 0}}},
			},
			wantOK:         true,
			wantDegraded:   true,
			wantAlias:      "a",
			wantReasonPart: "earliest refresh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SelectAccount(tt.cli, tt.model, tt.pinned, tt.accounts)
			assert.Equal(t, tt.wantOK, got.OK)
			assert.Equal(t, tt.wantDegraded, got.Degraded)
			assert.Equal(t, tt.wantAlias, got.Alias)
			assert.NotEmpty(t, got.Reason)
			if tt.wantReasonPart != "" {
				assert.Contains(t, got.Reason, tt.wantReasonPart)
			}
		})
	}
}

func TestBestAccountCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cli        string
		model      string
		accounts   []AccountUsage
		wantFound  bool
		wantRemain float64
		wantUsable *bool
		wantReason string
	}{
		{
			name:      "no account has entry for model",
			cli:       "claude",
			model:     "m1",
			accounts:  []AccountUsage{{Account: "a", Usages: []types.ModelUsage{{Model: "other", Remaining: 0.9}}}},
			wantFound: false,
		},
		{
			name:  "policy-accepted account beats higher raw remaining",
			cli:   "claude",
			model: "m1",
			accounts: []AccountUsage{
				{Account: "a", Usages: []types.ModelUsage{{Model: "m1", Remaining: 0.90, Usable: boolPtr(false), Reason: "low"}}},
				{Account: "b", Usages: []types.ModelUsage{{Model: "m1", Remaining: 0.04, Usable: boolPtr(true)}}},
			},
			wantFound:  true,
			wantRemain: 0.04,
			wantUsable: boolPtr(true),
		},
		{
			name:  "account missing entry makes rejected target usable",
			cli:   "claude",
			model: "m1",
			accounts: []AccountUsage{
				{Account: "a", Usages: []types.ModelUsage{{Model: "m1", Remaining: 0.08, Usable: boolPtr(false), Reason: "low"}}},
				{Account: "b", Usages: []types.ModelUsage{{Model: "other", Remaining: 0.9}}},
			},
			wantFound:  true,
			wantRemain: 0.08,
			wantUsable: boolPtr(true),
			wantReason: "",
		},
		{
			name:  "all accounts reject stands",
			cli:   "claude",
			model: "m1",
			accounts: []AccountUsage{
				{Account: "a", Usages: []types.ModelUsage{{Model: "m1", Remaining: 0.08, Usable: boolPtr(false), Reason: "weekly: 8%"}}},
			},
			wantFound:  true,
			wantRemain: 0.08,
			wantUsable: boolPtr(false),
			wantReason: "weekly: 8%",
		},
		{
			name:  "without verdicts highest remaining wins and status-only ignored",
			cli:   "claude",
			model: "m1",
			accounts: []AccountUsage{
				{Account: "a", Usages: []types.ModelUsage{{Model: "m1", Remaining: 0.15}}},
				{Account: "b", Usages: []types.ModelUsage{{Model: "m1", Remaining: 0.60}, {Model: "m1", Remaining: 0.99, Status: llms.QuotaStatusError}}},
			},
			wantFound:  true,
			wantRemain: 0.60,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, found := BestAccountCheck(tt.cli, tt.model, tt.accounts)
			assert.Equal(t, tt.wantFound, found)
			if !tt.wantFound {
				return
			}
			assert.InDelta(t, tt.wantRemain, got.Remaining, 1e-9)
			assert.Equal(t, tt.wantUsable, got.Usable)
			assert.Equal(t, tt.wantReason, got.Reason)
		})
	}
}

func TestPolicyHelpers(t *testing.T) {
	t.Parallel()

	t.Run("PolicyReason", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, PolicyReason(QuotaCheck{Remaining: 0.01}))
		assert.Empty(t, PolicyReason(QuotaCheck{Usable: boolPtr(true)}))
		assert.Equal(t, "weekly: 4% remaining", PolicyReason(QuotaCheck{Usable: boolPtr(false), Reason: "weekly: 4% remaining"}))
		assert.Equal(t, "not usable under the quota policy", PolicyReason(QuotaCheck{Usable: boolPtr(false)}))
	})

	t.Run("ExplicitUsable", func(t *testing.T) {
		t.Parallel()
		assert.True(t, ExplicitUsable(QuotaCheck{Remaining: 0.01}))
		assert.False(t, ExplicitUsable(QuotaCheck{Remaining: 0}))
		assert.True(t, ExplicitUsable(QuotaCheck{Remaining: 0, Usable: boolPtr(true)}))
		assert.False(t, ExplicitUsable(QuotaCheck{Remaining: 0.9, Usable: boolPtr(false)}))
	})

	t.Run("AutoSelectable", func(t *testing.T) {
		t.Parallel()
		assert.True(t, AutoSelectable(QuotaCheck{Remaining: 0.11}, 0))
		assert.False(t, AutoSelectable(QuotaCheck{Remaining: 0.10}, 0))
		assert.True(t, AutoSelectable(QuotaCheck{Remaining: 0.06}, 0.05))
		assert.True(t, AutoSelectable(QuotaCheck{Remaining: 0.02, Usable: boolPtr(true)}, 0.10))
		assert.False(t, AutoSelectable(QuotaCheck{Remaining: 0.90, Usable: boolPtr(false)}, 0.10))
	})
}

func TestSelectTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		candidates        []TargetCandidate
		explicitModel     string
		minThreshold      float64
		checks            map[string]QuotaCheck
		wantSelected      bool
		wantTarget        TargetCandidate
		wantExplicitModel string
		wantNotFound      bool
		wantDisabled      bool
		wantHasEnabled    bool
		wantStatusCount   int
		wantStatuses      []TargetStatus
		wantReasonSubstr  string
	}{
		{
			name: "explicit model found and usable",
			candidates: []TargetCandidate{
				{CLI: "agy", Model: "m1", Enabled: true},
				{CLI: "claude", Model: "m2", Enabled: true},
			},
			explicitModel:     "m2",
			checks:            map[string]QuotaCheck{"claude/m2": {Remaining: 0.05}},
			wantSelected:      true,
			wantTarget:        TargetCandidate{CLI: "claude", Model: "m2", Enabled: true},
			wantExplicitModel: "m2",
			wantHasEnabled:    true,
			wantReasonSubstr:  "explicit target claude/m2 usable",
		},
		{
			name:              "explicit model usable via policy with zero remaining",
			candidates:        []TargetCandidate{{CLI: "claude", Model: "m1", Enabled: true}},
			explicitModel:     "m1",
			checks:            map[string]QuotaCheck{"claude/m1": {Remaining: 0, Usable: boolPtr(true)}},
			wantSelected:      true,
			wantTarget:        TargetCandidate{CLI: "claude", Model: "m1", Enabled: true},
			wantExplicitModel: "m1",
			wantHasEnabled:    true,
			wantReasonSubstr:  "explicit target claude/m1 usable",
		},
		{
			name:              "explicit model not in list",
			candidates:        []TargetCandidate{{CLI: "agy", Model: "m1", Enabled: true}},
			explicitModel:     "missing",
			wantSelected:      false,
			wantExplicitModel: "missing",
			wantNotFound:      true,
			wantReasonSubstr:  "not in configured model list",
		},
		{
			name:              "explicit model provider disabled",
			candidates:        []TargetCandidate{{CLI: "agy", Model: "m1", Enabled: false}},
			explicitModel:     "m1",
			wantSelected:      false,
			wantExplicitModel: "m1",
			wantDisabled:      true,
			wantTarget:        TargetCandidate{CLI: "agy", Model: "m1", Enabled: false},
			wantReasonSubstr:  "disabled",
		},
		{
			name: "explicit model rejected by policy captures all statuses",
			candidates: []TargetCandidate{
				{CLI: "agy", Model: "m1", Enabled: true},
				{CLI: "claude", Model: "m2", Enabled: true},
				{CLI: "opencode", Model: "m3", Enabled: false},
			},
			explicitModel: "m1",
			checks: map[string]QuotaCheck{
				"agy/m1":    {Remaining: 0.08, Usable: boolPtr(false), Reason: "weekly: 8% remaining"},
				"claude/m2": {Remaining: 0.70},
			},
			wantSelected:      false,
			wantTarget:        TargetCandidate{CLI: "agy", Model: "m1", Enabled: true},
			wantExplicitModel: "m1",
			wantHasEnabled:    true,
			wantStatusCount:   3,
			wantStatuses: []TargetStatus{
				{CLI: "agy", Model: "m1", Remaining: 0.08, Enabled: true, Forceable: false, Reason: "weekly: 8% remaining"},
				{CLI: "claude", Model: "m2", Remaining: 0.70, Enabled: true, Forceable: true},
				{CLI: "opencode", Model: "m3", Enabled: false, Forceable: false},
			},
			wantReasonSubstr: "weekly: 8% remaining",
		},
		{
			name: "explicit model rejected by policy with empty reason falls back to default reason",
			candidates: []TargetCandidate{
				{CLI: "agy", Model: "m1", Enabled: true},
				{CLI: "claude", Model: "m2", Enabled: true},
			},
			explicitModel: "m1",
			checks: map[string]QuotaCheck{
				"agy/m1":    {Remaining: 0.90, Usable: boolPtr(false)},
				"claude/m2": {Remaining: 0.05},
			},
			wantSelected:      false,
			wantTarget:        TargetCandidate{CLI: "agy", Model: "m1", Enabled: true},
			wantExplicitModel: "m1",
			wantHasEnabled:    true,
			wantStatusCount:   2,
			wantStatuses: []TargetStatus{
				{CLI: "agy", Model: "m1", Remaining: 0.90, Enabled: true, Forceable: false, Reason: "not usable under the quota policy"},
				{CLI: "claude", Model: "m2", Remaining: 0.05, Enabled: true, Forceable: true},
			},
			wantReasonSubstr: "not usable under the quota policy",
		},
		{
			name: "auto selection skips disabled and low quota, switches CLI group",
			candidates: []TargetCandidate{
				{CLI: "agy", Model: "m0", Enabled: false},
				{CLI: "agy", Model: "m1", Enabled: true},
				{CLI: "claude", Model: "m2", Enabled: true},
				{CLI: "opencode", Model: "m3", Enabled: true},
			},
			checks: map[string]QuotaCheck{
				"agy/m1":    {Remaining: 0.05},
				"claude/m2": {Remaining: 0.45},
			},
			wantSelected:     true,
			wantTarget:       TargetCandidate{CLI: "claude", Model: "m2", Enabled: true},
			wantHasEnabled:   true,
			wantStatusCount:  3, // stops evaluating once claude/m2 is selected
			wantReasonSubstr: "selected claude/m2",
		},
		{
			name: "auto selection policy usable=true overrides low remaining",
			candidates: []TargetCandidate{
				{CLI: "claude", Model: "m1", Enabled: true},
			},
			checks: map[string]QuotaCheck{
				"claude/m1": {Remaining: 0.03, Usable: boolPtr(true)},
			},
			wantSelected:     true,
			wantTarget:       TargetCandidate{CLI: "claude", Model: "m1", Enabled: true},
			wantHasEnabled:   true,
			wantStatusCount:  1,
			wantReasonSubstr: "selected claude/m1",
		},
		{
			name: "auto selection policy usable=false overrides high remaining",
			candidates: []TargetCandidate{
				{CLI: "claude", Model: "m1", Enabled: true},
				{CLI: "agy", Model: "m2", Enabled: true},
			},
			checks: map[string]QuotaCheck{
				"claude/m1": {Remaining: 0.90, Usable: boolPtr(false), Reason: "weekly: 4% remaining"},
				"agy/m2":    {Remaining: 0.25},
			},
			wantSelected:    true,
			wantTarget:      TargetCandidate{CLI: "agy", Model: "m2", Enabled: true},
			wantHasEnabled:  true,
			wantStatusCount: 2,
			wantStatuses: []TargetStatus{
				{CLI: "claude", Model: "m1", Remaining: 0.90, Enabled: true, Forceable: false, Reason: "weekly: 4% remaining"},
				{CLI: "agy", Model: "m2", Remaining: 0.25, Enabled: true, Forceable: true},
			},
			wantReasonSubstr: "selected agy/m2",
		},
		{
			name: "auto selection honors non-default minThreshold",
			candidates: []TargetCandidate{
				{CLI: "agy", Model: "m1", Enabled: true},
			},
			minThreshold: 0.05,
			checks: map[string]QuotaCheck{
				"agy/m1": {Remaining: 0.08},
			},
			wantSelected:     true,
			wantTarget:       TargetCandidate{CLI: "agy", Model: "m1", Enabled: true},
			wantHasEnabled:   true,
			wantStatusCount:  1,
			wantReasonSubstr: "selected agy/m1",
		},
		{
			name: "auto selection all providers disabled",
			candidates: []TargetCandidate{
				{CLI: "agy", Model: "m1", Enabled: false},
			},
			wantSelected:     false,
			wantHasEnabled:   false,
			wantStatusCount:  1,
			wantReasonSubstr: "no enabled CLI targets",
		},
		{
			name: "auto selection all exhausted",
			candidates: []TargetCandidate{
				{CLI: "agy", Model: "m1", Enabled: true},
				{CLI: "claude", Model: "m2", Enabled: true},
			},
			checks: map[string]QuotaCheck{
				"agy/m1":    {Remaining: 0.05},
				"claude/m2": {Remaining: 0.10},
			},
			wantSelected:    false,
			wantHasEnabled:  true,
			wantStatusCount: 2,
			wantStatuses: []TargetStatus{
				{CLI: "agy", Model: "m1", Remaining: 0.05, Enabled: true, Forceable: true},
				{CLI: "claude", Model: "m2", Remaining: 0.10, Enabled: true, Forceable: true},
			},
			wantReasonSubstr: "more than 10% quota",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			checkFn := func(cli, model string) QuotaCheck {
				return tt.checks[cli+"/"+model]
			}
			got := SelectTarget(tt.candidates, tt.explicitModel, tt.minThreshold, checkFn)
			assert.Equal(t, tt.wantSelected, got.Selected)
			assert.Equal(t, tt.wantTarget, got.Target)
			assert.Equal(t, tt.wantExplicitModel, got.ExplicitModel)
			assert.Equal(t, tt.wantNotFound, got.ModelNotFound)
			assert.Equal(t, tt.wantDisabled, got.ProviderDisabled)
			assert.Equal(t, tt.wantHasEnabled, got.HasEnabledTarget)
			assert.Len(t, got.Statuses, tt.wantStatusCount)
			if tt.wantStatuses != nil {
				assert.Equal(t, tt.wantStatuses, got.Statuses)
			}
			assert.Contains(t, got.Reason, tt.wantReasonSubstr)
		})
	}
}

func TestResolveSessionCLIAndFiltering(t *testing.T) {
	t.Parallel()

	targets := []TargetCandidate{
		{CLI: "agy", Model: "gemini-3"},
		{CLI: "agy", Model: "gemini-4"},
		{CLI: "claude", Model: "opus"},
	}

	tests := []struct {
		name              string
		forcedModel       string
		storedModel       string
		hasSessionHistory bool
		wantCLI           string
		wantFilteredLen   int
	}{
		{"fresh chat allows cross-CLI fallback", "", "", false, "", 0},
		{"forced model pins its CLI", "opus", "", false, "claude", 1},
		{"stored model pins its CLI", "", "opus", true, "claude", 1},
		{"forced model overrides stored model", "gemini-4", "opus", true, "agy", 2},
		{"history without model pins first CLI", "", "", true, "agy", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cli := ResolveSessionCLI(targets, tt.forcedModel, tt.storedModel, tt.hasSessionHistory)
			assert.Equal(t, tt.wantCLI, cli)

			filtered := FilterTargetsByCLI(targets, cli)
			if tt.wantCLI == "" {
				require.Nil(t, filtered)
			} else {
				require.Len(t, filtered, tt.wantFilteredLen)
				for _, f := range filtered {
					assert.Equal(t, tt.wantCLI, f.CLI)
				}
			}
		})
	}

	t.Run("non-matching cli returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, FilterTargetsByCLI(targets, "opencode"))
	})
}
