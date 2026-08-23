package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/AgentDrasil/asgard/simplest/types"
)

// OpenAICompat streams over the OpenAI chat-completions protocol
// (POST {baseURL}/chat/completions, SSE). Configure the API key
// programmatically via NewOpenAICompat or StreamOptions.APIKey.
type OpenAICompat struct {
	Client  *http.Client
	APIKey  string
	BaseURL string // optional override; model.BaseURL wins when set
}

var _ types.Provider = (*OpenAICompat)(nil)

func NewOpenAICompat(apiKey string) *OpenAICompat {
	return &OpenAICompat{Client: http.DefaultClient, APIKey: apiKey}
}

func (p *OpenAICompat) apiKey(opts *types.StreamOptions) string {
	if opts != nil && opts.APIKey != "" {
		return opts.APIKey
	}
	return p.APIKey
}

// wire shapes.

type oaToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaWireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function oaToolCallFn `json:"function"`
}

type oaImageURL struct {
	URL string `json:"url"`
}

type oaPart struct {
	Type     string      `json:"type,omitempty"`
	Text     string      `json:"text,omitempty"`
	ImageURL *oaImageURL `json:"image_url,omitempty"`
}

type oaMessage struct {
	Role             string           `json:"role"`
	Content          any              `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	Reasoning        string           `json:"reasoning,omitempty"`
	ReasoningText    string           `json:"reasoning_text,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []oaWireToolCall `json:"tool_calls,omitempty"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model         string      `json:"model"`
	Messages      []oaMessage `json:"messages"`
	Stream        bool        `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
	MaxCompletionTokens *int64   `json:"max_completion_tokens,omitempty"`
	Tools               []oaTool `json:"tools,omitempty"`
	ReasoningEffort     string   `json:"reasoning_effort,omitempty"`
}

type oaUsage struct {
	PromptTokens         int64 `json:"prompt_tokens"`
	CompletionTokens     int64 `json:"completion_tokens"`
	CachedTokens         int64 `json:"cached_tokens"`
	PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens    int64 `json:"cached_tokens"`
		CacheWriteToken int64 `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

func (u *oaUsage) toUsage(m *types.Model) types.Usage {
	// Cache read tokens: prompt_tokens_details?.cached_tokens ??
	// prompt_cache_hit_tokens ?? cached_tokens ?? 0. Default 0 so that
	// servers without cache details don't bill all prompt tokens as cache reads.
	cacheRead := int64(0)
	cacheWrite := int64(0)
	if u.PromptTokensDetails != nil {
		cacheRead = u.PromptTokensDetails.CachedTokens
		cacheWrite = u.PromptTokensDetails.CacheWriteToken
	} else if u.PromptCacheHitTokens > 0 {
		cacheRead = u.PromptCacheHitTokens
	} else if u.CachedTokens > 0 {
		cacheRead = u.CachedTokens
	}
	in := u.PromptTokens - cacheRead - cacheWrite
	if in < 0 {
		in = 0
	}
	usage := types.Usage{
		Input:       in,
		Output:      u.CompletionTokens,
		CacheRead:   cacheRead,
		CacheWrite:  cacheWrite,
		TotalTokens: in + u.CompletionTokens + cacheRead + cacheWrite,
	}
	if u.CompletionTokensDetails != nil {
		r := u.CompletionTokensDetails.ReasoningTokens
		usage.Reasoning = &r
	}
	CalculateCost(m, &usage)
	return usage
}

// message conversion.

func imageSupported(m *types.Model) bool {
	for _, in := range m.Input {
		if in == "image" {
			return true
		}
	}
	return false
}

const imageOmittedPlaceholder = "(image omitted: model does not support images)"

// ConvertMessages renders a Context into OpenAI chat-completions messages.
func (p *OpenAICompat) ConvertMessages(model *types.Model, cx *types.Context) ([]oaMessage, error) {
	msgs := []oaMessage{}
	if cx.SystemPrompt != "" {
		msgs = append(msgs, oaMessage{Role: "system", Content: cx.SystemPrompt})
	}
	for _, m := range cx.Messages {
		switch msg := m.(type) {
		case *types.UserMessage:
			blocks, err := types.DecodeUserContent(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("user content: %w", err)
			}
			parts, hasImage := userParts(blocks, imageSupported(model))
			if len(parts) == 0 && !hasImage {
				continue
			}
			if !hasImage {
				msgs = append(msgs, oaMessage{Role: "user", Content: parts[0].Text})
			} else {
				msgs = append(msgs, oaMessage{Role: "user", Content: parts})
			}
		case *types.AssistantMessage:
			text := ""
			reasoningField := ""
			var thinking []string
			for _, blk := range msg.Content {
				switch b := blk.(type) {
				case types.TextContent:
					if strings.TrimSpace(b.Text) != "" {
						text += b.Text
					}
				case types.ThinkingContent:
					if strings.TrimSpace(b.Thinking) != "" {
						thinking = append(thinking, b.Thinking)
						switch b.Signature {
						case "reasoning_content", "reasoning", "reasoning_text":
							if reasoningField == "" {
								reasoningField = b.Signature
							}
						}
					}
				}
			}
			out := oaMessage{Role: "assistant"}
			hasToolCalls := false
			for _, blk := range msg.Content {
				tc, ok := blk.(types.ToolCall)
				if !ok {
					continue
				}
				args := string(tc.Arguments)
				if args == "" || args == "null" {
					args = "{}"
				}
				out.ToolCalls = append(out.ToolCalls, oaWireToolCall{
					ID: tc.ID, Type: "function",
					Function: oaToolCallFn{Name: tc.Name, Arguments: args},
				})
				hasToolCalls = true
			}
			if text != "" {
				out.Content = text
			} else if len(thinking) > 0 && reasoningField == "" {
				out.Content = ""
			}
			if reasoningField != "" {
				setReasoning(&out, reasoningField, strings.Join(thinking, "\n"))
			}
			if out.Content == nil && !hasToolCalls {
				continue // skip empty assistant turns (aborted responses)
			}
			msgs = append(msgs, out)
		case *types.ToolResultMessage:
			blocks, err := types.DecodeToolResultContent(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("tool result content: %w", err)
			}
			text, images := toolResultTextAndImages(blocks)
			content := text
			if content == "" {
				if len(images) > 0 {
					content = "(see attached image)"
				} else {
					content = "(no tool output)"
				}
			}
			msgs = append(msgs, oaMessage{
				Role: "tool", Content: content,
				ToolCallID: msg.ToolCallID,
			})
			if len(images) > 0 && imageSupported(model) {
				parts := []oaPart{{Type: "text", Text: "Attached image(s) from tool result:"}}
				parts = append(parts, images...)
				msgs = append(msgs, oaMessage{Role: "user", Content: parts})
			}
		}
	}
	return msgs, nil
}

func setReasoning(m *oaMessage, field, value string) {
	switch field {
	case "reasoning":
		m.Reasoning = value
	case "reasoning_text":
		m.ReasoningText = value
	default:
		m.ReasoningContent = value
	}
}

func userParts(blocks []types.AssistantContent, allowImages bool) (parts []oaPart, hasImage bool) {
	for _, blk := range blocks {
		switch b := blk.(type) {
		case types.TextContent:
			parts = append(parts, oaPart{Type: "text", Text: b.Text})
		case types.ImageContent:
			hasImage = true
			if allowImages {
				parts = append(parts, oaPart{
					Type:     "image_url",
					ImageURL: &oaImageURL{URL: "data:" + b.MimeType + ";base64," + b.Data},
				})
			} else if len(parts) == 0 || parts[len(parts)-1].Text != imageOmittedPlaceholder {
				parts = append(parts, oaPart{Type: "text", Text: imageOmittedPlaceholder})
			}
		}
	}
	return parts, hasImage
}

func toolResultTextAndImages(blocks []types.AssistantContent) (string, []oaPart) {
	var lines []string
	var images []oaPart
	for _, blk := range blocks {
		switch b := blk.(type) {
		case types.TextContent:
			lines = append(lines, b.Text)
		case types.ImageContent:
			images = append(images, oaPart{
				Type:     "image_url",
				ImageURL: &oaImageURL{URL: "data:" + b.MimeType + ";base64," + b.Data},
			})
		}
	}
	return strings.Join(lines, "\n"), images
}

// streaming chunk parsing.

type oaDelta struct {
	Content          *string `json:"content"`
	ReasoningContent string  `json:"reasoning_content"`
	Reasoning        string  `json:"reasoning"`
	ReasoningText    string  `json:"reasoning_text"`
	ToolCalls        []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Function *struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// firstReasoning returns the first non-empty reasoning field value, trying
// ["reasoning_content", "reasoning", "reasoning_text"] in that order.
func firstReasoning(d oaDelta) string {
	switch {
	case d.ReasoningContent != "":
		return d.ReasoningContent
	case d.Reasoning != "":
		return d.Reasoning
	default:
		return d.ReasoningText
	}
}

type oaChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		FinishReason *string          `json:"finish_reason"`
		Delta        oaDelta          `json:"delta"`
		Usage        *json.RawMessage `json:"usage"`
	} `json:"choices"`
	Usage *oaUsage `json:"usage"`
}

