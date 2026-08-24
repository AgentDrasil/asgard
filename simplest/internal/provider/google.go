package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// Gemini streams over the Google Generative AI protocol via the official
// google.golang.org/genai SDK. Only Gemini 3 series and Gemma 4 models are
// supported; any other model ID fails the stream.
type Gemini struct {
	Client *http.Client
	APIKey string
}

var _ types.Provider = (*Gemini)(nil)

func NewGemini(apiKey string) *Gemini {
	return &Gemini{Client: http.DefaultClient, APIKey: apiKey}
}

func (p *Gemini) apiKey(opts *types.StreamOptions) string {
	if opts != nil && opts.APIKey != "" {
		return opts.APIKey
	}
	return p.APIKey
}

// model-family helpers.

var (
	gemini3ProRe   = regexp.MustCompile(`^gemini-(3(\.\d+)?-)?pro(-(preview|latest))?$`)
	gemini3LiteRe  = regexp.MustCompile(`^gemini-(3(\.\d+)?-)?flash-lite(-(preview|latest))?$|^gemini-flash-lite-latest$`)
	gemini3FlashRe = regexp.MustCompile(`^gemini-(3(\.\d+)?-)?flash(-(preview|latest))?$|^gemini-flash-latest$`)
	gemma4Re       = regexp.MustCompile(`^gemma-4-(\d+b(-a\d+b)?)-it$`)
)

// gemini3Family returns "pro", "lite", "flash" or "" for gemini-3 style
// models. flash-lite must be tested before flash.
func gemini3Family(modelID string) string {
	if gemini3ProRe.MatchString(modelID) {
		return "pro"
	}
	if gemini3LiteRe.MatchString(modelID) {
		return "lite"
	}
	if gemini3FlashRe.MatchString(modelID) {
		return "flash"
	}
	return ""
}

func isGemma4Model(modelID string) bool { return gemma4Re.MatchString(modelID) }

// supportedModel reports whether the model ID belongs to a supported family
// (Gemini 3 series or Gemma 4).
func supportedModel(modelID string) bool {
	return gemini3Family(modelID) != "" || isGemma4Model(modelID)
}

// requiresToolCallID reports whether the model echoes tool call ids in
// functionCall/functionResponse parts.
func requiresToolCallID(modelID string) bool {
	return strings.HasPrefix(modelID, "claude-") || strings.HasPrefix(modelID, "gpt-oss-") ||
		gemini3Family(modelID) != ""
}

func sameModel(am *types.AssistantMessage, model *types.Model) bool {
	return am.Provider == model.Provider && am.API == model.API && am.Model == model.ID
}

// thinking configuration.

// googleThinkingConfig resolves the thinking config for a request based on
// the requested level and model family. Gemma 4 supports no thinking config;
// unknown families are rejected by the caller.
func googleThinkingConfig(level types.ThinkingLevel, modelID string) *genai.ThinkingConfig {
	family := gemini3Family(modelID)
	switch family {
	case "":
		return nil // gemma: no thinking control
	case "pro":
		// gemini-3 pro supports LOW/HIGH only and cannot disable thinking.
		lvl := genai.ThinkingLevelHigh
		switch level {
		case types.ThinkingMinimal, types.ThinkingLow, types.ThinkingOff:
			lvl = genai.ThinkingLevelLow
		}
		return &genai.ThinkingConfig{IncludeThoughts: true, ThinkingLevel: lvl}
	case "flash":
		// flash supports LOW/MEDIUM/HIGH; MINIMAL is not accepted.
		var lvl genai.ThinkingLevel
		switch level {
		case types.ThinkingMinimal, types.ThinkingLow, types.ThinkingOff:
			lvl = genai.ThinkingLevelLow
		case types.ThinkingMedium:
			lvl = genai.ThinkingLevelMedium
		default:
			lvl = genai.ThinkingLevelHigh
		}
		return &genai.ThinkingConfig{IncludeThoughts: true, ThinkingLevel: lvl}
	default:
		// flash-lite supports MINIMAL/LOW/MEDIUM/HIGH.
		var lvl genai.ThinkingLevel
		switch level {
		case types.ThinkingMinimal, types.ThinkingOff:
			lvl = genai.ThinkingLevelMinimal
		case types.ThinkingLow:
			lvl = genai.ThinkingLevelLow
		case types.ThinkingMedium:
			lvl = genai.ThinkingLevelMedium
		default:
			lvl = genai.ThinkingLevelHigh
		}
		return &genai.ThinkingConfig{IncludeThoughts: true, ThinkingLevel: lvl}
	}
}

