package types

import (
	"context"
	"strings"
	"time"
)

// ReportFunc is called for each incremental update emitted by the agent during
// a Prompt call. Parameters:
//
//	stepIndex – 0-based line index in the transcript file
//	source    – originating party, e.g. "MODEL", "SYSTEM"
//	entryType – classification: "tool_call", "agent_response", or "other"
//	content   – raw content string from the transcript entry
//	metadata  – optional extra fields (may be nil)
type ReportFunc func(stepIndex int, source, entryType, content string, metadata map[string]any)

// SandboxSpec defines the agent-specific configurations needed when setting up a bubblewrap sandbox.
type SandboxSpec interface {
	SystemPromptHeader() string
	SystemPromptPeerHeader() string
	SystemPromptConfigPath(home string) string
	SkillsMountPath(home string) string
	MountDirectories(home string) []string
	AuthDirectory(home string) string
	ExtraArgs() []string
}

// CLIClient defines the interface that all CLI agents must implement.
type CLIClient interface {
	Usage(ctx context.Context, opts UsageOptions) ([]ModelUsage, error)
	Models(ctx context.Context, opts UsageOptions) ([]string, error)
	Prompt(ctx context.Context, prompt string, opts PromptOptions) (*PromptResult, error)
}

// QuotaLimit represents a single specific quota limit (e.g. 5h, weekly).
type QuotaLimit struct {
	Name        string  `json:"name"`
	Remaining   float64 `json:"remaining"`
	RefreshDate int64   `json:"refresh_date,omitempty"`
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

	// Limits holds the breakdown of individual quota limits.
	Limits []QuotaLimit `json:"limits,omitempty"`
}

// PromptResult is the structured response from a Prompt call.
type PromptResult struct {
	// SessionID is the conversation / session identifier used for this run.
	SessionID string `json:"session_id"`

	// InputTokens is the number of input tokens used.
	InputTokens int `json:"input_tokens"`

	// MaxTokens is the context window size limit.
	MaxTokens int `json:"max_tokens"`

	// Remaining is the fraction of remaining quota in [0, 1], e.g. 0.916.
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

	// Detailed requests multi-tier breakdown of quota limits (e.g. for Web UI).
	// When false (default for CLI mode), only top-level main quota is returned.
	Detailed bool
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

	// AddTmpToDir adds /tmp as an additional allowed directory for the agent.
	AddTmpToDir bool

	// ReportCallback, if non-nil, is invoked for each incremental status update
	// produced by the agent during execution. It is safe to be nil; callers that
	// do not need streaming updates can omit it.
	ReportCallback ReportFunc
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

// RemapSandboxPath cleans and normalizes target file paths.
// /tmp/ paths remain /tmp/ so they are treated as sandbox temporary artifacts.
func RemapSandboxPath(targetPath string) string {
	return strings.TrimSpace(targetPath)
}

// KnownVariants is the canonical set of model effort variants that may be appended
// to a model string as a suffix segment (e.g. "provider/model/low", "model/high").
var KnownVariants = map[string]bool{
	"minimal":  true,
	"low":      true,
	"medium":   true,
	"high":     true,
	"xhigh":    true,
	"max":      true,
	"thinking": true,
}

// SplitModelVariant parses a model string that may contain a variant suffix
// (e.g. "zai-coding-plan/glm-5.3/low", "claude-sonnet-4-6/high") and separates
// the base model name from the variant suffix. If no recognized variant suffix is
// present, the variant return value is empty string.
func SplitModelVariant(modelWithVariant string) (string, string) {
	if modelWithVariant == "" {
		return "", ""
	}
	parts := strings.Split(modelWithVariant, "/")
	if len(parts) > 1 {
		last := strings.ToLower(parts[len(parts)-1])
		if KnownVariants[last] {
			return strings.Join(parts[:len(parts)-1], "/"), last
		}
	}
	return modelWithVariant, ""
}

// DefaultContextWindow is the standard fallback context window size (1M tokens).
const DefaultContextWindow = 1048576

// exactModelContextTable maps canonical model names to their context window sizes.
var exactModelContextTable = map[string]int{
	// agy (Claude: 256000, Gemini: 1048576)
	"claude-opus-4-6-thinking": 256000,
	"claude-sonnet-4-6":        256000,
	"gemini-3.1-pro-high":      1048576,
	"gemini-3.1-pro-low":       1048576,
	"gemini-3.7-flash-high":    1048576,
	"gemini-3.7-flash-low":     1048576,
	"gemini-3.7-flash-medium":  1048576,

	// opencode
	"opencode/big-pickle":                      200000,
	"opencode/ling-3.0-flash-fin-free":         262144,
	"opencode/mimo-v2.5-free":                  1048576,
	"opencode/muse-spark-1.2-contributor-free": 1048576,
	"opencode/nemotron-3-ultra-free":           262144,
	"opencode/nemotron-3.5-lightning-free":     262144,
	"zai-coding-plan/glm-5.3":                  1048576,
	"zai-coding-plan/glm-5.3-flash":            1048576,
}

// StripVariant removes effort/thinking variant suffixes (e.g. "/low", "/high")
// from a model string.
func StripVariant(model string) string {
	model = strings.TrimSpace(model)
	if base, variant := SplitModelVariant(model); variant != "" {
		model = base
	}
	return strings.TrimSpace(model)
}

// LookupContextWindow determines the context window limit for a model.
// It returns the limit in tokens and whether the model was recognized as a known model.
// For unknown models, it falls back to DefaultContextWindow and returns known=false.
func LookupContextWindow(model string) (limit int, known bool) {
	raw := strings.ToLower(strings.TrimSpace(model))
	if raw == "" {
		return DefaultContextWindow, false
	}

	// 1. Exact match on raw lowercase model name
	if val, ok := exactModelContextTable[raw]; ok {
		return val, true
	}

	// 2. Exact match on stripped variant name (e.g. "zai-coding-plan/glm-5.3/low" -> "zai-coding-plan/glm-5.3")
	stripped := StripVariant(raw)
	if val, ok := exactModelContextTable[stripped]; ok {
		return val, true
	}

	// Unknown model fallback
	return DefaultContextWindow, false
}

// GetModelContextWindow returns the context window token limit for a model.
// If unknown, it returns DefaultContextWindow (1M).
func GetModelContextWindow(model string) int {
	limit, _ := LookupContextWindow(model)
	return limit
}