func mapFinishReason(reason string) (types.StopReason, string) {
	switch reason {
	case "", "stop", "end":
		return types.StopStop, ""
	case "length":
		return types.StopLength, ""
	case "function_call", "tool_calls":
		return types.StopToolUse, ""
	case "content_filter":
		return types.StopError, "Provider finish_reason: content_filter"
	case "network_error":
		return types.StopError, "Provider finish_reason: network_error"
	default:
		return types.StopError, "Provider finish_reason: " + reason
	}
}

// toolAccum tracks one in-flight tool call block.
type toolAccum struct {
	contentIdx  int
	id          string
	name        string
	partialArgs string
}

// parseStreamingJSON best-effort parses a possibly-truncated JSON object by
// appending missing closers so partial arguments can still be displayed
// incrementally.
func parseStreamingJSON(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	var closers []byte
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			closers = append(closers, '}')
		case c == '[':
			closers = append(closers, ']')
		case c == '}' || c == ']':
			if len(closers) > 0 {
				closers = closers[:len(closers)-1]
			}
		}
	}
	if inStr {
		// Close the string first (LIFO): it was opened last.
		closers = append([]byte{'"'}, closers...)
	}
	trimmed := strings.TrimRight(s, ", \t\r\n")
	fixed := trimmed + string(closers)
	if json.Valid([]byte(fixed)) {
		return json.RawMessage(fixed)
	}
	return nil
}

