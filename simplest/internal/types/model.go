package types

import (
	"context"
	"encoding/json"
	"strings"
)

// API wire protocols supported by this library.
const (
	APIOpenAICompat = "openai-compat"
	APIGoogleGemini = "google-gemini"
)

// ModelCostRates is pricing in $/million tokens.
type ModelCostRates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// Model describes one callable model endpoint. Configured programmatically.
type Model struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	API             string            `json:"api"` // APIOpenAICompat or APIGoogleGemini
	Provider        string            `json:"provider"`
	BaseURL         string            `json:"baseUrl"`
	Reasoning       bool              `json:"reasoning"`
	ReasoningEffort []string          `json:"reasoningEffort,omitempty"`
	Input           []string          `json:"input"` // "text", "image"
	Cost            ModelCostRates    `json:"cost"`
	ContextWindow   int64             `json:"contextWindow"`
	MaxTokens       int64             `json:"maxTokens"`
	Headers         map[string]string `json:"headers,omitempty"`
}

// SupportsReasoningEffort reports whether the given effort is allowed for this model.
// If ReasoningEffort is empty, all valid thinking levels are considered supported.
func (m *Model) SupportsReasoningEffort(effort string) bool {
	if m == nil || len(m.ReasoningEffort) == 0 {
		return true
	}
	for _, allowed := range m.ReasoningEffort {
		if strings.EqualFold(allowed, effort) {
			return true
		}
	}
	return false
}

// ToolDef is the provider-facing description of a tool sent to the LLM API.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Context is the payload sent to a provider: system prompt, transcript, tools.
type Context struct {
	SystemPrompt string    `json:"systemPrompt,omitempty"`
	Messages     []Message `json:"messages"`
	Tools        []ToolDef `json:"tools,omitempty"`
}

// StreamOptions carries per-request knobs.
type StreamOptions struct {
	Temperature   *float64      `json:"temperature,omitempty"`
	MaxTokens     *int64        `json:"maxTokens,omitempty"`
	ThinkingLevel ThinkingLevel `json:"thinkingLevel,omitempty"`
	// APIKey overrides the provider's configured key for this call.
	APIKey string `json:"-"`
}

// Provider streams one assistant response over the given wire protocol.
//
// Contract: never panics on request failures; failures are reported as a final
// StreamErrorEvent on the returned channel, which is always closed exactly once.
type Provider interface {
	Stream(ctx context.Context, model *Model, cx *Context, opts *StreamOptions) <-chan AssistantMessageEvent
}
