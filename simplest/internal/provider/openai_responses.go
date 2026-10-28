package provider

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// OpenAIResponses streams over the OpenAI Responses protocol
// (POST {baseURL}/responses, SSE). It is a port of pi's openai-responses
// adapter; the chat-completions client stays in OpenAICompat for third parties
// that only implement that protocol. Configure the API key programmatically
// via NewOpenAIResponses or StreamOptions.APIKey.
type OpenAIResponses struct {
	Client  *http.Client
	APIKey  string
	BaseURL string // optional override; model.BaseURL wins when set
}

var _ types.Provider = (*OpenAIResponses)(nil)

func NewOpenAIResponses(apiKey string) *OpenAIResponses {
	return &OpenAIResponses{Client: http.DefaultClient, APIKey: apiKey}
}

func (p *OpenAIResponses) apiKey(opts *types.StreamOptions) string {
	if opts != nil && opts.APIKey != "" {
		return opts.APIKey
	}
	return p.APIKey
}

func (p *OpenAIResponses) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

// responsesMinOutputTokens is the smallest max_output_tokens the API accepts.
const responsesMinOutputTokens = 16

// wire shapes. Input items are heterogeneous (messages, reasoning items,
// function calls), so they are built as maps; reasoning items are replayed
// verbatim from the stored signature.

type orTool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict,omitempty"`
}

type orReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

type orRequest struct {
	Model           string       `json:"model"`
	Input           []any        `json:"input"`
	Stream          bool         `json:"stream"`
	Store           bool         `json:"store"`
	MaxOutputTokens *int64       `json:"max_output_tokens,omitempty"`
	Temperature     *float64     `json:"temperature,omitempty"`
	Tools           []orTool     `json:"tools,omitempty"`
	Reasoning       *orReasoning `json:"reasoning,omitempty"`
	Include         []string     `json:"include,omitempty"`
}

var orIDUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// orNormalizeIDPart sanitizes one half of a "callId|itemId" tool call id to
// the charset and 64-char limit the API enforces.
func orNormalizeIDPart(part string) string {
	s := orIDUnsafe.ReplaceAllString(part, "_")
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.TrimRight(s, "_")
}

func orShortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// orSplitToolCallID splits "callId|itemId"; itemID is "" when absent.
func orSplitToolCallID(id string) (callID, itemID string) {
	callID, itemID, _ = strings.Cut(id, "|")
	return orNormalizeIDPart(callID), itemID
}

type orTextSignature struct {
	V     int    `json:"v"`
	ID    string `json:"id"`
	Phase string `json:"phase,omitempty"`
}

func encodeTextSignatureV1(id, phase string) string {
	b, _ := json.Marshal(orTextSignature{V: 1, ID: id, Phase: phase})
	return string(b)
}

// parseTextSignature accepts the v1 JSON form and falls back to treating a
// plain string as a legacy message id.
func parseTextSignature(sig string) (id, phase string) {
	if sig == "" {
		return "", ""
	}
	if strings.HasPrefix(sig, "{") {
		var s orTextSignature
		if json.Unmarshal([]byte(sig), &s) == nil && s.V == 1 && s.ID != "" {
			if s.Phase == "commentary" || s.Phase == "final_answer" {
				return s.ID, s.Phase
			}
			return s.ID, ""
		}
	}
	return sig, ""
}

// orInstructionRole mirrors pi: reasoning models get "developer" unless the
// compat explicitly disables it.
func orInstructionRole(model *types.Model) string {
	if model == nil || !model.Reasoning {
		return "system"
	}
	if c := model.Compat; c != nil && c.SupportsDeveloperRole != nil && !*c.SupportsDeveloperRole {
		return "system"
	}
	return "developer"
}

func orImagePart(img types.ImageContent) map[string]any {
	return map[string]any{
		"type": "input_image", "detail": "auto",
		"image_url": "data:" + img.MimeType + ";base64," + img.Data,
	}
}

