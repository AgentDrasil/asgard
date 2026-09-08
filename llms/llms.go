// Package llms provides shared types, interfaces, and utilities for LLM providers and quota inspection.
package llms

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
}

// UsageOptions controls how model usage / quota queries behave.
type UsageOptions struct {
	// Detailed requests multi-tier breakdown of quota limits.
	Detailed bool `json:"detailed"`
}
