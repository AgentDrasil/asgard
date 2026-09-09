package types

import (
	"context"
	"encoding/json"
	"strings"
)

// API wire protocols supported by this library.
const (
	APIOpenAICompat = "openai-compat"
	APIGemini       = "gemini"
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
	// ID is the stable identifier used for config matching, sessions, and
	// events. It is what the caller refers to when selecting a model.
	ID string `json:"id"`
	// Model, when non-empty, is the model identifier actually sent to the
	// provider API; it decouples a stable alias from an upstream name that may
	// carry expiry or preview suffixes (e.g. ID "ds-v4.1-flash" maps to wire
	// model "deepseek-v4.1-flash-expires-on-0910"). Empty means ID is sent
	// as-is.
	Model           string            `json:"model,omitempty"`
	Name            string            `json:"name"`
	API             string            `json:"api"` // APIOpenAICompat or APIGemini
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

// WireID returns the model identifier placed in the API request: Model when
// set, otherwise ID.
func (m *Model) WireID() string {
	if m == nil {
		return ""
	}
	if m.Model != "" {
		return m.Model
	}
	return m.ID
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
