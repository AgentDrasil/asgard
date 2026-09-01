package types

import "github.com/AgentDrasil/asgard/llms"

// QuotaLimit represents a single specific quota limit (e.g. TOKENS_LIMIT, 5h, weekly).
type QuotaLimit = llms.QuotaLimit

// ModelUsage represents the quota status for a single model.
type ModelUsage = llms.ModelUsage

// UsageOptions controls how model usage / quota queries behave.
type UsageOptions = llms.UsageOptions
