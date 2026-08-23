package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// --- model family classification ---

func TestGemini3Family(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"gemini-pro-latest", "pro"},
		{"gemini-3-pro", "pro"},
		{"gemini-3-pro-latest", "pro"},
		{"gemini-3.1-pro-preview", "pro"},
		{"gemini-3.5-pro-preview", "pro"},
		{"gemini-3.7-flash", "flash"},
		{"gemini-3.6-flash", "flash"},
		{"gemini-3.5-flash", "flash"},
		{"gemini-3-flash-preview", "flash"},
		{"gemini-flash-latest", "flash"},
		{"gemini-flash-lite-latest", "lite"},
		{"gemini-3.1-flash-lite", "lite"},
		{"gemini-3.5-flash-lite", "lite"},
		{"gemini-2.5-flash", ""},
		{"gpt-4o", ""},
	}
	for _, c := range cases {
		if got := gemini3Family(c.id); got != c.want {
			t.Errorf("gemini3Family(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

func TestIsGemma4Model(t *testing.T) {
	yes := []string{"gemma-4-26b-a4b-it", "gemma-4-31b-it"}
	no := []string{"gemma-3-27b-it", "gemma-2-9b-it", "gemini-3-flash"}
	for _, id := range yes {
		if !isGemma4Model(id) {
			t.Errorf("isGemma4Model(%q) = false", id)
		}
	}
	for _, id := range no {
		if isGemma4Model(id) {
			t.Errorf("isGemma4Model(%q) = true", id)
		}
	}
}

func TestSupportedModel(t *testing.T) {
	for _, id := range []string{"gemini-3.7-flash", "gemini-3.1-pro-preview", "gemma-4-31b-it"} {
		if !supportedModel(id) {
			t.Errorf("supportedModel(%q) = false", id)
		}
	}
	for _, id := range []string{"gemini-2.5-flash", "gemini-2.5-pro", "claude-sonnet-4"} {
		if supportedModel(id) {
			t.Errorf("supportedModel(%q) = true", id)
		}
	}
}

func TestRequiresToolCallID(t *testing.T) {
	if !requiresToolCallID("gemini-3.7-flash") || !requiresToolCallID("gemini-3.1-pro-preview") {
		t.Error("gemini 3 models must echo tool call ids")
	}
	if requiresToolCallID("gemini-2.5-flash") || requiresToolCallID("gemma-4-31b-it") {
		t.Error("unexpected tool call id requirement")
	}
}

// --- thinking config resolution ---

func tcLevel(tc *genai.ThinkingConfig) genai.ThinkingLevel {
	if tc == nil {
		return ""
	}
	return tc.ThinkingLevel
}

func TestGoogleThinkingConfigPro(t *testing.T) {
	id := "gemini-3.1-pro-preview"
	if got := tcLevel(googleThinkingConfig(types.ThinkingOff, id)); got != genai.ThinkingLevelLow {
		t.Errorf("off -> %q, want LOW", got)
	}
	if got := tcLevel(googleThinkingConfig(types.ThinkingMinimal, id)); got != genai.ThinkingLevelLow {
		t.Errorf("minimal -> %q, want LOW (MINIMAL unsupported)", got)
	}
	if got := tcLevel(googleThinkingConfig(types.ThinkingHigh, id)); got != genai.ThinkingLevelHigh {
		t.Errorf("high -> %q, want HIGH", got)
	}
}

func TestGoogleThinkingConfigFlash(t *testing.T) {
	id := "gemini-3.7-flash"
	if got := tcLevel(googleThinkingConfig(types.ThinkingOff, id)); got != genai.ThinkingLevelLow {
		t.Errorf("off -> %q, want LOW", got)
	}
	if got := tcLevel(googleThinkingConfig(types.ThinkingMinimal, id)); got != genai.ThinkingLevelLow {
		t.Errorf("minimal -> %q, want LOW (MINIMAL unsupported)", got)
	}
	if got := tcLevel(googleThinkingConfig(types.ThinkingMedium, id)); got != genai.ThinkingLevelMedium {
		t.Errorf("medium -> %q, want MEDIUM", got)
	}
	if got := tcLevel(googleThinkingConfig(types.ThinkingHigh, id)); got != genai.ThinkingLevelHigh {
		t.Errorf("high -> %q, want HIGH", got)
	}
}

func TestGoogleThinkingConfigLite(t *testing.T) {
	id := "gemini-3.5-flash-lite"
	if got := tcLevel(googleThinkingConfig(types.ThinkingOff, id)); got != genai.ThinkingLevelMinimal {
		t.Errorf("off -> %q, want MINIMAL", got)
	}
	if got := tcLevel(googleThinkingConfig(types.ThinkingLow, id)); got != genai.ThinkingLevelLow {
		t.Errorf("low -> %q, want LOW", got)
	}
}

func TestGoogleThinkingConfigGemmaAndUnknown(t *testing.T) {
	if tc := googleThinkingConfig(types.ThinkingHigh, "gemma-4-31b-it"); tc != nil {
		t.Errorf("gemma must get no thinking config, got %+v", tc)
	}
	if tc := googleThinkingConfig(types.ThinkingHigh, "gemini-2.5-flash"); tc != nil {
		t.Errorf("unknown family must get no thinking config, got %+v", tc)
	}
}

func TestGoogleThinkingConfigIncludeThoughts(t *testing.T) {
	for _, id := range []string{"gemini-3.7-flash", "gemini-3.1-pro-preview"} {
		tc := googleThinkingConfig(types.ThinkingMedium, id)
		if tc == nil || !tc.IncludeThoughts {
			t.Errorf("%s: IncludeThoughts not set: %+v", id, tc)
		}
	}
}

// --- stream behavior ---

func TestGeminiUnsupportedModelFails(t *testing.T) {
	p := NewGemini("k")
	m := gModel("http://127.0.0.1:1")
	m.ID = "gemini-2.5-flash"
	_, _, errEv := drain(p.Stream(context.Background(), m, simpleContext(), nil))
	if errEv == nil || !strings.Contains(errEv.Message.ErrorMessage, "unsupported model") {
		t.Fatalf("expected unsupported-model error, got %+v", errEv)
	}
}

func TestGeminiThinkingLevelInRequest(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
	}, &captured, nil, "")
	defer srv.Close()

	p := NewGemini("g-key")
	m := gModel(srv.URL)
	opts := &types.StreamOptions{ThinkingLevel: types.ThinkingMedium}
	_, _, errEv := drain(p.Stream(context.Background(), m, simpleContext(), opts))
	if errEv != nil {
		t.Fatal(errEv.Message.ErrorMessage)
	}
	gc, ok := captured["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig missing: %v", captured)
	}
	thinking, ok := gc["thinkingConfig"].(map[string]any)
	if !ok {
		t.Fatalf("thinkingConfig missing: %v", gc)
	}
	if thinking["thinkingLevel"] != "MEDIUM" {
		t.Fatalf("thinkingLevel = %v, want MEDIUM", thinking["thinkingLevel"])
	}
}

