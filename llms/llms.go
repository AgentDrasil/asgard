// Package llms provides shared types, interfaces, and utilities for LLM providers and quota inspection.
package llms

// QuotaLimit represents a single specific quota limit (e.g. TOKENS_LIMIT, 5h, weekly).
type QuotaLimit struct {
	Name        string  `json:"name"`
	Remaining   float64 `json:"remaining"`
	RefreshDate int64   `json:"refresh_date,omitempty"`
}

// ModelUsage represents the quota status for a single model.
type ModelUsage struct {
	// Model is the full model name, e.g. "gemini/gemini-3.7-flash" or "zai-coding-plan/glm-5.3".
	Model string `json:"model"`

	// Remaining is the fraction of quota still available in [0, 1].
	// 1.0 means fully available; 0.8 means 80% remaining.
	Remaining float64 `json:"remaining"`

	// RefreshDate is the unix timestamp (seconds since epoch) when the quota resets.
	// 0 when quota is fully available.
	RefreshDate int64 `json:"refresh_date,omitempty"`

	// Limits holds the breakdown of individual quota limits.
	Limits []QuotaLimit `json:"limits,omitempty"`
}

// UsageOptions controls how model usage / quota queries behave.
type UsageOptions struct {
	// Detailed requests multi-tier breakdown of quota limits.
	Detailed bool `json:"detailed"`
}
