package types

import (
	"context"
	"time"
)

// CLIClient defines the interface that all CLI agents must implement.
type CLIClient interface {
	Usage(ctx context.Context, opts UsageOptions) ([]ModelUsage, error)
	Models(ctx context.Context, opts UsageOptions) ([]string, error)
	Prompt(ctx context.Context, prompt string, opts PromptOptions) (*PromptResult, error)
}

// ModelUsage represents the quota status for a single model.
type ModelUsage struct {
	// Model is the full model name, e.g. "Claude Sonnet 4.6 (Thinking)".
	Model string `json:"model"`

	// Remaining is the fraction of quota still available in [0, 1].
	// 1.0 means fully available; 0.8 means 80% remaining.
	Remaining float64 `json:"remaining"`

	// RefreshDate is the unix timestamp (seconds since epoch) when the quota resets.
	// 0 when quota is fully available.
	RefreshDate int64 `json:"refresh_date,omitempty"`
}

// PromptResult is the structured response from a Prompt call.
type PromptResult struct {
	// SessionID is the conversation / session identifier used for this run.
	SessionID string `json:"session_id"`

	// InputTokens is the value of input_tokens from the final statusline.
	InputTokens int `json:"input_tokens"`

	// MaxTokens is the value of max (context window size) from the final statusline.
	MaxTokens int `json:"max_tokens"`

	// Remaining is the fraction of remaining quota in [0, 1] from the final
	// statusline, e.g. 0.916.
	Remaining float64 `json:"remaining"`

	// LastContent is the raw "content" field of the last line in the
	// transcript JSONL file, giving the caller access to the full response.
	LastContent string `json:"last_content"`
}

// UsageOptions controls how Usage behaves.
type UsageOptions struct {
	// Dir is the working directory passed to the agent.
	Dir string

	// StartupDelay is the maximum time to wait for agent's statusbar to report
	// "idle" before sending the command.
	StartupDelay time.Duration

	// ResponseDelay is how long to wait for the command response to appear.
	ResponseDelay time.Duration
}

func (o *UsageOptions) StartupDelayOrDefault() time.Duration {
	if o.StartupDelay > 0 {
		return o.StartupDelay
	}
	return 10 * time.Second
}

func (o *UsageOptions) ResponseDelayOrDefault() time.Duration {
	if o.ResponseDelay > 0 {
		return o.ResponseDelay
	}
	return 1 * time.Second
}

// PromptOptions controls how Prompt behaves.
type PromptOptions struct {
	// Dir is the working directory passed to the agent.
	Dir string

	// SessionID is the conversation ID to resume.
	SessionID string

	// StartupDelay is the maximum time to wait for agent's statusbar to report
	// "idle" before sending the prompt.
	StartupDelay time.Duration

	// ResponseDelay is the maximum time to wait for the agent to return to idle.
	ResponseDelay time.Duration

	// Model is the name of the model to select.
	Model string
}

func (o *PromptOptions) StartupDelayOrDefault() time.Duration {
	if o.StartupDelay > 0 {
		return o.StartupDelay
	}
	return 10 * time.Second
}

func (o *PromptOptions) ResponseDelayOrDefault() time.Duration {
	if o.ResponseDelay > 0 {
		return o.ResponseDelay
	}
	return 300 * time.Second
}
