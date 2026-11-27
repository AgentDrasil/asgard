package agentwrapper

import (
	"fmt"
	"math"
	"strings"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// MinAutoQuotaThreshold is the default minimum remaining quota fraction (10%)
// required for automatic target selection when no quota policy verdict applies.
const MinAutoQuotaThreshold = 0.10

// AccountQuotaState is the fetched quota state of one account alias for a CLI.
type AccountQuotaState struct {
	Alias  string
	Usages []types.ModelUsage
	Err    error
}

// AccountSelection is the result of pure account selection for a (cli, model) pair.
type AccountSelection struct {
	Alias    string
	OK       bool
	Degraded bool
	Reason   string
}

// stripProvider drops a leading "<provider>/" from a model name.
func stripProvider(m string) string {
	if i := strings.Index(m, "/"); i >= 0 {
		return m[i+1:]
	}
	return m
}

// modelMatchesCaseInsensitive reports whether two model identifiers match
// case-insensitively, either as-is or after stripping a leading "<provider>/".
func modelMatchesCaseInsensitive(want, have string) bool {
	return strings.EqualFold(want, have) || strings.EqualFold(stripProvider(want), stripProvider(have))
}

// UsageExhausted reports whether the account usage should not serve the model.
// The quota policy verdict, when attached, is authoritative; otherwise a
// non-positive headline Remaining or any non-positive limit Remaining counts as
// exhausted.
func UsageExhausted(u types.ModelUsage) bool {
	if u.Usable != nil {
		return !*u.Usable
	}
	if u.Remaining <= 0 {
		return true
	}
	for _, l := range u.Limits {
		if l.Remaining <= 0 {
			return true
		}
	}
	return false
}

// ModelAvailability reports whether usages leave quota for model on cli, and
// when an exhausted model refreshes (0 if unknown). A model with no matching
// entry in usages counts as available.
func ModelAvailability(cli string, usages []types.ModelUsage, model string) (available bool, refresh int64) {
	matched := false
	for _, u := range usages {
		if !MatchesModel(cli, model, u.Model) && !modelMatchesCaseInsensitive(model, u.Model) {
			continue
		}
		matched = true
		if !UsageExhausted(u) {
			return true, 0
		}
		if u.RefreshDate > 0 && (refresh == 0 || u.RefreshDate < refresh) {
			refresh = u.RefreshDate
		}
	}
	return !matched, refresh
}

// SelectAccount chooses which account alias to use for (cli, model) given an
// optional pinned override and per-account quota states (expected in sorted
// alias order).
//
// Selection rules:
//  1. A non-empty pinned override wins immediately.
//  2. With 0 accounts, OK is false. With 1 account, that account is chosen.
//  3. Otherwise accounts are scanned in order and the first available account
//     wins.
//  4. Accounts whose quota fetch errored are skipped while any later account
//     might still be confirmed available; if none are confirmed available and
//     at least one errored, the first account in order is used (Degraded=true).
//  5. When every account answered and all are exhausted, the account whose
//     quota refreshes earliest is picked (Degraded=true).
func SelectAccount(cli, model, pinned string, accounts []AccountQuotaState) AccountSelection {
	if pinned != "" {
		return AccountSelection{
			Alias:  pinned,
			OK:     true,
			Reason: fmt.Sprintf("pinned to account %q", pinned),
		}
	}
	if len(accounts) == 0 {
		return AccountSelection{
			OK:     false,
			Reason: "no accounts configured",
		}
	}
	if len(accounts) == 1 {
		return AccountSelection{
			Alias:  accounts[0].Alias,
			OK:     true,
			Reason: fmt.Sprintf("single account %q", accounts[0].Alias),
		}
	}

	bestAlias, bestRefresh := "", int64(math.MaxInt64)
	errored := false
	for _, acct := range accounts {
		if acct.Err != nil {
			errored = true
			continue
		}
		avail, refresh := ModelAvailability(cli, acct.Usages, model)
		if avail {
			return AccountSelection{
				Alias:  acct.Alias,
				OK:     true,
				Reason: fmt.Sprintf("account %q has remaining quota for %s", acct.Alias, model),
			}
		}
		if refresh == 0 {
			refresh = math.MaxInt64
		}
		if bestAlias == "" || refresh < bestRefresh {
			bestAlias, bestRefresh = acct.Alias, refresh
		}
	}
	if errored {
		return AccountSelection{
			Alias:    accounts[0].Alias,
			OK:       true,
			Degraded: true,
			Reason:   fmt.Sprintf("no account confirmed available and some quota unknown, using first account %q", accounts[0].Alias),
		}
	}
	return AccountSelection{
		Alias:    bestAlias,
		OK:       true,
		Degraded: true,
		Reason:   fmt.Sprintf("all accounts exhausted for %s, picking earliest refresh %q", model, bestAlias),
	}
}

// bestMatchingUsage picks, among the entries for model, the one a run should
// count on: an entry the quota policy accepts wins, then the most remaining.
func bestMatchingUsage(cli, model string, usages []types.ModelUsage) (best types.ModelUsage, found bool) {
	accepted := func(u types.ModelUsage) bool { return u.Usable != nil && *u.Usable }
	for _, u := range usages {
		if u.IsStatusOnly() || !MatchesModel(cli, model, u.Model) {
			continue
		}
		switch {
		case !found,
			accepted(u) && !accepted(best),
			accepted(u) == accepted(best) && u.Remaining > best.Remaining:
			best, found = u, true
		}
	}
	return best, found
}

// BestAccountCheck reports the best account for model across accounts. Like
// account selection, an account that answered but has no entry for the model
// counts as available, so when the quota policy rejects every account that does
// have an entry, such an account still makes the target usable. found is false
// when no account has an entry for the model.
func BestAccountCheck(cli, model string, accounts []AccountUsage) (QuotaCheck, bool) {
	var all []types.ModelUsage
	missing := false
	for _, a := range accounts {
		if _, ok := bestMatchingUsage(cli, model, a.Usages); !ok {
			missing = true
		}
		all = append(all, a.Usages...)
	}
	best, found := bestMatchingUsage(cli, model, all)
	if !found {
		return QuotaCheck{}, false
	}
	check := QuotaCheck{Remaining: best.Remaining, Usable: best.Usable, Reason: best.Reason}
	if missing && best.Usable != nil && !*best.Usable {
		usable := true
		check.Usable, check.Reason = &usable, ""
	}
	return check, true
}

// PolicyReason returns why the quota policy rejects the target, or "" when it
// does not (no verdict, or an accepting one).
func PolicyReason(c QuotaCheck) string {
	if c.Usable == nil || *c.Usable {
		return ""
	}
	if c.Reason != "" {
		return c.Reason
	}
	return "not usable under the quota policy"
}

// ExplicitUsable reports whether an explicitly selected model may run: the quota
// policy verdict decides when there is one, otherwise the model only needs
// some quota left (> 0).
func ExplicitUsable(c QuotaCheck) bool {
	if c.Usable != nil {
		return *c.Usable
	}
	return c.Remaining > 0
}

// AutoSelectable reports whether automatic selection may pick a target. The
// account's quota policy verdict decides when there is one; otherwise the
// remaining quota must exceed minThreshold (defaulting to MinAutoQuotaThreshold
// if <= 0).
func AutoSelectable(c QuotaCheck, minThreshold float64) bool {
	if c.Usable != nil {
		return *c.Usable
	}
	if minThreshold <= 0 {
		minThreshold = MinAutoQuotaThreshold
	}
	return c.Remaining > minThreshold
}

// TargetCandidate describes a configured (CLI, Model) candidate and whether
// its provider is currently enabled.
type TargetCandidate struct {
	CLI     string
	Model   string
	Enabled bool
}

// TargetStatus records the evaluated quota state of one candidate target.
type TargetStatus struct {
	CLI       string
	Model     string
	Remaining float64
	Enabled   bool
	Forceable bool
	Reason    string
}

// TargetSelection is the result of pure target selection across candidates.
type TargetSelection struct {
	Target           TargetCandidate
	Selected         bool
	Reason           string
	Statuses         []TargetStatus
	HasEnabledTarget bool
	ExplicitModel    string
	ModelNotFound    bool
	ProviderDisabled bool
}

// SelectTarget evaluates candidate targets against quota checks and policy
// rules, returning either the chosen target or structured failure details.
//
// When explicitModel is non-empty, only that model is considered (no fallback).
// When explicitModel is empty, the first enabled candidate passing
// AutoSelectable is chosen; evaluation stops as soon as one is selected.
func SelectTarget(candidates []TargetCandidate, explicitModel string, minThreshold float64, checkFn func(cli, model string) QuotaCheck) TargetSelection {
	if minThreshold <= 0 {
		minThreshold = MinAutoQuotaThreshold
	}

	if explicitModel != "" {
		var matched *TargetCandidate
		for i := range candidates {
			if candidates[i].Model == explicitModel {
				matched = &candidates[i]
				break
			}
		}
		if matched == nil {
			return TargetSelection{
				ExplicitModel: explicitModel,
				ModelNotFound: true,
				Reason:        fmt.Sprintf("selected model %q is not in configured model list", explicitModel),
			}
		}
		if !matched.Enabled {
			return TargetSelection{
				Target:           *matched,
				ExplicitModel:    explicitModel,
				ProviderDisabled: true,
				Reason:           fmt.Sprintf("provider %q for model %q is disabled in configuration", matched.CLI, explicitModel),
			}
		}
		check := checkFn(matched.CLI, matched.Model)
		if ExplicitUsable(check) {
			return TargetSelection{
				Target:           *matched,
				Selected:         true,
				HasEnabledTarget: true,
				ExplicitModel:    explicitModel,
				Reason:           fmt.Sprintf("explicit target %s/%s usable (%.0f%% remaining)", matched.CLI, matched.Model, check.Remaining*100),
			}
		}

		statuses := make([]TargetStatus, 0, len(candidates))
		for _, c := range candidates {
			st := TargetStatus{CLI: c.CLI, Model: c.Model, Enabled: c.Enabled}
			if c.Enabled {
				chk := checkFn(c.CLI, c.Model)
				st.Remaining = chk.Remaining
				st.Forceable = ExplicitUsable(chk)
				st.Reason = PolicyReason(chk)
			}
			statuses = append(statuses, st)
		}
		reason := PolicyReason(check)
		if reason == "" {
			reason = fmt.Sprintf("model %q has no quota remaining", explicitModel)
		}
		return TargetSelection{
			Target:           *matched,
			Selected:         false,
			HasEnabledTarget: true,
			ExplicitModel:    explicitModel,
			Statuses:         statuses,
			Reason:           reason,
		}
	}

	hasEnabled := false
	statuses := make([]TargetStatus, 0, len(candidates))
	for _, c := range candidates {
		if !c.Enabled {
			statuses = append(statuses, TargetStatus{CLI: c.CLI, Model: c.Model, Enabled: false})
			continue
		}
		hasEnabled = true
		check := checkFn(c.CLI, c.Model)
		statuses = append(statuses, TargetStatus{
			CLI:       c.CLI,
			Model:     c.Model,
			Remaining: check.Remaining,
			Enabled:   true,
			Forceable: ExplicitUsable(check),
			Reason:    PolicyReason(check),
		})
		if AutoSelectable(check, minThreshold) {
			return TargetSelection{
				Target:           c,
				Selected:         true,
				HasEnabledTarget: true,
				Statuses:         statuses,
				Reason:           fmt.Sprintf("selected %s/%s (%.0f%% remaining)", c.CLI, c.Model, check.Remaining*100),
			}
		}
	}

	if !hasEnabled {
		return TargetSelection{
			HasEnabledTarget: false,
			Statuses:         statuses,
			Reason:           "no enabled CLI targets available",
		}
	}
	return TargetSelection{
		HasEnabledTarget: true,
		Statuses:         statuses,
		Reason:           fmt.Sprintf("no CLI target with more than %.0f%% quota remaining", minThreshold*100),
	}
}

// CLIForModel returns the CLI of the first candidate carrying model, or "" when
// model is empty or not present in targets.
func CLIForModel(targets []TargetCandidate, model string) string {
	if model == "" {
		return ""
	}
	for _, t := range targets {
		if t.Model == model {
			return t.CLI
		}
	}
	return ""
}

// ResolveSessionCLI determines which CLI group a chat should be pinned to,
// preserving existing session history when present while allowing fresh chats
// to fall back across CLI groups.
func ResolveSessionCLI(targets []TargetCandidate, forcedModel, storedModel string, hasSessionHistory bool) string {
	if cli := CLIForModel(targets, forcedModel); cli != "" {
		return cli
	}
	if cli := CLIForModel(targets, storedModel); cli != "" {
		return cli
	}
	if hasSessionHistory && len(targets) > 0 {
		return targets[0].CLI
	}
	return ""
}

// FilterTargetsByCLI filters targets down to those running on cli. An empty
// cli returns nil so callers keep the full candidate list.
func FilterTargetsByCLI(targets []TargetCandidate, cli string) []TargetCandidate {
	if cli == "" {
		return nil
	}
	var out []TargetCandidate
	for _, t := range targets {
		if t.CLI == cli {
			out = append(out, t)
		}
	}
	return out
}