// ConvertMessages renders a Context into Responses API input items.
func (p *OpenAIResponses) ConvertMessages(model *types.Model, cx *types.Context) ([]any, error) {
	input := []any{}
	if cx.SystemPrompt != "" {
		input = append(input, map[string]any{"role": orInstructionRole(model), "content": cx.SystemPrompt})
	}
	msgIndex := 0
	for _, m := range cx.Messages {
		switch msg := m.(type) {
		case *types.UserMessage:
			blocks, err := types.DecodeUserContent(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("user content: %w", err)
			}
			var parts []any
			for _, blk := range blocks {
				switch b := blk.(type) {
				case types.TextContent:
					parts = append(parts, map[string]any{"type": "input_text", "text": b.Text})
				case types.ImageContent:
					if model.SupportsImage() {
						parts = append(parts, orImagePart(b))
					} else if n := len(parts); n == 0 || parts[n-1].(map[string]any)["text"] != imageOmittedPlaceholder {
						parts = append(parts, map[string]any{"type": "input_text", "text": imageOmittedPlaceholder})
					}
				}
			}
			if len(parts) > 0 {
				input = append(input, map[string]any{"role": "user", "content": parts})
			}
		case *types.AssistantMessage:
			sameAPI := msg.Provider == model.Provider && msg.API == model.API
			same := sameAPI && msg.Model == model.ID
			differentModel := sameAPI && msg.Model != model.ID
			var out []any
			textIdx := 0
			for _, blk := range msg.Content {
				switch b := blk.(type) {
				case types.ThinkingContent:
					// Reasoning items are opaque to other models; replay only
					// when the signature came from this exact model.
					if same && b.Signature != "" {
						out = append(out, json.RawMessage(b.Signature))
					}
				case types.TextContent:
					fallback := fmt.Sprintf("msg_pi_%d", msgIndex)
					if textIdx > 0 {
						fallback = fmt.Sprintf("msg_pi_%d_%d", msgIndex, textIdx)
					}
					textIdx++
					id, phase := "", ""
					if same {
						id, phase = parseTextSignature(b.TextSignature)
					}
					if id == "" {
						id = fallback
					} else if len(id) > 64 {
						id = "msg_" + orShortHash(id)
					}
					item := map[string]any{
						"type": "message", "role": "assistant", "status": "completed", "id": id,
						"content": []any{map[string]any{"type": "output_text", "text": b.Text, "annotations": []any{}}},
					}
					if phase != "" {
						item["phase"] = phase
					}
					out = append(out, item)
				case types.ToolCall:
					callID, itemID := orSplitToolCallID(b.ID)
					// Only fc_* item ids are valid for function_call, and ids
					// from another model are dropped to avoid reasoning-pairing
					// validation. Foreign (other provider/api) ids are rehashed.
					switch {
					case differentModel || itemID == "":
						itemID = ""
					case !sameAPI:
						itemID = "fc_" + orShortHash(itemID)
					default:
						itemID = orNormalizeIDPart(itemID)
						if !strings.HasPrefix(itemID, "fc_") {
							itemID = ""
						}
					}
					args := string(b.Arguments)
					if args == "" || args == "null" {
						args = "{}"
					}
					item := map[string]any{"type": "function_call", "call_id": callID, "name": b.Name, "arguments": args}
					if itemID != "" {
						item["id"] = itemID
					}
					if same && b.Namespace != "" {
						item["namespace"] = b.Namespace
					}
					out = append(out, item)
				}
			}
			if len(out) > 0 {
				input = append(input, out...)
			}
		case *types.ToolResultMessage:
			blocks, err := types.DecodeToolResultContent(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("tool result content: %w", err)
			}
			callID, _ := orSplitToolCallID(msg.ToolCallID)
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": callID,
				"output": orToolResultOutput(model, blocks),
			})
		}
		msgIndex++
	}
	return input, nil
}

// orToolResultOutput returns a plain string for text-only results and an
// input_text/input_image array when images should be forwarded.
func orToolResultOutput(model *types.Model, blocks []types.AssistantContent) any {
	var lines []string
	var images []types.ImageContent
	for _, blk := range blocks {
		switch b := blk.(type) {
		case types.TextContent:
			lines = append(lines, b.Text)
		case types.ImageContent:
			images = append(images, b)
		}
	}
	text := strings.Join(lines, "\n")
	hasText := strings.TrimSpace(text) != ""
	if len(images) == 0 || !model.SupportsImage() {
		switch {
		case hasText:
			return text
		case len(images) > 0:
			return "(see attached image)"
		}
		return "(no tool output)"
	}
	var out []any
	if hasText {
		out = append(out, map[string]any{"type": "input_text", "text": text})
	}
	for _, img := range images {
		out = append(out, orImagePart(img))
	}
	return out
}

