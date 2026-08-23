package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AgentDrasil/asgard/simplest/types"
)

const defaultGeminiBase = "https://generativelanguage.googleapis.com/v1beta"

// Gemini streams over the Google Generative AI protocol
// (POST {base}/models/{id}:streamGenerateContent?alt=sse, x-goog-api-key).
// When Model.BaseURL is set it must already include the API version path.
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

// wire shapes.

type gInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFunctionCall struct {
	Name string          `json:"name"`
	ID   string          `json:"id,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

type gFunctionResponse struct {
	Name     string `json:"name"`
	Response any    `json:"response"`
	ID       string `json:"id,omitempty"`
}

type gPart struct {
	Text             string             `json:"text,omitempty"`
	Thought          bool               `json:"thought,omitempty"`
	ThoughtSignature string             `json:"thoughtSignature,omitempty"`
	InlineData       *gInline           `json:"inlineData,omitempty"`
	FunctionCall     *gFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *gFunctionResponse `json:"functionResponse,omitempty"`
}

type gContent struct {
	Role  string  `json:"role,omitempty"` // "user" | "model"
	Parts []gPart `json:"parts"`
}

type gFuncDecl struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type gTools struct {
	FunctionDeclarations []gFuncDecl `json:"functionDeclarations"`
}

type gThinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	ThinkingBudget  *int64 `json:"thinkingBudget,omitempty"`
}

type gGenerationConfig struct {
	Temperature     *float64         `json:"temperature,omitempty"`
	MaxOutputTokens *int64           `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *gThinkingConfig `json:"thinkingConfig,omitempty"`
}

type gRequest struct {
	Contents          []gContent         `json:"contents"`
	Tools             []gTools           `json:"tools,omitempty"`
	SystemInstruction *gContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *gGenerationConfig `json:"generationConfig,omitempty"`
}

// model-family helpers (thinking-level resolution and tool-call-id
// requirement tables per model family).

var (
	gemini3ProRe    = regexp.MustCompile(`gemini-3(\.\d+)?-pro`)
	gemini3FlashRe  = regexp.MustCompile(`gemini-3(\.\d+)?-flash|gemini-flash-latest|gemini-flash-lite-latest`)
	geminiMajorRe   = regexp.MustCompile(`^gemini(?:-live)?-(\d+)`)
	gemma4Re        = regexp.MustCompile(`gemma-?4`)
	base64PayloadRe = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)
)

// gemini3Family returns "pro", "flash" or "" for gemini-3 style models.
func gemini3Family(modelID string) string {
	if gemini3ProRe.MatchString(modelID) {
		return "pro"
	}
	if gemini3FlashRe.MatchString(modelID) {
		return "flash"
	}
	return ""
}

// requiresToolCallID reports whether the model echoes tool call ids in
// functionCall/functionResponse parts.
func requiresToolCallID(modelID string) bool {
	if strings.HasPrefix(modelID, "claude-") || strings.HasPrefix(modelID, "gpt-oss-") {
		return true
	}
	if m := geminiMajorRe.FindStringSubmatch(modelID); m != nil {
		major, err := strconv.Atoi(m[1])
		return err == nil && major >= 3
	}
	return false
}

func validB64(s string) bool {
	return s != "" && len(s)%4 == 0 && base64PayloadRe.MatchString(s)
}

func sameModel(am *types.AssistantMessage, model *types.Model) bool {
	return am.Provider == model.Provider && am.API == model.API && am.Model == model.ID
}

// message conversion.