// Stream implements types.Provider.
func (p *OpenAICompat) Stream(ctx context.Context, model *types.Model, cx *types.Context, opts *types.StreamOptions) <-chan types.AssistantMessageEvent {
	ch := make(chan types.AssistantMessageEvent, eventBuffer)
	go func() {
		defer close(ch)
		em := newEmitter(ch)
		key := p.apiKey(opts)
		if key == "" {
			em.fail(ctx, fmt.Errorf("openai-completions: no API key configured"))
			return
		}
		body, err := p.buildRequest(model, cx, opts)
		if err != nil {
			em.fail(ctx, err)
			return
		}
		base := p.BaseURL
		if model.BaseURL != "" {
			base = model.BaseURL
		}
		url := strings.TrimRight(base, "/") + "/chat/completions"
		headers := map[string]string{"Authorization": "Bearer " + key}
		for k, v := range model.Headers {
			headers[k] = v
		}
		resp, err := postSSE(ctx, p.client(), url, headers, body)
		if err != nil {
			em.fail(ctx, err)
			return
		}

		em.start(model.API, model.Provider, model.ID)
		tools := map[int]*toolAccum{}
		hasFinish := false

		scanErr := scanSSE(resp, func(payload string) error {
			if payload == "[DONE]" {
				return ioEOF()
			}
			var chunk oaChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				return nil // skip malformed chunks
			}
			if em.out.ResponseID == "" {
				em.out.ResponseID = chunk.ID
			}
			if chunk.Model != "" && chunk.Model != model.ID && em.out.ResponseModel == "" {
				em.out.ResponseModel = chunk.Model
			}
			if chunk.Usage != nil {
				u := chunk.Usage.toUsage(model)
				em.out.Usage = u
			}
			if len(chunk.Choices) == 0 {
				return nil
			}
			choice := chunk.Choices[0]
			if choice.Usage != nil {
				var u oaUsage
				if json.Unmarshal(*choice.Usage, &u) == nil {
					em.out.Usage = u.toUsage(model)
				}
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				raw := *choice.FinishReason
				em.out.RawStopReason = raw
				stop, errMsg := mapFinishReason(raw)
				em.out.StopReason = stop
				em.out.ErrorMessage = errMsg
				hasFinish = true
			}
			d := choice.Delta
			if d.Content != nil && *d.Content != "" {
				em.textDelta(*d.Content)
			}
			if v := firstReasoning(d); v != "" {
				em.thinkingDelta(v, "")
			}
			for _, tcd := range d.ToolCalls {
				idx := 0
				if tcd.Index != nil {
					idx = *tcd.Index
				}
				acc := tools[idx]
				if acc == nil {
					acc = &toolAccum{}
					acc.contentIdx = em.appendToolCall(types.ToolCall{Type: types.TypeToolCall})
					tools[idx] = acc
				}
				if tcd.ID != "" && acc.id == "" {
					acc.id = tcd.ID
					tc := em.out.Content[acc.contentIdx].(types.ToolCall)
					tc.ID = tcd.ID
					em.out.Content[acc.contentIdx] = tc
				}
				if tcd.Function != nil {
					if tcd.Function.Name != "" && acc.name == "" {
						acc.name = tcd.Function.Name
						tc := em.out.Content[acc.contentIdx].(types.ToolCall)
						tc.Name = tcd.Function.Name
						em.out.Content[acc.contentIdx] = tc
					}
					if tcd.Function.Arguments != "" {
						acc.partialArgs += tcd.Function.Arguments
						tc := em.out.Content[acc.contentIdx].(types.ToolCall)
						tc.Arguments = parseStreamingJSON(acc.partialArgs)
						em.out.Content[acc.contentIdx] = tc
						em.toolCallDelta(acc.contentIdx, tcd.Function.Arguments, tc.Arguments)
					}
				}
			}
			return nil
		})

		for _, acc := range tools {
			tc := em.out.Content[acc.contentIdx].(types.ToolCall)
			if final := parseStreamingJSON(acc.partialArgs); final != nil {
				tc.Arguments = final
				em.out.Content[acc.contentIdx] = tc
			}
			if tc.Arguments == nil {
				tc.Arguments = json.RawMessage("{}")
				em.out.Content[acc.contentIdx] = tc
			}
			em.toolCallEnd(acc.contentIdx)
		}

		if scanErr != nil && scanErr != errDone {
			em.fail(ctx, scanErr)
			return
		}
		if streamAborted(ctx) {
			em.fail(ctx, context.Canceled)
			return
		}
		if em.out.ErrorMessage != "" && em.out.StopReason == types.StopError {
			em.fail(ctx, fmt.Errorf("%s", em.out.ErrorMessage))
			return
		}
		if !hasFinish {
			em.fail(ctx, fmt.Errorf("Stream ended without finish_reason"))
			return
		}
		em.out.Timestamp = time.Now().UnixMilli()
		em.done(em.out.StopReason)
	}()
	return ch
}