func (p *OpenAIResponses) buildRequest(model *types.Model, cx *types.Context, opts *types.StreamOptions) ([]byte, error) {
	input, err := p.ConvertMessages(model, cx)
	if err != nil {
		return nil, err
	}
	req := orRequest{Model: model.WireID(), Input: input, Stream: true}
	if opts != nil {
		if opts.MaxTokens != nil && *opts.MaxTokens > 0 {
			mx := *opts.MaxTokens
			if mx < responsesMinOutputTokens {
				mx = responsesMinOutputTokens
			}
			req.MaxOutputTokens = &mx
		}
		if opts.Temperature != nil {
			t := *opts.Temperature
			req.Temperature = &t
		}
		level := opts.ThinkingLevel
		if model.Reasoning && level != "" {
			if !model.SupportsThinkingLevel(level) {
				if len(model.ReasoningEffort) > 0 {
					return nil, fmt.Errorf("unsupported reasoning effort %q for model %q: allowed values are %v", level, model.ID, model.ReasoningEffort)
				}
				return nil, fmt.Errorf("unsupported reasoning effort %q for model %q", level, model.ID)
			}
			wire := string(level)
			var mapped *string
			hasMap := false
			if model.ThinkingLevelMap != nil {
				mapped, hasMap = model.ThinkingLevelMap[level]
			}
			if hasMap && mapped != nil {
				wire = *mapped
			}
			if level == types.ThinkingOff {
				// An explicitly unsupported "off" (nil map entry) sends nothing.
				if !hasMap || mapped != nil {
					if !hasMap {
						wire = "none"
					}
					req.Reasoning = &orReasoning{Effort: wire}
				}
			} else {
				req.Reasoning = &orReasoning{Effort: wire, Summary: "auto"}
				req.Include = []string{"reasoning.encrypted_content"}
			}
		}
	}
	for _, td := range cx.Tools {
		params := td.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		strict := false
		req.Tools = append(req.Tools, orTool{Type: "function", Name: td.Name, Description: td.Description, Parameters: params, Strict: &strict})
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return mergeSamplingJSON(body, mergeSamplingParams(model, opts))
}

// streaming events.

type orItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Namespace        *string         `json:"namespace"`
	Arguments        string          `json:"arguments"`
	Phase            string          `json:"phase"`
	EncryptedContent string          `json:"encrypted_content"`
	Summary          []orText        `json:"summary"`
	Content          []orContentPart `json:"content"`
}

type orText struct {
	Text string `json:"text"`
}

type orContentPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type orResponse struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Output []json.RawMessage `json:"output"`
	Usage  *struct {
		InputTokens        int64 `json:"input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		TotalTokens        int64 `json:"total_tokens"`
		InputTokensDetails *struct {
			CachedTokens     int64 `json:"cached_tokens"`
			CacheWriteTokens int64 `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails *struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

type orEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Delta       string          `json:"delta"`
	Arguments   string          `json:"arguments"`
	Code        string          `json:"code"`
	Message     string          `json:"message"`
	Item        json.RawMessage `json:"item"`
	Response    *orResponse     `json:"response"`
}

// orSlot is one in-flight output item, keyed by output_index.
type orSlot struct {
	kind       string // "thinking" | "text" | "toolCall"
	contentIdx int
	args       toolArgAccumulator
	call       *orItem // set for toolCall slots
}

// mapResponsesStatus maps a terminal response status to a stop reason.
func mapResponsesStatus(status, incompleteReason string) (types.StopReason, string) {
	switch status {
	case "", "completed", "in_progress", "queued":
		return types.StopStop, ""
	case "incomplete":
		if incompleteReason == "max_output_tokens" {
			return types.StopLength, ""
		}
		if incompleteReason != "" {
			return types.StopError, "Response incomplete: " + incompleteReason
		}
		return types.StopError, "Response incomplete without a provider reason"
	case "failed", "cancelled":
		return types.StopError, ""
	}
	return types.StopError, "Unhandled response status: " + status
}