func (p *Gemini) ConvertMessages(model *types.Model, cx *types.Context) ([]gContent, error) {
	var contents []gContent
	withToolCallID := requiresToolCallID(model.ID)

	addParts := func(role string, parts ...gPart) {
		contents = append(contents, gContent{Role: role, Parts: parts})
	}

	for _, m := range cx.Messages {
		switch msg := m.(type) {
		case *types.UserMessage:
			blocks, err := types.DecodeUserContent(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("user content: %w", err)
			}
			parts := make([]gPart, 0, len(blocks))
			for _, blk := range blocks {
				switch b := blk.(type) {
				case types.TextContent:
					parts = append(parts, gPart{Text: b.Text})
				case types.ImageContent:
					parts = append(parts, gPart{InlineData: &gInline{MimeType: b.MimeType, Data: b.Data}})
				}
			}
			if len(parts) > 0 {
				addParts("user", parts...)
			}
		case *types.AssistantMessage:
			same := sameModel(msg, model)
			parts := make([]gPart, 0, len(msg.Content))
			for _, blk := range msg.Content {
				switch b := blk.(type) {
				case types.ThinkingContent:
					if !same {
						continue // cross-model thinking is dropped entirely
					}
					if strings.TrimSpace(b.Thinking) == "" && b.Signature == "" {
						continue
					}
					sig := ""
					if validB64(b.Signature) {
						sig = b.Signature
					} else if b.Redacted {
						continue // redacted payloads without a reusable signature are unusable
					}
					parts = append(parts, gPart{Text: b.Thinking, Thought: true, ThoughtSignature: sig})
				case types.TextContent:
					if b.Text == "" {
						continue
					}
					parts = append(parts, gPart{Text: b.Text})
				case types.ToolCall:
					args := b.Arguments
					if len(args) == 0 || string(args) == "null" {
						args = json.RawMessage(`{}`)
					}
					call := &gFunctionCall{Name: b.Name, Args: args}
					if withToolCallID && b.ID != "" {
						call.ID = b.ID
					}
					parts = append(parts, gPart{FunctionCall: call})
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
			resp := any(map[string]any{"output": text})
			if msg.IsError {
				resp = map[string]any{"error": text}
			}
			fr := &gFunctionResponse{Name: msg.ToolName, Response: resp}
			if withToolCallID && msg.ToolCallID != "" {
				fr.ID = msg.ToolCallID
			}
			// Merge consecutive function responses into one user entry.
			n := len(contents)
			if n > 0 && contents[n-1].Role == "user" && hasFunctionResponse(contents[n-1]) {
				contents[n-1].Parts = append(contents[n-1].Parts, gPart{FunctionResponse: fr})
			} else {
				addParts("user", gPart{FunctionResponse: fr})
			}
			if len(images) > 0 && imageSupported(model) {
				imgParts := []gPart{{Text: "Tool result image:"}}
				for _, im := range images {
					imgParts = append(imgParts, gPart{InlineData: &gInline{MimeType: im.MimeType, Data: im.Data}})
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

func hasFunctionResponse(c gContent) bool {
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

// response parsing.

type gChunk struct {
	ResponseID string `json:"responseId"`
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text             *string `json:"text"`
				Thought          bool    `json:"thought"`
				ThoughtSignature string  `json:"thoughtSignature"`
				FunctionCall     *struct {
					Name string          `json:"name"`
					ID   string          `json:"id"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int64 `json:"promptTokenCount"`
		CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
		ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
		CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
		TotalTokenCount         int64 `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func mapFinishReasonGoogle(reason string) types.StopReason {
	switch reason {
	case "STOP":
		return types.StopStop
	case "MAX_TOKENS":
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
			em.fail(ctx, fmt.Errorf("google-generative-ai: no API key configured"))
			return
		}
		body, err := p.buildRequest(model, cx, opts)
		if err != nil {
			em.fail(ctx, err)
			return
		}
		base := defaultGeminiBase
		if model.BaseURL != "" {
			base = strings.TrimRight(model.BaseURL, "/")
		}
		url := base + "/models/" + model.ID + ":streamGenerateContent?alt=sse"
		headers := map[string]string{"x-goog-api-key": key}
		for k, v := range model.Headers {
			headers[k] = v
		}
		resp, err := postSSE(ctx, p.client(), url, headers, body)
		if err != nil {
			em.fail(ctx, err)
			return
		}

		em.start(model.API, model.Provider, model.ID)
		toolCallCounter := 0
		finishReason := ""

		scanErr := scanSSE(resp, func(payload string) error {
			var chunk gChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				return nil // skip malformed chunks
			}
			if em.out.ResponseID == "" {
				em.out.ResponseID = chunk.ResponseID
			}
			if um := chunk.UsageMetadata; um != nil {
				input := um.PromptTokenCount - um.CachedContentTokenCount
				if input < 0 {
					input = 0
				}
				thoughts := um.ThoughtsTokenCount
				usage := types.Usage{
					Input:       input,
					Output:      um.CandidatesTokenCount + um.ThoughtsTokenCount,
					CacheRead:   um.CachedContentTokenCount,
					Reasoning:   &thoughts,
					TotalTokens: um.TotalTokenCount,
				}
				CalculateCost(model, &usage)
				em.out.Usage = usage
			}
			if len(chunk.Candidates) == 0 {
				return nil
			}
			cand := chunk.Candidates[0]
			for _, part := range cand.Content.Parts {
				if part.Text != nil {
					if part.Thought {
						em.thinkingDelta(*part.Text, part.ThoughtSignature)
					} else {
						em.textDelta(*part.Text)
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
					args := fc.Args
					if len(args) == 0 || string(args) == "null" {
						args = json.RawMessage(`{}`)
					}
					idx := em.appendToolCall(types.ToolCall{
						Type: types.TypeToolCall, ID: id, Name: fc.Name, Arguments: args,
					})
					em.toolCallDelta(idx, string(args), args)
					em.toolCallEnd(idx)
				}
			}
			if cand.FinishReason != "" {
				finishReason = cand.FinishReason
			}
			return nil
		})

		if scanErr != nil {
			em.fail(ctx, scanErr)
			return
		}
		if streamAborted(ctx) {
			em.fail(ctx, context.Canceled)
			return
		}
		if finishReason == "" {
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
		em.out.RawStopReason = finishReason
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

func (p *Gemini) buildRequest(model *types.Model, cx *types.Context, opts *types.StreamOptions) ([]byte, error) {
	contents, err := p.ConvertMessages(model, cx)
	if err != nil {
		return nil, err
	}
	req := gRequest{Contents: contents}
	if cx.SystemPrompt != "" {
		req.SystemInstruction = &gContent{Parts: []gPart{{Text: cx.SystemPrompt}}}
	}
	cfg := &gGenerationConfig{}
	hasCfg := false
	if opts != nil {
		if opts.Temperature != nil {
			t := *opts.Temperature
			cfg.Temperature = &t
			hasCfg = true
		}
		if opts.MaxTokens != nil {
			mx := *opts.MaxTokens
			cfg.MaxOutputTokens = &mx
			hasCfg = true
		}
		level := opts.ThinkingLevel
		if model.Reasoning && level != "" {
			cfg.ThinkingConfig = googleThinkingConfig(level, model.ID)
			hasCfg = true
		}
	} else if model.Reasoning {
		cfg.ThinkingConfig = &gThinkingConfig{IncludeThoughts: true}
		hasCfg = true
	}
	if hasCfg {
		req.GenerationConfig = cfg
	}
	if len(cx.Tools) > 0 {
		decls := make([]gFuncDecl, 0, len(cx.Tools))
		for _, td := range cx.Tools {
			params := td.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			decls = append(decls, gFuncDecl{Name: td.Name, Description: td.Description, ParametersJSONSchema: params})
		}
		req.Tools = []gTools{{FunctionDeclarations: decls}}
	}
	return json.Marshal(req)
}

func isGemma4Model(modelID string) bool { return gemma4Re.MatchString(modelID) }

// googleThinkingConfig resolves the thinking config for a request
// based on the requested thinking level and model ID.
func googleThinkingConfig(level types.ThinkingLevel, modelID string) *gThinkingConfig {
	if level == types.ThinkingOff {
		return disabledThinkingConfig(modelID)
	}
	return &gThinkingConfig{IncludeThoughts: true, ThinkingLevel: apiThinkingLevel(level, modelID)}
}

func disabledThinkingConfig(modelID string) *gThinkingConfig {
	switch {
	case gemini3Family(modelID) == "pro":
		return &gThinkingConfig{ThinkingLevel: "LOW"}
	case gemini3Family(modelID) == "flash", isGemma4Model(modelID):
		return &gThinkingConfig{ThinkingLevel: "MINIMAL"}
	default:
		budget := int64(0)
		return &gThinkingConfig{ThinkingBudget: &budget}
	}
}

func apiThinkingLevel(level types.ThinkingLevel, modelID string) string {
	family := gemini3Family(modelID)
	gemma := isGemma4Model(modelID)
	switch level {
	case types.ThinkingMinimal:
		if family == "pro" || gemma {
			if family == "pro" {
				return "LOW"
			}
			return "MINIMAL"
		}
		return "MINIMAL"
	case types.ThinkingLow:
		if family == "pro" || gemma {
			if gemma {
				return "MINIMAL"
			}
			return "LOW"
		}
		return "LOW"
	case types.ThinkingMedium:
		if family == "pro" || gemma {
			return "HIGH"
		}
		return "MEDIUM"
	default:
		return "HIGH"
	}
}
