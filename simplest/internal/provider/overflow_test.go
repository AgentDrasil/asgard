package provider

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func TestIsContextOverflowError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain non-overflow", errors.New("connection reset by peer"), false},

		// HTTP 413 (pi 73f8de80b): overflow regardless of body.
		{"413 no body", &HTTPStatusError{StatusCode: 413}, true},
		{"413 with overflow body", &HTTPStatusError{StatusCode: 413, Body: `{"error":{"type":"request_too_large"}}`}, true},
		{"413 with unrelated body", &HTTPStatusError{StatusCode: 413, Body: "whatever"}, true},

		// 400/422 match only via an overflow-pattern body.
		{"400 with overflow body", &HTTPStatusError{StatusCode: 400, Body: "prompt is too long: 213462 tokens > 200000 maximum"}, true},
		{"400 with gemini body", &HTTPStatusError{StatusCode: 400, Body: "The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)"}, true},
		{"422 with overflow body", &HTTPStatusError{StatusCode: 422, Body: "Your input exceeds the context window of this model"}, true},
		{"400 without body", &HTTPStatusError{StatusCode: 400}, false},
		{"400 plain body", &HTTPStatusError{StatusCode: 400, Body: "invalid parameter"}, false},

		// Provider-specific overflow text.
		{"anthropic", errors.New("prompt is too long: 213462 tokens > 200000 maximum"), true},
		{"openai", errors.New("Your input exceeds the context window of this model"), true},
		{"litellm", errors.New("Requested token count exceeds the model's maximum context length of 131072 tokens"), true},
		{"gemini", errors.New("input token count 1196265 exceeds the maximum 1048575"), true},
		{"groq", errors.New("Please reduce the length of the messages or completion"), true},
		{"openrouter", errors.New("This endpoint's maximum context length is 65536 tokens"), true},
		{"llama.cpp", errors.New("the request exceeds the available context size, try increasing it"), true},
		{"lm studio", errors.New("tokens to keep from the initial prompt is greater than the context length"), true},
		{"copilot", errors.New("prompt token count of 200000 exceeds the limit of 8192"), true},
		{"minimax", errors.New("invalid params, context window exceeds limit"), true},
		{"kimi", errors.New("Your request exceeded model token limit: 131072 (requested: 200000)"), true},
		{"mistral", errors.New("Prompt contains 300000 tokens which is too large for model with 32000 maximum context length"), true},
		{"z.ai", errors.New(`{"code":"1261","message":"Prompt too long"}`), true},
		{"ollama", errors.New("prompt too long; exceeded max context length by 4096 tokens"), true},
		{"dashscope", errors.New("Range of input length should be [1, 30720]"), true},
		{"generic context_length_exceeded", errors.New("context_length_exceeded"), true},
		{"generic context length exceeded", errors.New("context length exceeded"), true},
		{"generic too many tokens", errors.New("Sorry, too many tokens in request"), true},
		{"generic token limit exceeded", errors.New("token limit exceeded"), true},
		{"413 api error text", errors.New("Cerebras API error (413): "), true},
		{"413 prefix text", errors.New("413 Request Too Large"), true},
		{"xai", errors.New("This model's maximum prompt length is 131072 but the request contains 537812 tokens"), true},

		// Non-overflow exclusions.
		{"rate limit", errors.New("rate limit exceeded for too many tokens"), false},
		{"too many requests", &HTTPStatusError{StatusCode: 429, Body: "Too Many Requests"}, false},
		{"bedrock throttling", errors.New("Throttling error: Too many tokens, please wait before trying again."), false},
		{"service unavailable", errors.New("Service unavailable: too many tokens queued"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsContextOverflowError(tt.err), "err: %v", tt.err)
		})
	}
}