// Stream implements types.Provider.
func (p *OpenAIResponses) Stream(ctx context.Context, model *types.Model, cx *types.Context, opts *types.StreamOptions) <-chan types.AssistantMessageEvent {
	ch := make(chan types.AssistantMessageEvent, eventBuffer)
	go func() {
		defer close(ch)
		em := newEmitter(ch)
		key := p.apiKey(opts)
		if key == "" {
			em.fail(ctx, fmt.Errorf("openai-responses: no API key configured"))
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
		url := strings.TrimRight(base, "/") + "/responses"
		headers := map[string]string{"Authorization": "Bearer " + key}
		applyUserAgent(headers, model.Headers)
		var policy *types.RetryPolicy
		var beforeReq func(req *http.Request, body []byte) ([]byte, error)
		var onStreamEvent func(rawEvent string, rawData []byte)
		if opts != nil {
			policy = opts.RetryPolicy
			beforeReq = opts.BeforeProviderRequest
			onStreamEvent = opts.OnProviderStreamEvent
		}
		resp, err := ExecuteWithRetry(ctx, policy, func() (*http.Response, error) {
			return postSSE(ctx, p.client(), url, headers, body, beforeReq)
		})
		if err != nil {
			em.fail(ctx, err)
			return
		}

		em.start(model.API, model.Provider, model.ID)
		slots := map[int]*orSlot{}
		unfinished := map[int]bool{} // content indexes of tool calls without output_item.done
		sawTerminal := false
		var streamErr error

		getSlot := func(idx int, kind string) *orSlot {
			if s := slots[idx]; s != nil && s.kind == kind {
				return s
			}
			return nil
		}
		textDelta := func(idx int, kind, delta string) {
			s := getSlot(idx, kind)
			if s == nil {
				return
			}
			if kind == "thinking" {
				b := em.out.Content[s.contentIdx].(types.ThinkingContent)
				b.Thinking += delta
				em.out.Content[s.contentIdx] = b
				em.partial(types.EvThinkingDelta, s.contentIdx, delta, "", nil)
				return
			}
			b := em.out.Content[s.contentIdx].(types.TextContent)
			b.Text += delta
			em.out.Content[s.contentIdx] = b
			em.partial(types.EvTextDelta, s.contentIdx, delta, "", nil)
		}
		setArgs := func(s *orSlot, raw json.RawMessage) {
			tc := em.out.Content[s.contentIdx].(types.ToolCall)
			tc.Arguments = raw
			em.out.Content[s.contentIdx] = tc
		}
		create := func(idx int, it *orItem) *orSlot {
			if s := slots[idx]; s != nil {
				return s
			}
			var s *orSlot
			switch it.Type {
			case "reasoning":
				em.out.Content = append(em.out.Content, types.ThinkingContent{Type: types.TypeThinking})
				s = &orSlot{kind: "thinking", contentIdx: len(em.out.Content) - 1}
				em.partial(types.EvThinkingStart, s.contentIdx, "", "", nil)
			case "message":
				if it.Phase == "final_answer" {
					em.out.StopReason = types.StopStop
				}
				em.out.Content = append(em.out.Content, types.TextContent{Type: types.TypeText})
				s = &orSlot{kind: "text", contentIdx: len(em.out.Content) - 1}
				em.partial(types.EvTextStart, s.contentIdx, "", "", nil)
			case "function_call":
				tc := types.ToolCall{Type: types.TypeToolCall, ID: it.CallID + "|" + it.ID, Name: it.Name, Arguments: json.RawMessage("{}")}
				if it.Namespace != nil {
					tc.Namespace = *it.Namespace
				}
				s = &orSlot{kind: "toolCall", call: it}
				s.contentIdx = em.appendToolCall(tc)
				if it.Arguments != "" {
					args, _ := s.args.append(it.Arguments)
					if args != nil {
						setArgs(s, args)
					}
				}
				unfinished[s.contentIdx] = true
			default:
				return nil
			}
			slots[idx] = s
			return s
		}

		scanErr := scanSSE(resp, func(payload string) error {
			if onStreamEvent != nil {
				onStreamEvent("message", []byte(payload))
			}
			if payload == "[DONE]" {
				return ioEOF()
			}
			var ev orEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				return nil // skip malformed events
			}
			var item orItem
			if len(ev.Item) > 0 {
				_ = json.Unmarshal(ev.Item, &item)
			}
			switch ev.Type {
			case "response.created":
				if ev.Response != nil {
					em.out.ResponseID = ev.Response.ID
				}
			case "response.output_item.added":
				create(ev.OutputIndex, &item)
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				textDelta(ev.OutputIndex, "thinking", ev.Delta)
			case "response.reasoning_summary_part.done":
				textDelta(ev.OutputIndex, "thinking", "\n\n")
			case "response.output_text.delta", "response.refusal.delta":
				textDelta(ev.OutputIndex, "text", ev.Delta)
			case "response.function_call_arguments.delta":
				s := getSlot(ev.OutputIndex, "toolCall")
				if s == nil {
					return nil
				}
				args, _ := s.args.append(ev.Delta)
				if args != nil {
					setArgs(s, args)
				}
				em.toolCallDelta(s.contentIdx, ev.Delta, em.out.Content[s.contentIdx].(types.ToolCall).Arguments)
			case "response.function_call_arguments.done":
				s := getSlot(ev.OutputIndex, "toolCall")
				if s == nil {
					return nil
				}
				// The done event carries the full text: emit only the part
				// not yet streamed, or restart when it diverges.
				prev := s.args.rawString()
				if strings.HasPrefix(ev.Arguments, prev) {
					if rest := ev.Arguments[len(prev):]; rest != "" {
						args, _ := s.args.append(rest)
						if args != nil {
							setArgs(s, args)
						}
						em.toolCallDelta(s.contentIdx, rest, em.out.Content[s.contentIdx].(types.ToolCall).Arguments)
					}
				} else {
					s.args = toolArgAccumulator{}
					args, _ := s.args.append(ev.Arguments)
					if args != nil {
						setArgs(s, args)
					}
				}
			case "response.output_item.done":
				if item.Type == "message" && item.Phase == "final_answer" {
					em.out.StopReason = types.StopStop
				}
				s := create(ev.OutputIndex, &item)
				if s == nil {
					return nil
				}
				switch {
				case item.Type == "reasoning" && s.kind == "thinking":
					b := em.out.Content[s.contentIdx].(types.ThinkingContent)
					if t := joinOrTexts(item.Summary); t != "" {
						b.Thinking = t
					} else {
						var parts []string
						for _, c := range item.Content {
							parts = append(parts, c.Text)
						}
						if t := strings.Join(parts, "\n\n"); t != "" {
							b.Thinking = t
						}
					}
					b.Signature = string(ev.Item)
					em.out.Content[s.contentIdx] = b
					em.partial(types.EvThinkingEnd, s.contentIdx, "", b.Thinking, nil)
					delete(slots, ev.OutputIndex)
				case item.Type == "message" && s.kind == "text":
					b := em.out.Content[s.contentIdx].(types.TextContent)
					var sb strings.Builder
					for _, c := range item.Content {
						if c.Type == "output_text" {
							sb.WriteString(c.Text)
						} else {
							sb.WriteString(c.Refusal)
						}
					}
					b.Text = sb.String()
					b.TextSignature = encodeTextSignatureV1(item.ID, item.Phase)
					em.out.Content[s.contentIdx] = b
					em.partial(types.EvTextEnd, s.contentIdx, "", b.Text, nil)
					delete(slots, ev.OutputIndex)
				case item.Type == "function_call" && s.kind == "toolCall":
					raw := s.args.rawString()
					if item.Arguments != "" {
						raw = item.Arguments
					}
					tc := em.out.Content[s.contentIdx].(types.ToolCall)
					tc.Arguments, tc.ArgumentsError = parseToolCallArguments(raw)
					if item.Namespace != nil {
						tc.Namespace = *item.Namespace
					}
					em.out.Content[s.contentIdx] = tc
					delete(unfinished, s.contentIdx)
					em.toolCallEnd(s.contentIdx)
					delete(slots, ev.OutputIndex)
				}
			case "response.completed", "response.incomplete":
				if ev.Response != nil {
					sawTerminal = true
					p.finalize(em, model, ev.Response)
				}
			case "error":
				streamErr = fmt.Errorf("Error Code %s: %s", ev.Code, ev.Message)
				return errDone
			case "response.failed":
				sawTerminal = true
				msg := "Unknown error (no error details in response)"
				if r := ev.Response; r != nil {
					em.out.RawStopReason = r.Status
					switch {
					case r.Error != nil:
						code, m := r.Error.Code, r.Error.Message
						if code == "" {
							code = "unknown"
						}
						if m == "" {
							m = "no message"
						}
						msg = code + ": " + m
					case r.IncompleteDetails != nil && r.IncompleteDetails.Reason != "":
						msg = "incomplete: " + r.IncompleteDetails.Reason
					}
				}
				streamErr = fmt.Errorf("%s", msg)
				return errDone
			}
			return nil
		})

		if streamErr != nil {
			em.fail(ctx, streamErr)
			return
		}
		if scanErr != nil && scanErr != errDone {
			em.fail(ctx, scanErr)
			return
		}
		if streamAborted(ctx) {
			em.fail(ctx, context.Canceled)
			return
		}
		if !sawTerminal {
			em.fail(ctx, fmt.Errorf("OpenAI Responses stream ended before a terminal response event"))
			return
		}
		if em.out.StopReason == types.StopError {
			msg := em.out.ErrorMessage
			if msg == "" {
				msg = "An unknown error occurred"
			}
			em.fail(ctx, fmt.Errorf("%s", msg))
			return
		}
		if em.out.StopReason == types.StopToolUse {
			for idx := range em.out.Content {
				if unfinished[idx] {
					tc := em.out.Content[idx].(types.ToolCall)
					em.fail(ctx, fmt.Errorf("OpenAI Responses stream completed with an unfinished tool call: %s (%s)", tc.Name, tc.ID))
					return
				}
			}
		}
		em.done(em.out.StopReason)
	}()
	return ch
}