func (p *OpenAICompat) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

// errDone is the sentinel used by the [DONE] payload to end the stream.
var errDone = fmt.Errorf("done")

func ioEOF() error { return errDone }

func (p *OpenAICompat) buildRequest(model *types.Model, cx *types.Context, opts *types.StreamOptions) ([]byte, error) {
	msgs, err := p.ConvertMessages(model, cx)
	if err != nil {
		return nil, err
	}
	req := oaRequest{
		Model:    model.ID,
		Messages: msgs,
		Stream:   true,
	}
	req.StreamOptions = &struct {
		IncludeUsage bool `json:"include_usage"`
	}{IncludeUsage: true}
	if opts != nil {
		if opts.Temperature != nil {
			t := *opts.Temperature
			req.Temperature = &t
		}
		if opts.MaxTokens != nil {
			mx := *opts.MaxTokens
			req.MaxCompletionTokens = &mx
		}
		level := opts.ThinkingLevel
		if model.Reasoning && level != "" && level != types.ThinkingOff {
			req.ReasoningEffort = string(level)
		}
	}
	for _, td := range cx.Tools {
		t := oaTool{Type: "function"}
		t.Function.Name = td.Name
		t.Function.Description = td.Description
		params := td.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		t.Function.Parameters = params
		req.Tools = append(req.Tools, t)
	}
	return json.Marshal(req)
}