// message conversion.

func (p *Gemini) ConvertMessages(model *types.Model, cx *types.Context) ([]*genai.Content, error) {
	var contents []*genai.Content
	withToolCallID := requiresToolCallID(model.ID)

	addParts := func(role string, parts ...*genai.Part) {
		contents = append(contents, &genai.Content{Role: role, Parts: parts})
	}

	for _, m := range cx.Messages {
		switch msg := m.(type) {
		case *types.UserMessage:
			blocks, err := types.DecodeUserContent(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("user content: %w", err)
			}
			parts := make([]*genai.Part, 0, len(blocks))
			for _, blk := range blocks {
				switch b := blk.(type) {
				case types.TextContent:
					parts = append(parts, &genai.Part{Text: b.Text})
				case types.ImageContent:
					parts = append(parts, &genai.Part{InlineData: &genai.Blob{MIMEType: b.MimeType, Data: []byte(b.Data)}})
				}
			}
			if len(parts) > 0 {
				addParts("user", parts...)
			}
		case *types.AssistantMessage:
			same := sameModel(msg, model)
			parts := make([]*genai.Part, 0, len(msg.Content))
			for _, blk := range msg.Content {
				switch b := blk.(type) {
				case types.ThinkingContent:
					if !same {
						continue // cross-model thinking is dropped entirely
					}
					if strings.TrimSpace(b.Thinking) == "" && b.Signature == "" {
						continue
					}
					sig := []byte(nil)
					if b.Signature != "" {
						sig = []byte(b.Signature)
					} else if b.Redacted {
						continue // redacted payloads without a reusable signature are unusable
					}
					parts = append(parts, &genai.Part{Text: b.Thinking, Thought: true, ThoughtSignature: sig})
				case types.TextContent:
					if b.Text == "" {
						continue
					}
					parts = append(parts, &genai.Part{Text: b.Text})
				case types.ToolCall:
					args := map[string]any{}
					if len(b.Arguments) > 0 && string(b.Arguments) != "null" {
						if err := json.Unmarshal(b.Arguments, &args); err != nil {
							return nil, fmt.Errorf("tool call %s arguments: %w", b.Name, err)
						}
					}
					call := &genai.FunctionCall{Name: b.Name, Args: args}
					if withToolCallID && b.ID != "" {
						call.ID = b.ID
					}
					part := &genai.Part{FunctionCall: call}
					if b.Signature != "" {
						// Gemini 3 requires thought signatures to be echoed
						// back on functionCall parts.
						part.ThoughtSignature = []byte(b.Signature)
					}
					parts = append(parts, part)
				}
			}
			if len(parts) > 0 {
				addParts("model", parts...)
			}
		case *types.ToolResultMessage:
			blocks, err := types.DecodeToolResultContent(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("tool result content: %w", err)
			}
			text, images := geminiToolResultBlocks(blocks)
			resp := map[string]any{"output": text}
			if msg.IsError {
				resp = map[string]any{"error": text}
			}
			fr := &genai.FunctionResponse{Name: msg.ToolName, Response: resp}
			if withToolCallID && msg.ToolCallID != "" {
				fr.ID = msg.ToolCallID
			}
			// Merge consecutive function responses into one user entry.
			n := len(contents)
			if n > 0 && contents[n-1].Role == "user" && hasFunctionResponse(contents[n-1]) {
				contents[n-1].Parts = append(contents[n-1].Parts, &genai.Part{FunctionResponse: fr})
			} else {
				addParts("user", &genai.Part{FunctionResponse: fr})
			}
			if len(images) > 0 && imageSupported(model) {
				imgParts := []*genai.Part{{Text: "Tool result image:"}}
				for _, im := range images {
					imgParts = append(imgParts, &genai.Part{InlineData: &genai.Blob{MIMEType: im.MimeType, Data: []byte(im.Data)}})
				}
				if gemini3Family(model.ID) != "" {
					last := len(contents) - 1
					contents[last].Parts = append(contents[last].Parts, imgParts...)
				} else {
					addParts("user", imgParts...)
				}
			}
		}
	}
	return contents, nil
}