func joinOrTexts(ts []orText) string {
	parts := make([]string, 0, len(ts))
	for _, t := range ts {
		parts = append(parts, t.Text)
	}
	return strings.Join(parts, "\n\n")
}

// finalize applies a terminal response (completed/incomplete): response id,
// usage, cost, stop reason, and encrypted reasoning backfill.
func (p *OpenAIResponses) finalize(em *emitter, model *types.Model, r *orResponse) {
	// Backfill encrypted_content that only arrives on the terminal response
	// into the stored reasoning signatures (pi backfillReasoningSignatures).
	for _, raw := range r.Output {
		var it orItem
		if json.Unmarshal(raw, &it) != nil || it.Type != "reasoning" || it.EncryptedContent == "" {
			continue
		}
		for i, blk := range em.out.Content {
			b, ok := blk.(types.ThinkingContent)
			if !ok || b.Signature == "" {
				continue
			}
			var stored map[string]json.RawMessage
			if json.Unmarshal([]byte(b.Signature), &stored) != nil {
				continue
			}
			var id string
			_ = json.Unmarshal(stored["id"], &id)
			var enc string
			_ = json.Unmarshal(stored["encrypted_content"], &enc)
			if id != it.ID || enc != "" {
				continue
			}
			stored["encrypted_content"], _ = json.Marshal(it.EncryptedContent)
			if nb, err := json.Marshal(stored); err == nil {
				b.Signature = string(nb)
				em.out.Content[i] = b
			}
		}
	}
	if r.ID != "" {
		em.out.ResponseID = r.ID
	}
	if u := r.Usage; u != nil {
		var cached, write int64
		if d := u.InputTokensDetails; d != nil {
			cached, write = d.CachedTokens, d.CacheWriteTokens
		}
		in := u.InputTokens - cached - write
		if in < 0 {
			in = 0
		}
		reasoning := int64(0)
		if d := u.OutputTokensDetails; d != nil {
			reasoning = d.ReasoningTokens
		}
		em.out.Usage = types.Usage{
			Input: in, Output: u.OutputTokens, CacheRead: cached, CacheWrite: write,
			Reasoning: &reasoning, TotalTokens: u.TotalTokens,
		}
	}
	CalculateCost(model, &em.out.Usage)
	reason := ""
	if r.IncompleteDetails != nil {
		reason = r.IncompleteDetails.Reason
	}
	em.out.RawStopReason = r.Status
	if reason != "" {
		em.out.RawStopReason = r.Status + "." + reason
	}
	em.out.StopReason, em.out.ErrorMessage = mapResponsesStatus(r.Status, reason)
	if em.out.StopReason == types.StopStop {
		for _, blk := range em.out.Content {
			if _, ok := blk.(types.ToolCall); ok {
				em.out.StopReason = types.StopToolUse
				break
			}
		}
	}
}
