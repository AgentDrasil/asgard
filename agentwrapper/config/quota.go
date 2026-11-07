package config

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/AgentDrasil/asgard/llms"
)

// DefaultQuotaProfile names the profile applied to accounts that are not
// bound to one.
const DefaultQuotaProfile = "default"

// QuotaProfile decides when an account is no longer worth using, per quota
// window. Profiles are shared plan templates (e.g. a $20 plan), not
// per-account settings: every account of that plan binds to the same one.
type QuotaProfile struct {
	// Thresholds maps a limit name (exact, case-insensitive, or a glob such as
	// "weekly*") to the remaining fraction at or below which the account is
	// not used. The key "*" is the fallback for limits without their own
	// entry. A limit with no matching entry is only unusable when exhausted.
	Thresholds map[string]float64 `yaml:"thresholds"`

	// Unlimited marks pay-per-request accounts: always usable.
	Unlimited bool `yaml:"unlimited"`
}

// threshold returns the cutoff for the named limit: an exact key first, then
// the first matching glob in key order, then the "*" fallback.
func (p QuotaProfile) threshold(limit string) float64 {
	name := strings.ToLower(limit)
	keys := make([]string, 0, len(p.Thresholds))
	for k := range p.Thresholds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.ToLower(k) == name {
			return p.Thresholds[k]
		}
	}
	for _, k := range keys {
		if k == "*" {
			continue
		}
		if ok, _ := path.Match(strings.ToLower(k), name); ok {
			return p.Thresholds[k]
		}
	}
	return p.Thresholds["*"]
}

// ProfileFor returns the profile bound to an account: the named one, else the
// default profile. ok is false when neither is configured, so callers keep
// their own behavior.
func (c *Config) ProfileFor(name string) (QuotaProfile, bool) {
	if c == nil {
		return QuotaProfile{}, false
	}
	if name != "" {
		if p, ok := c.QuotaProfiles[name]; ok {
			return p, true
		}
	}
	p, ok := c.QuotaProfiles[DefaultQuotaProfile]
	return p, ok
}

// Evaluate returns whether the usage is still usable under the profile and,
// when it is not, why. Per-window thresholds apply independently: any limit
// at or below its own threshold makes the usage unusable. Without limits the
// headline Remaining is checked against the "*" threshold.
func (p QuotaProfile) Evaluate(u llms.ModelUsage) (usable bool, reason string) {
	if p.Unlimited {
		return true, ""
	}
	if len(u.Limits) == 0 {
		if th := p.threshold("*"); u.Remaining <= th {
			return false, fmt.Sprintf("%.0f%% remaining, at or below the %.0f%% threshold", u.Remaining*100, th*100)
		}
		return true, ""
	}
	for _, l := range u.Limits {
		if th := p.threshold(l.Name); l.Remaining <= th {
			return false, fmt.Sprintf("%s: %.0f%% remaining, at or below the %.0f%% threshold", l.Name, l.Remaining*100, th*100)
		}
	}
	return true, ""
}

// ApplyQuotaPolicy sets Usable and Reason on every model entry of usages
// according to the profile bound to the account. Status-only entries are left
// alone. It does nothing when no profile (not even the default) is configured.
func (c *Config) ApplyQuotaPolicy(profileName string, usages []llms.ModelUsage) {
	p, ok := c.ProfileFor(profileName)
	if !ok {
		return
	}
	for i := range usages {
		if usages[i].IsStatusOnly() {
			continue
		}
		usable, reason := p.Evaluate(usages[i])
		usages[i].Usable = &usable
		usages[i].Reason = reason
	}
}