func hasFunctionResponse(c *genai.Content) bool {
	for _, p := range c.Parts {
		if p.FunctionResponse != nil {
			return true
		}
	}
	return false
}

func geminiToolResultBlocks(blocks []types.AssistantContent) (string, []types.ImageContent) {
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
	if text == "" && len(images) > 0 {
		text = "(see attached image)"
	}
	return text, images
}

// response parsing helpers.

func mapFinishReasonGoogle(reason genai.FinishReason) types.StopReason {
	switch reason {
	case genai.FinishReasonStop:
		return types.StopStop
	case genai.FinishReasonMaxTokens:
		return types.StopLength
	default:
		return types.StopError
	}
}

// Stream implements types.Provider.
func (p *Gemini) Stream(ctx context.Context, model *types.Model, cx *types.Context, opts *types.StreamOptions) <-chan types.AssistantMessageEvent {
	ch := make(chan types.AssistantMessageEvent, eventBuffer)
	go func() {
		defer close(ch)
		em := newEmitter(ch)
		key := p.apiKey(opts)
		if key == "" {
			em.fail(ctx, fmt.Errorf("google-gemini: no API key configured"))
			return
		}
		if !supportedModel(model.ID) {
			em.fail(ctx, fmt.Errorf("google-gemini: unsupported model %q (only gemini-3 series and gemma-4 are supported)", model.ID))
			return
		}
		contents, config, err := p.buildRequest(model, cx, opts)
		if err != nil {
			em.fail(ctx, err)
			return
		}
		cc := &genai.ClientConfig{
			APIKey:     key,
			Backend:    genai.BackendGeminiAPI,
			HTTPClient: p.client(),
		}
		if model.BaseURL != "" {
			cc.HTTPOptions.BaseURL = model.BaseURL
		}
		client, err := genai.NewClient(ctx, cc)
		if err != nil {
			em.fail(ctx, err)
			return
		}

		em.start(model.API, model.Provider, model.ID)
		toolCallCounter := 0
		finishReason := genai.FinishReasonUnspecified

		for resp, err := range client.Models.GenerateContentStream(ctx, model.ID, contents, config) {
			if err != nil {
				em.fail(ctx, err)
				return
			}
			if em.out.ResponseID == "" && resp.ResponseID != "" {
				em.out.ResponseID = resp.ResponseID
			}
			if um := resp.UsageMetadata; um != nil {
				input := int64(um.PromptTokenCount) - int64(um.CachedContentTokenCount)
				if input < 0 {
					input = 0
				}
				thoughts := int64(um.ThoughtsTokenCount)
				usage := types.Usage{
					Input:       input,
					Output:      int64(um.CandidatesTokenCount) + thoughts,
					CacheRead:   int64(um.CachedContentTokenCount),
					Reasoning:   &thoughts,
					TotalTokens: int64(um.TotalTokenCount),
				}
				CalculateCost(model, &usage)
				em.out.Usage = usage
			}
			if len(resp.Candidates) == 0 {
				continue
			}
			cand := resp.Candidates[0]
			if cand.FinishReason != "" && cand.FinishReason != genai.FinishReasonUnspecified {
				finishReason = cand.FinishReason
			}
			if cand.Content == nil {
				continue
			}
			for _, part := range cand.Content.Parts {
				if part.Text != "" {
					if part.Thought {
						em.thinkingDelta(part.Text, string(part.ThoughtSignature))
					} else {
						em.textDelta(part.Text)
					}
					continue
				}
				if part.FunctionCall != nil {
					fc := part.FunctionCall
					id := fc.ID
					dup := id == ""
					for _, blk := range em.out.Content {
						if tc, ok := blk.(types.ToolCall); ok && id != "" && tc.ID == id {
							dup = true
							break
						}
					}
					if dup {
						toolCallCounter++
						id = fmt.Sprintf("%s_%d_%d", fc.Name, time.Now().UnixMilli(), toolCallCounter)
					}
					args := json.RawMessage("{}")
					if len(fc.Args) > 0 {
						if raw, merr := json.Marshal(fc.Args); merr == nil {
							args = raw
						}
					}
					idx := em.appendToolCall(types.ToolCall{
						Type: types.TypeToolCall, ID: id, Name: fc.Name, Arguments: args,
						Signature: string(part.ThoughtSignature),
					})
					em.toolCallDelta(idx, string(args), args)
					em.toolCallEnd(idx)
				}
			}
		}

		if streamAborted(ctx) {
			em.fail(ctx, context.Canceled)
			return
		}
		if finishReason == genai.FinishReasonUnspecified {
			em.fail(ctx, fmt.Errorf("google stream ended without a finish reason"))
			return
		}
		stop := mapFinishReasonGoogle(finishReason)
		if stop == types.StopError {
			em.fail(ctx, fmt.Errorf("provider finishReason: %s", finishReason))
			return
		}
		if stop == types.StopStop {
			for _, blk := range em.out.Content {
				if _, ok := blk.(types.ToolCall); ok {
					stop = types.StopToolUse
					break
				}
			}
		}
		em.out.RawStopReason = string(finishReason)
		em.out.Timestamp = time.Now().UnixMilli()
		em.done(stop)
	}()
	return ch
}

