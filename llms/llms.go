// Package llms provides shared types, interfaces, and utilities for LLM providers and quota inspection.
package llms

import "strings"

// QuotaLimit represents a single specific quota limit (e.g. TOKENS_LIMIT, 5h, weekly).
type QuotaLimit struct {
	Name        string  `json:"name"`
	Remaining   float64 `json:"remaining"`
	RefreshDate int64   `json:"refresh_date,omitempty"`
}

// AccountBalance describes the prepaid balance of a pay-as-you-go provider
// account (e.g. the DeepSeek /user/balance endpoint). Monetary amounts are
// kept as strings to mirror the provider wire format exactly.
type AccountBalance struct {
	// IsAvailable reports whether the account balance is usable for API calls.
	IsAvailable bool `json:"is_available"`

	// BalanceInfos holds the per-currency balance breakdown.
	BalanceInfos []BalanceInfo `json:"balance_infos"`
}

// BalanceInfo is one currency entry of an AccountBalance.
type BalanceInfo struct {
	Currency        string `json:"currency"`
	TotalBalance    string `json:"total_balance"`
	GrantedBalance  string `json:"granted_balance"`
	ToppedUpBalance string `json:"topped_up_balance"`
}

// ModelUsage represents the quota status for a single model.
type ModelUsage struct {
	// Model is the full model name, e.g. "gemini/gemini-3.7-flash" or "zai-coding-plan/glm-5.3".
	Model string `json:"model"`

	// Remaining is the fraction of quota still available in [0, 1].
	// 1.0 means fully available; 0.8 means 80% remaining.
	// For prepaid providers carrying a Balance, Remaining reports account
	// usability instead: 1.0 when the balance is available, 0 when it is
	// empty or unavailable.
	Remaining float64 `json:"remaining"`

	// RefreshDate is the unix timestamp (seconds since epoch) when the quota resets.
	// 0 when quota is fully available.
	RefreshDate int64 `json:"refresh_date,omitempty"`

	// Limits holds the breakdown of individual quota limits.
	Limits []QuotaLimit `json:"limits,omitempty"`

	// Balance is the prepaid account balance for pay-as-you-go providers
	// (e.g. DeepSeek). It is nil for subscription/rate-limit based quotas.
	Balance *AccountBalance `json:"balance,omitempty"`

	// Account is the runner-local account alias this quota belongs to. It is
	// empty for CLIs without accounts.
	Account string `json:"account,omitempty"`

	// Status marks a status-only entry: Model is empty and the entry carries
	// no quota. Empty for a regular quota entry. See the QuotaStatus*
	// constants. Consumers that look for models must skip these entries.
	Status string `json:"status,omitempty"`

	// Message is the error text of a status-only entry.
	Message string `json:"message,omitempty"`
}

// Quota entry statuses (ModelUsage.Status).
const (
	// QuotaStatusNoCredentials: the CLI (or account) has no usable login.
	QuotaStatusNoCredentials = "no_credentials"
	// QuotaStatusError: the quota query failed; Message holds the error.
	QuotaStatusError = "error"
)

// IsStatusOnly reports whether the entry is a status-only entry rather than a
// model's quota.
func (u ModelUsage) IsStatusOnly() bool { return u.Status != "" }

// NoCredentialsMarkers are lowercase fragments of CLI output that mean the CLI
// is not logged in.
var NoCredentialsMarkers = []string{
	"authentication required",
	"not logged in",
	"please run /login",
	"invalid api key",
	"no credentials",
	"credentials not found",
}

// IsNoCredentialsText reports whether CLI output or an error text says the
// CLI has no usable credentials.
func IsNoCredentialsText(text string) bool {
	text = strings.ToLower(text)
	for _, m := range NoCredentialsMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// StatusUsage builds the status-only entry for a failed quota query:
// QuotaStatusNoCredentials when the error text indicates missing credentials,
// QuotaStatusError otherwise.
func StatusUsage(err error) ModelUsage {
	st := QuotaStatusError
	if IsNoCredentialsText(err.Error()) {
		st = QuotaStatusNoCredentials
	}
	return ModelUsage{Status: st, Message: err.Error()}
}

// UsageOptions controls how model usage / quota queries behave.
type UsageOptions struct {
	// Detailed requests multi-tier breakdown of quota limits.
	Detailed bool `json:"detailed"`
}
