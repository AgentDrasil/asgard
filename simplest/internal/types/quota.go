package types

import "github.com/AgentDrasil/asgard/llms"

// QuotaLimit represents a single specific quota limit (e.g. 5h, weekly, TIME_LIMIT).
type QuotaLimit = llms.QuotaLimit

// ModelUsage represents the quota status for a single model.
type ModelUsage = llms.ModelUsage

// UsageOptions controls how model usage / quota queries behave.
type UsageOptions = llms.UsageOptions