func TestGeminiToolCallIDEchoedInRequest(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
	}, &captured, nil, "")
	defer srv.Close()

	cx := &types.Context{
		Messages: []types.Message{
			&types.UserMessage{Content: types.TextOnly("go"), Timestamp: 1},
			&types.AssistantMessage{
				Content: []types.AssistantContent{
					types.ToolCall{Type: "toolCall", ID: "tc1", Name: "read", Arguments: json.RawMessage(`{}`)},
				},
				Provider: "google", API: types.APIGoogleGemini, Model: "gemini-3.7-flash",
			},
			&types.ToolResultMessage{ToolCallID: "tc1", ToolName: "read",
				Content: json.RawMessage(`[{"type":"text","text":"data"}]`)},
		},
	}
	p := NewGemini("g-key")
	_, _, errEv := drain(p.Stream(context.Background(), gModel(srv.URL), cx, nil))
	if errEv != nil {
		t.Fatal(errEv.Message.ErrorMessage)
	}
	contents := captured["contents"].([]any)
	modelEntry := contents[1].(map[string]any)
	part := modelEntry["parts"].([]any)[0].(map[string]any)
	fc := part["functionCall"].(map[string]any)
	if fc["id"] != "tc1" {
		t.Fatalf("function call id missing: %v", fc)
	}
	respPart := contents[2].(map[string]any)["parts"].([]any)[0].(map[string]any)
	fr := respPart["functionResponse"].(map[string]any)
	if fr["id"] != "tc1" {
		t.Fatalf("function response id missing: %v", fr)
	}
}

func TestGeminiThoughtSignatureRoundTrip(t *testing.T) {
	sig := "QUJDRA=="
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"text":"pondering","thought":true,"thoughtSignature":"` + sig + `"},{"functionCall":{"name":"weather","args":{"city":"Tokyo"}},"thoughtSignature":"` + sig + `"}]},"finishReason":"STOP"}]}`,
	}
	var captured map[string]any
	srv := sseServer(t, chunks, &captured, nil, "")
	defer srv.Close()

	p := NewGemini("g-key")
	_, done, errEv := drain(p.Stream(context.Background(), gModel(srv.URL), simpleContext(), nil))
	if errEv != nil {
		t.Fatal(errEv.Message.ErrorMessage)
	}
	var tc types.ToolCall
	for _, blk := range done.Message.Content {
		if c, ok := blk.(types.ToolCall); ok {
			tc = c
		}
	}
	if tc.Name != "weather" {
		t.Fatalf("tool call missing: %+v", done.Message.Content)
	}
	// The SDK base64-decodes thoughtSignature into raw bytes.
	if tc.Signature != "ABCD" {
		t.Fatalf("signature not captured: %q", tc.Signature)
	}

	// Echoing the assistant message back must attach the signature to the
	// functionCall part.
	model := gModel("http://unused")
	cx := &types.Context{
		Messages: []types.Message{&types.AssistantMessage{
			Content:  []types.AssistantContent{tc},
			Provider: model.Provider, API: model.API, Model: model.ID,
		}},
	}
	contents, err := p.ConvertMessages(model, cx)
	if err != nil {
		t.Fatal(err)
	}
	part := contents[0].Parts[0]
	if part.FunctionCall == nil || string(part.ThoughtSignature) != "ABCD" {
		t.Fatalf("signature not echoed on functionCall part: %+v", part)
	}
}