func TestIsContextOverflowMessageErrorText(t *testing.T) {
	overflow := &types.AssistantMessage{
		StopReason:   types.StopError,
		ErrorMessage: "prompt is too long: 213462 tokens > 200000 maximum",
	}
	assert.True(t, IsContextOverflowMessage(overflow, 0))
	assert.True(t, IsContextOverflowMessage(overflow, 200000))

	// Empty-body HTTP 413 surfaces at the message boundary as the
	// HTTPStatusError sentinel text (pi 73f8de80b).
	assert.True(t, IsContextOverflowMessage(&types.AssistantMessage{
		StopReason:   types.StopError,
		ErrorMessage: (&HTTPStatusError{StatusCode: 413}).Error(),
	}, 0), "provider returned HTTP 413 must be detected")
	assert.True(t, IsContextOverflowMessage(&types.AssistantMessage{
		StopReason:   types.StopError,
		ErrorMessage: "HTTP 413 Payload Too Large",
	}, 0))

	assert.False(t, IsContextOverflowMessage(nil, 200000))
	assert.False(t, IsContextOverflowMessage(&types.AssistantMessage{
		StopReason:   types.StopError,
		ErrorMessage: "Throttling error: Too many tokens, please wait before trying again.",
	}, 200000))
	assert.False(t, IsContextOverflowMessage(&types.AssistantMessage{
		StopReason:   types.StopError,
		ErrorMessage: "rate limit exceeded",
	}, 200000))
	assert.False(t, IsContextOverflowMessage(&types.AssistantMessage{
		StopReason: types.StopError,
	}, 200000))
}

func TestIsContextOverflowMessageSilentUsageOverflow(t *testing.T) {
	// z.ai style silent overflow: successful stop, input already past window.
	msg := &types.AssistantMessage{
		StopReason: types.StopStop,
		Usage:      types.Usage{Input: 100001, CacheRead: 0},
	}
	assert.True(t, IsContextOverflowMessage(msg, 100000))

	// Exactly at the window is not silent overflow (must be strictly greater).
	atWindow := &types.AssistantMessage{
		StopReason: types.StopStop,
		Usage:      types.Usage{Input: 90000, CacheRead: 10000},
	}
	assert.False(t, IsContextOverflowMessage(atWindow, 100000))

	// CacheRead counts toward input.
	viaCache := &types.AssistantMessage{
		StopReason: types.StopStop,
		Usage:      types.Usage{Input: 50000, CacheRead: 50001},
	}
	assert.True(t, IsContextOverflowMessage(viaCache, 100000))

	// Without a context window, silent overflow is undetectable.
	assert.False(t, IsContextOverflowMessage(msg, 0))
}

func TestIsContextOverflowMessageLengthStopOverflow(t *testing.T) {
	// Xiaomi MiMo style: length stop, zero output, input fills >=99% window.
	msg := &types.AssistantMessage{
		StopReason: types.StopLength,
		Usage:      types.Usage{Input: 99000},
	}
	assert.True(t, IsContextOverflowMessage(msg, 100000))

	// Near miss: only 90% full.
	nearMiss := &types.AssistantMessage{
		StopReason: types.StopLength,
		Usage:      types.Usage{Input: 90000},
	}
	assert.False(t, IsContextOverflowMessage(nearMiss, 100000))

	// Length stop but with output: not overflow.
	withOutput := &types.AssistantMessage{
		StopReason: types.StopLength,
		Usage:      types.Usage{Input: 100000, Output: 10},
	}
	assert.False(t, IsContextOverflowMessage(withOutput, 100000))

	// No context window: cannot detect.
	assert.False(t, IsContextOverflowMessage(msg, 0))
}

func TestIsContextOverflowMessageNormalMessages(t *testing.T) {
	assert.False(t, IsContextOverflowMessage(textMsgLike("hello"), 100000))
	assert.False(t, IsContextOverflowMessage(&types.AssistantMessage{
		StopReason: types.StopStop,
		Usage:      types.Usage{Input: 100, Output: 50},
	}, 100000))
}

func textMsgLike(text string) *types.AssistantMessage {
	return &types.AssistantMessage{
		Content:    []types.AssistantContent{types.TextContent{Type: "text", Text: text}},
		StopReason: types.StopStop,
	}
}

func TestIsContextOverflowErrorWrapped(t *testing.T) {
	wrapped := fmt.Errorf("stream failed: %w", &HTTPStatusError{StatusCode: 413})
	assert.True(t, IsContextOverflowError(wrapped))
}