func (p *Gemini) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

func (p *Gemini) buildRequest(model *types.Model, cx *types.Context, opts *types.StreamOptions) ([]*genai.Content, *genai.GenerateContentConfig, error) {
	contents, err := p.ConvertMessages(model, cx)
	if err != nil {
		return nil, nil, err
	}
	config := &genai.GenerateContentConfig{}
	if cx.SystemPrompt != "" {
		config.SystemInstruction = &genai.Content{Role: "user", Parts: []*genai.Part{{Text: cx.SystemPrompt}}}
	}
	if opts != nil {
		if opts.Temperature != nil {
			t := float32(*opts.Temperature)
			config.Temperature = &t
		}
		if opts.MaxTokens != nil {
			config.MaxOutputTokens = int32(*opts.MaxTokens)
		}
		level := opts.ThinkingLevel
		if model.Reasoning && level != "" {
			if len(model.ReasoningEffort) > 0 && !model.SupportsReasoningEffort(string(level)) {
				return nil, nil, fmt.Errorf("unsupported reasoning effort %q for model %q: allowed values are %v", level, model.ID, model.ReasoningEffort)
			}
			config.ThinkingConfig = googleThinkingConfig(level, model.ID)
		}
	} else if model.Reasoning {
		config.ThinkingConfig = &genai.ThinkingConfig{IncludeThoughts: true}
	}
	if len(cx.Tools) > 0 {
		decls := make([]*genai.FunctionDeclaration, 0, len(cx.Tools))
		for _, td := range cx.Tools {
			params := td.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			var schema map[string]any
			if err := json.Unmarshal(params, &schema); err != nil {
				return nil, nil, fmt.Errorf("tool %s parameters: %w", td.Name, err)
			}
			decls = append(decls, &genai.FunctionDeclaration{
				Name:                 td.Name,
				Description:          td.Description,
				ParametersJsonSchema: schema,
			})
		}
		config.Tools = []*genai.Tool{{FunctionDeclarations: decls}}
	}
	return contents, config, nil
}
