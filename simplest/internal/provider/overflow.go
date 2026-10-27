// overflow.go detects context-overflow failures reported by LLM providers.
//
// Port of pi packages/ai/src/utils/overflow.ts (v0.99.2) plus the HTTP 413
// detection added in pi 73f8de80b: overflow can surface as an HTTP status
// (413, or 400/422 with an overflow body), as a StopError assistant message
// with a provider-specific error text, as a silent overflow (a successful
// "stop" whose input usage already exceeds the context window), or as a
// length stop with zero output on an input that fills the window.
package provider

import (
	"errors"
	"regexp"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// overflowPatterns are matched (case-insensitively) against provider error
// text. Ported from pi overflow.ts OVERFLOW_PATTERNS; RE2 needs no lookahead
// for any of them. The final pattern is the HTTP 413 text form from
// pi 73f8de80b (e.g. "Foo API error (413): ..." or a bare "413" prefix).
var overflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)prompt (?:is )?too long`),                                                                    // Anthropic, z.ai
	regexp.MustCompile(`(?i)prompt exceeds max length`),                                                                  // z.ai CN endpoint
	regexp.MustCompile(`(?i)request_too_large`),                                                                          // Anthropic byte-size overflow (HTTP 413)
	regexp.MustCompile(`(?i)input is too long for requested model`),                                                      // Amazon Bedrock
	regexp.MustCompile(`(?i)exceeds the context window`),                                                                 // OpenAI
	regexp.MustCompile(`(?i)exceeds (?:the )?(?:model'?s )?maximum context length(?: of [\d,]+ tokens?|\s*\([\d,]+\))?`), // OpenAI-compatible proxies (LiteLLM)
	regexp.MustCompile(`(?i)input token count.*exceeds the maximum`),                                                     // Google (Gemini)
	regexp.MustCompile(`(?i)maximum prompt length is \d+`),                                                               // xAI (Grok)
	regexp.MustCompile(`(?i)reduce the length of the messages`),                                                          // Groq
	regexp.MustCompile(`(?i)maximum context length is \d+ tokens`),                                                       // OpenRouter (most backends)
	regexp.MustCompile(`(?i)exceeds (?:the )?maximum allowed input length of [\d,]+ tokens?`),                            // OpenRouter/Poolside
	regexp.MustCompile(`(?i)input \(\d+ tokens\) is longer than the model'?s context length \(\d+ tokens\)`),             // Together AI
	regexp.MustCompile(`(?i)exceeds the limit of \d+`),                                                                   // GitHub Copilot
	regexp.MustCompile(`(?i)exceeds the available context size`),                                                         // llama.cpp server
	regexp.MustCompile(`(?i)greater than the context length`),                                                            // LM Studio
	regexp.MustCompile(`(?i)context window exceeds limit`),                                                               // MiniMax
	regexp.MustCompile(`(?i)exceeded model token limit`),                                                                 // Kimi For Coding
	regexp.MustCompile(`(?i)too large for model with \d+ maximum context length`),                                        // Mistral
	regexp.MustCompile(`(?i)prompt has [\d,]+ tokens?, but the configured context size is [\d,]+ tokens?`),               // DS4 server
	regexp.MustCompile(`(?i)model_context_window_exceeded`),                                                              // z.ai non-standard finish_reason
	regexp.MustCompile(`(?i)prompt too long; exceeded (?:max )?context length`),                                          // Ollama
	regexp.MustCompile(`(?i)range of input length should be`),                                                            // DashScope / Qwen
	regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded`),                                                              // generic fallback
	regexp.MustCompile(`(?i)too many tokens`),                                                                            // generic fallback
	regexp.MustCompile(`(?i)token limit exceeded`),                                                                       // generic fallback
	regexp.MustCompile(`(?i)^(?:[\w -]+ API error \(413\):|413(?:\s|:|$))`),                                              // HTTP 413 text form (pi 73f8de80b)
	regexp.MustCompile(`(?i)^provider returned HTTP 413(?::|$)`),                                                         // HTTPStatusError with an empty-body 413
	regexp.MustCompile(`(?i)^HTTP 413\b`),                                                                                // generic "HTTP 413 ..." status text
}

// cerebrasBodylessOverflowPattern matches Cerebras's bodyless 400/413
// overflow responses, e.g. "400 status code (no body)".
var cerebrasBodylessOverflowPattern = regexp.MustCompile(`(?i)^4(?:00|13)\s*(?:status code)?\s*\(no body\)`)

// nonOverflowPatterns exclude rate-limit / throttling / availability errors
// that mention token counts, so they are never mistaken for overflow. Ported
// from pi overflow.ts NON_OVERFLOW_PATTERNS.
var nonOverflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(?:Throttling error|Service unavailable):`), // AWS Bedrock
	regexp.MustCompile(`(?i)rate limit`),                                 // generic rate limiting
	regexp.MustCompile(`(?i)too many requests`),                          // HTTP 429 style
}

// IsContextOverflowError reports whether err is a context-overflow failure.
// An *HTTPStatusError with StatusCode 413 is always overflow (pi 73f8de80b);
// 400/422 are overflow only when the body matches an overflow pattern. Any
// other error is inspected by its text, after excluding non-overflow errors
// (rate limits, throttling, availability).
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == 413 {
			return true
		}
		// 400/422 (and any other status) count as overflow only when the
		// body/text unambiguously matches an overflow pattern.
		text := httpErr.Body
		if text == "" {
			text = err.Error()
		}
		return isOverflowText(text)
	}
	return isOverflowText(err.Error())
}

// IsContextOverflowMessage reports whether an assistant message represents a
// context overflow. Port of pi isContextOverflow (overflow.ts):
//
//  1. error-based: StopError with an ErrorMessage matching an overflow
//     pattern (after non-overflow exclusions);
//  2. silent overflow: a successful "stop" whose input+cacheRead usage
//     already exceeds the context window (z.ai style);
//  3. length-stop overflow: "length" with zero output on an input filling
//     at least 99% of the window (Xiaomi MiMo style).
//
// A non-positive contextWindow disables cases 2 and 3.
func IsContextOverflowMessage(msg *types.AssistantMessage, contextWindow int64) bool {
	if msg == nil {
		return false
	}
	if msg.StopReason == types.StopError && msg.ErrorMessage != "" && isOverflowText(msg.ErrorMessage) {
		return true
	}
	if msg.StopReason == types.StopError && msg.ErrorMessage != "" &&
		msg.Provider == "cerebras" && cerebrasBodylessOverflowPattern.MatchString(msg.ErrorMessage) {
		return true
	}
	if contextWindow > 0 {
		input := msg.Usage.Input + msg.Usage.CacheRead
		if msg.StopReason == types.StopStop && input > contextWindow {
			return true
		}
		if msg.StopReason == types.StopLength && msg.Usage.Output == 0 &&
			float64(input) >= 0.99*float64(contextWindow) { // exact-boundary compare like pi
			return true
		}
	}
	return false
}

// isOverflowText applies the exclusion patterns first, then the overflow
// patterns, mirroring pi overflow.ts.
func isOverflowText(s string) bool {
	if s == "" {
		return false
	}
	for _, p := range nonOverflowPatterns {
		if p.MatchString(s) {
			return false
		}
	}
	for _, p := range overflowPatterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}
