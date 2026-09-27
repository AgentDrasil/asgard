package types

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// API wire protocols supported by this library.
const (
	APIOpenAICompat = "openai-compat"
	APIGemini       = "gemini"
)

// ModelType distinguishes chat models, image models, and classifier models.
type ModelType string

const (
	ModelTypeChat       ModelType = "chat"
	ModelTypeImage      ModelType = "image"
	ModelTypeClassifier ModelType = "classifier"
)

// ThinkingLevelMap maps thinking level to provider-specific wire string representation.
// A nil pointer indicates that the specific level is explicitly unsupported.
type ThinkingLevelMap map[ThinkingLevel]*string

// ModelPromptCache defines prompt caching threshold parameters in tokens.
type ModelPromptCache struct {
	Short int `json:"short,omitempty" yaml:"short,omitempty"`
	Long  int `json:"long,omitempty" yaml:"long,omitempty"`
}

// ModelSamplingParams represents arbitrary model sampling parameters.
type ModelSamplingParams map[string]any

// ModelImageResizeOptions specifies resizing parameters for image input.
type ModelImageResizeOptions struct {
	MaxWidth    int   `json:"maxWidth,omitempty" yaml:"maxWidth,omitempty"`
	MaxHeight   int   `json:"maxHeight,omitempty" yaml:"maxHeight,omitempty"`
	MaxBytes    int64 `json:"maxBytes,omitempty" yaml:"maxBytes,omitempty"`
	JPEGQuality int   `json:"jpegQuality,omitempty" yaml:"jpegQuality,omitempty"`
}

// ModelImageInputLimits defines per-message, per-request, and resizing limits for image inputs.
type ModelImageInputLimits struct {
	Resize        *ModelImageResizeOptions `json:"resize,omitempty" yaml:"resize,omitempty"`
	MaxPerMessage int                      `json:"maxPerMessage,omitempty" yaml:"maxPerMessage,omitempty"`
	MaxPerRequest int                      `json:"maxPerRequest,omitempty" yaml:"maxPerRequest,omitempty"`
}

// ModelInputLimits defines request-level and media-specific input constraints.
type ModelInputLimits struct {
	MaxRequestBytes int64                  `json:"maxRequestBytes,omitempty" yaml:"maxRequestBytes,omitempty"`
	Images          *ModelImageInputLimits `json:"images,omitempty" yaml:"images,omitempty"`
}

// ModelCostRates is pricing in $/million tokens.
type ModelCostRates struct {
	Input      float64 `json:"input" yaml:"input"`
	Output     float64 `json:"output" yaml:"output"`
	CacheRead  float64 `json:"cacheRead" yaml:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite" yaml:"cacheWrite"`
}

// ModelCostTier defines tiered pricing rates when input tokens exceed InputTokensAbove.
type ModelCostTier struct {
	ModelCostRates   `yaml:",inline"`
	InputTokensAbove int64 `json:"inputTokensAbove" yaml:"inputTokensAbove"`
}

// ModelCost describes base pricing rates and optional tiered pricing.
type ModelCost struct {
	ModelCostRates `yaml:",inline"`
	Tiers          []ModelCostTier `json:"tiers,omitempty" yaml:"tiers,omitempty"`
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
	Model            string              `json:"model,omitempty"`
	Name             string              `json:"name"`
	Type             ModelType           `json:"type,omitempty"`
	API              string              `json:"api"` // APIOpenAICompat or APIGemini
	Provider         string              `json:"provider"`
	BaseURL          string              `json:"baseUrl"`
	Reasoning        bool                `json:"reasoning"`
	ReasoningEffort  []string            `json:"reasoningEffort,omitempty"`
	ThinkingLevelMap ThinkingLevelMap    `json:"thinkingLevelMap,omitempty"`
	Input            []string            `json:"input"` // "text", "image"
	Cost             ModelCost           `json:"cost"`
	PromptCache      *ModelPromptCache   `json:"promptCache,omitempty"`
	SamplingParams   ModelSamplingParams `json:"samplingParams,omitempty"`
	InputLimits      *ModelInputLimits   `json:"inputLimits,omitempty"`
	ContextWindow    int64               `json:"contextWindow"`
	MaxTokens        int64               `json:"maxTokens"`
	Headers          map[string]string   `json:"headers,omitempty"`
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

// SupportsImage reports whether the model supports image input.
func (m *Model) SupportsImage() bool {
	if m == nil {
		return false
	}
	for _, in := range m.Input {
		if in == "image" {
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
	// RetryPolicy controls request retry behavior. A nil value defaults to DefaultRetryPolicy().
	RetryPolicy *RetryPolicy `json:"retryPolicy,omitempty"`
	// BeforeProviderRequest allows inspecting or mutating the outgoing HTTP request payload/headers.
	// NOTE: Supported only for OpenAI-compatible providers. On Gemini (genai SDK), this is a no-op.
	BeforeProviderRequest func(req *http.Request, body []byte) ([]byte, error) `json:"-"`
	// OnProviderStreamEvent is called with raw provider stream events prior to normalization.
	OnProviderStreamEvent func(rawEvent string, rawData []byte) `json:"-"`
}

// Provider streams one assistant response over the given wire protocol.
//
// Contract: never panics on request failures; failures are reported as a final
// StreamErrorEvent on the returned channel, which is always closed exactly once.
type Provider interface {
	Stream(ctx context.Context, model *Model, cx *Context, opts *StreamOptions) <-chan AssistantMessageEvent
}
