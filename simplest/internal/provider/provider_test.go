package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// --- helpers ---

func sseServer(t *testing.T, events []string, captured *map[string]any, capturedHeaders *http.Header, pathContains string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pathContains != "" && !strings.Contains(r.URL.Path, pathContains) {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if captured != nil {
			*captured = body
		}
		if capturedHeaders != nil {
			*capturedHeaders = r.Header
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for _, e := range events {
			_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", e)
			f.Flush()
		}
	}))
}

func drain(ch <-chan types.AssistantMessageEvent) ([]types.AssistantMessageEvent, *types.DoneEvent, *types.StreamErrorEvent) {
	var all []types.AssistantMessageEvent
	for ev := range ch {
		switch e := ev.(type) {
		case types.DoneEvent:
			all = append(all, ev)
			return all, &e, nil
		case types.StreamErrorEvent:
			all = append(all, ev)
			return all, nil, &e
		default:
			all = append(all, ev)
		}
	}
	return all, nil, nil
}

func oaModel(url string) *types.Model {
	return &types.Model{
		ID: "gpt-test", Name: "GPT Test", API: types.APIOpenAICompat, Provider: "openai",
		BaseURL: url, Reasoning: true, ContextWindow: 128000, MaxTokens: 4096,
		Cost:  types.ModelCost{ModelCostRates: types.ModelCostRates{Input: 1, Output: 2}},
		Input: []string{"text", "image"},
	}
}

func gModel(url string) *types.Model {
	return &types.Model{
		ID: "gemini-3-flash", Name: "Gemini", API: types.APIGemini, Provider: "gemini",
		BaseURL: url, Reasoning: true, ContextWindow: 1e6, MaxTokens: 8192,
		Cost:  types.ModelCost{ModelCostRates: types.ModelCostRates{Input: 0.3, Output: 2.5}},
		Input: []string{"text", "image"},
	}
}

func simpleContext() *types.Context {
	return &types.Context{
		SystemPrompt: "be brief",
		Messages: []types.Message{
			&types.UserMessage{Content: types.TextOnly("hi"), Timestamp: 1},
		},
	}
}

func eventKinds(evs []types.AssistantMessageEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		switch v := e.(type) {
		case types.Partial:
			out = append(out, string(v.Kind))
		case types.DoneEvent:
			out = append(out, "done")
		case types.StreamErrorEvent:
			out = append(out, "error")
		}
	}
	return out
}

// --- OpenAI-completions ---

func TestOpenAIStreamHappyPath(t *testing.T) {
	chunks := []string{
		`{"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","function":{"name":"read","arguments":""}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"x\"}"}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":10},"completion_tokens_details":{"reasoning_tokens":5}}}`,
		"[DONE]",
	}
	var captured map[string]any
	var hdr http.Header
	srv := sseServer(t, chunks, &captured, &hdr, "/chat/completions")
	defer srv.Close()

	p := NewOpenAICompat("sk-test")
	evs, done, errEv := drain(p.Stream(context.Background(), oaModel(srv.URL), simpleContext(), nil))

	if errEv != nil {
		t.Fatalf("unexpected error event: %+v", errEv.Message.ErrorMessage)
	}
	if done == nil || done.Reason != types.StopToolUse {
		t.Fatalf("want done(toolUse), got %+v", done)
	}
	kinds := eventKinds(evs)
	want := "start,text_start,text_delta,text_delta,text_end,toolcall_start,toolcall_delta,toolcall_delta,toolcall_end,done"
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("event sequence:\n got %s\nwant %s", got, want)
	}
	msg := done.Message
	if len(msg.Content) != 2 ||
		msg.Content[0].(types.TextContent).Text != "Hello" ||
		msg.Content[1].(types.ToolCall).Name != "read" {
		t.Fatalf("bad content: %#v", msg.Content)
	}
	tc := msg.Content[1].(types.ToolCall)
	if tc.ID != "call_abc" || string(tc.Arguments) != `{"path":"x"}` {
		t.Fatalf("bad tool call: %+v", tc)
	}
	u := msg.Usage
	if u.Input != 90 || u.CacheRead != 10 || u.Output != 20 || u.TotalTokens != 120 {
		t.Fatalf("usage mismatch: %+v", u)
	}
	if u.Reasoning == nil || *u.Reasoning != 5 {
		t.Fatalf("reasoning tokens not mapped: %+v", u.Reasoning)
	}
	// cost: input 90*1/1e6 + output 20*2/1e6 (cache read rate unset => 0)
	wantOutput := 2.0 / 1e6 * 20
	if u.Cost.Total <= 0 || u.Cost.Output < wantOutput-1e-12 || u.Cost.Output > wantOutput+1e-12 {
		t.Fatalf("cost not computed: %+v", u.Cost)
	}
	if msg.ResponseID != "chatcmpl-1" || msg.RawStopReason != "tool_calls" {
		t.Fatalf("metadata lost: %+v", msg)
	}
	if got := hdr.Get("Authorization"); got != "Bearer sk-test" {
		t.Fatalf("auth header missing: %q", got)
	}
	if captured["stream_options"] == nil || captured["stream"] != true {
		t.Fatalf("request shape wrong: %v", captured)
	}
	if captured["reasoning_effort"] != nil {
		t.Fatalf("reasoning_effort must be omitted without thinking level")
	}
}

func TestOpenAIRequestOptionsAndTools(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]"}, &captured, nil, "")
	defer srv.Close()
	temp := 0.5
	mt := int64(1234)
	cx := simpleContext()
	cx.Tools = []types.ToolDef{{
		Name: "bash", Description: "run shell",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}}
	opts := &types.StreamOptions{Temperature: &temp, MaxTokens: &mt, ThinkingLevel: types.ThinkingMedium}
	p := NewOpenAICompat("k")
	_, _, _ = drain(p.Stream(context.Background(), oaModel(srv.URL), cx, opts))

	b := captured
	if b["temperature"] != 0.5 || b["max_completion_tokens"] != float64(1234) {
		t.Fatalf("options not sent: %v", b)
	}
	if b["reasoning_effort"] != "medium" {
		t.Fatalf("reasoning_effort wrong: %v", b["reasoning_effort"])
	}
	tools := b["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "bash" || fn["description"] != "run shell" {
		t.Fatalf("tools malformed: %v", tools)
	}
}

func TestOpenAIThinkingLevelWithMap(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]"}, &captured, nil, "")
	defer srv.Close()

	maxEffort := "max"
	m := oaModel(srv.URL)
	m.ThinkingLevelMap = types.ThinkingLevelMap{
		types.ThinkingHigh: &maxEffort,
	}

	cx := simpleContext()
	opts := &types.StreamOptions{ThinkingLevel: types.ThinkingHigh}
	p := NewOpenAICompat("k")
	_, _, errEv := drain(p.Stream(context.Background(), m, cx, opts))
	if errEv != nil {
		t.Fatalf("unexpected stream error: %+v", errEv)
	}

	if captured["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %v, want max", captured["reasoning_effort"])
	}
}

func TestOpenAISystemPromptAndRoles(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]"}, &captured, nil, "")
	defer srv.Close()
	cx := &types.Context{
		SystemPrompt: "sys prompt",
		Messages: []types.Message{
			&types.UserMessage{Content: types.TextOnly("q"), Timestamp: 1},
			&types.AssistantMessage{
				Content: []types.AssistantContent{
					types.TextContent{Type: "text", Text: "ans"},
					types.ToolCall{Type: "toolCall", ID: "c1", Name: "ls", Arguments: json.RawMessage(`{"p":1}`)},
				},
				StopReason: types.StopToolUse,
			},
			&types.ToolResultMessage{
				ToolCallID: "c1", ToolName: "ls",
				Content: json.RawMessage(`[{"type":"text","text":"out1\nout2"}]`),
			},
			&types.UserMessage{Content: types.TextOnly("next"), Timestamp: 4},
		},
	}
	p := NewOpenAICompat("k")
	_, _, _ = drain(p.Stream(context.Background(), oaModel(srv.URL), cx, nil))
	msgs := captured["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("want 5 wire messages, got %d: %v", len(msgs), msgs)
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "sys prompt" {
		t.Fatalf("system message wrong: %v", sys)
	}
	asst := msgs[2].(map[string]any)
	if asst["content"] != "ans" {
		t.Fatalf("assistant content wrong: %v", asst)
	}
	tcs := asst["tool_calls"].([]any)
	callFn := tcs[0].(map[string]any)["function"].(map[string]any)
	if callFn["arguments"] != `{"p":1}` {
		t.Fatalf("arguments must serialize as JSON string, got %T %v", callFn["arguments"], callFn["arguments"])
	}
	toolMsg := msgs[3].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["content"] != "out1\nout2" || toolMsg["tool_call_id"] != "c1" {
		t.Fatalf("tool result wrong: %v", toolMsg)
	}
	if msgs[4].(map[string]any)["content"] != "next" {
		t.Fatalf("trailing user lost: %v", msgs[4])
	}
}

func TestOpenAIConvertMessagesImageSupport(t *testing.T) {
	p := NewOpenAICompat("k")
	b64Data := "ZmFrZS1wbmctZGF0YQ=="

	userContentJSON, err := json.Marshal([]types.ImageContent{{
		Type:     types.TypeImage,
		Data:     b64Data,
		MimeType: "image/png",
	}})
	if err != nil {
		t.Fatal(err)
	}

	toolResultJSON, err := json.Marshal([]types.AssistantContent{
		types.TextContent{Type: types.TypeText, Text: "Read image file [image/png]"},
		types.ImageContent{Type: types.TypeImage, Data: b64Data, MimeType: "image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}

	cx := &types.Context{
		Messages: []types.Message{
			&types.UserMessage{Content: userContentJSON},
			&types.AssistantMessage{
				Content: []types.AssistantContent{
					types.ToolCall{Type: "toolCall", ID: "tc1", Name: "read", Arguments: json.RawMessage(`{"path":"a.png"}`)},
				},
			},
			&types.ToolResultMessage{ToolCallID: "tc1", ToolName: "read", Content: toolResultJSON},
		},
	}

	// Case 1: Model supports images
	mWithImage := oaModel("http://unused")
	mWithImage.Input = []string{"text", "image"}
	msgsWithImg, err := p.ConvertMessages(mWithImage, cx)
	if err != nil {
		t.Fatalf("ConvertMessages failed: %v", err)
	}
	// user, assistant, tool, user(attached image from tool result)
	if len(msgsWithImg) != 4 {
		t.Fatalf("want 4 messages, got %d: %+v", len(msgsWithImg), msgsWithImg)
	}
	// Verify user message has image
	userParts, ok := msgsWithImg[0].Content.([]oaPart)
	if !ok || len(userParts) != 1 || userParts[0].Type != "image_url" {
		t.Fatalf("expected user message to have image_url part: %+v", msgsWithImg[0].Content)
	}
	if userParts[0].ImageURL.URL != "data:image/png;base64,"+b64Data {
		t.Fatalf("unexpected image_url: %v", userParts[0].ImageURL)
	}
	// Verify tool result generated an attached image user message
	if msgsWithImg[2].Role != "tool" {
		t.Fatalf("want tool message at index 2: %+v", msgsWithImg[2])
	}
	if msgsWithImg[3].Role != "user" {
		t.Fatalf("want user message with attached image at index 3: %+v", msgsWithImg[3])
	}
	toolImgParts, ok := msgsWithImg[3].Content.([]oaPart)
	if !ok || len(toolImgParts) != 2 || toolImgParts[1].Type != "image_url" {
		t.Fatalf("expected attached image parts in msg[3]: %+v", msgsWithImg[3].Content)
	}

	// Case 2: Model does NOT support images
	mNoImage := oaModel("http://unused")
	mNoImage.Input = []string{"text"}
	msgsNoImg, err := p.ConvertMessages(mNoImage, cx)
	if err != nil {
		t.Fatalf("ConvertMessages failed: %v", err)
	}
	// user, assistant, tool (no extra user message with image)
	if len(msgsNoImg) != 3 {
		t.Fatalf("want 3 messages without image support, got %d: %+v", len(msgsNoImg), msgsNoImg)
	}
	// Verify placeholder used for user message
	noImgParts, ok := msgsNoImg[0].Content.([]oaPart)
	if !ok || len(noImgParts) != 1 || noImgParts[0].Text != imageOmittedPlaceholder {
		t.Fatalf("expected image placeholder in user parts, got %v", msgsNoImg[0].Content)
	}
}

func TestOpenAISendsWireModel(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{
		`{"id":"chatcmpl-1","model":"deepseek-v4.1-flash-expires-on-0910","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		"[DONE]",
	}, &captured, nil, "/chat/completions")
	defer srv.Close()

	p := NewOpenAICompat("k")
	m := oaModel(srv.URL)
	m.ID = "ds-v4.1-flash" // stable alias used everywhere but the wire
	m.Model = "deepseek-v4.1-flash-expires-on-0910"

	_, done, errEv := drain(p.Stream(context.Background(), m, simpleContext(), nil))
	if errEv != nil {
		t.Fatalf("unexpected error: %+v", errEv.Message.ErrorMessage)
	}
	if got := captured["model"]; got != "deepseek-v4.1-flash-expires-on-0910" {
		t.Fatalf("wire model = %v, want remote id", got)
	}
	// Events keep the stable alias; the echoed upstream name lands in ResponseModel.
	if done.Message.Model != "ds-v4.1-flash" {
		t.Fatalf("event model = %q, want alias", done.Message.Model)
	}
	if done.Message.ResponseModel != "deepseek-v4.1-flash-expires-on-0910" {
		t.Fatalf("response model = %q, want echoed upstream name", done.Message.ResponseModel)
	}
}
func TestOpenAIHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()
	p := NewOpenAICompat("wrong")
	_, _, errEv := drain(p.Stream(context.Background(), oaModel(srv.URL), simpleContext(), nil))
	if errEv == nil {
		t.Fatal("expected error event")
	}
	if !strings.Contains(errEv.Message.ErrorMessage, "401") || !strings.Contains(errEv.Message.ErrorMessage, "bad key") {
		t.Fatalf("error should carry status and body, got %q", errEv.Message.ErrorMessage)
	}
	if errEv.Reason != types.StopError {
		t.Fatalf("reason %q", errEv.Reason)
	}
}

func TestOpenAIMissingFinishReasonFails(t *testing.T) {
	srv := sseServer(t, []string{`{"choices":[{"delta":{"content":"x"}}]}`}, nil, nil, "")
	defer srv.Close()
	p := NewOpenAICompat("k")
	_, _, errEv := drain(p.Stream(context.Background(), oaModel(srv.URL), simpleContext(), nil))
	if errEv == nil || !strings.Contains(errEv.Message.ErrorMessage, "finish_reason") {
		t.Fatalf("expected finish_reason error, got %+v", errEv)
	}
}

func TestOpenAIReasoningDelta(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"delta":{"content":"answer"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		"[DONE]",
	}, nil, nil, "")
	defer srv.Close()
	p := NewOpenAICompat("k")
	evs, done, errEv := drain(p.Stream(context.Background(), oaModel(srv.URL), simpleContext(), nil))
	if errEv != nil {
		t.Fatal(errEv.Message.ErrorMessage)
	}
	kinds := strings.Join(eventKinds(evs), ",")
	if !strings.Contains(kinds, "thinking_start,thinking_delta,thinking_end") {
		t.Fatalf("thinking events missing: %s", kinds)
	}
	th := done.Message.Content[0].(types.ThinkingContent)
	if th.Thinking != "think" {
		t.Fatalf("thinking text: %q", th.Thinking)
	}
	txt := done.Message.Content[1].(types.TextContent)
	if txt.Text != "answer" {
		t.Fatalf("text after thinking: %q", txt.Text)
	}
}

func TestOpenAIReasoningEffortValidation(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"content":"ok"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		"[DONE]",
	}, nil, nil, "")
	defer srv.Close()

	model := oaModel(srv.URL)
	model.Reasoning = true
	model.ReasoningEffort = []string{"low", "high"}

	p := NewOpenAICompat("k")

	// Valid effort
	_, done, errEv := drain(p.Stream(context.Background(), model, simpleContext(), &types.StreamOptions{
		ThinkingLevel: types.ThinkingLow,
	}))
	if errEv != nil {
		t.Fatalf("expected success for allowed thinking level, got error: %+v", errEv)
	}
	if done == nil {
		t.Fatal("expected done event")
	}

	// Disallowed effort
	_, _, errEv = drain(p.Stream(context.Background(), model, simpleContext(), &types.StreamOptions{
		ThinkingLevel: types.ThinkingMax,
	}))
	if errEv == nil {
		t.Fatal("expected error event for disallowed thinking level")
	}
	if !strings.Contains(errEv.Message.ErrorMessage, "unsupported reasoning effort \"max\"") {
		t.Fatalf("unexpected error message: %q", errEv.Message.ErrorMessage)
	}
}

func TestOpenAIReasoningEffortThinkingLevelMap(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"content":"ok"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		"[DONE]",
	}
	var captured map[string]any
	srv := sseServer(t, chunks, &captured, nil, "/chat/completions")
	defer srv.Close()

	highMapped := "max"
	offMapped := "none"
	model := oaModel(srv.URL)
	model.Reasoning = true
	model.ThinkingLevelMap = types.ThinkingLevelMap{
		types.ThinkingMinimal: nil,
		types.ThinkingHigh:    &highMapped,
		types.ThinkingOff:     &offMapped,
	}

	p := NewOpenAICompat("sk-test")

	// 1. ThinkingHigh mapped to "max"
	_, done, errEv := drain(p.Stream(context.Background(), model, simpleContext(), &types.StreamOptions{
		ThinkingLevel: types.ThinkingHigh,
	}))
	require.Nil(t, errEv)
	require.NotNil(t, done)
	assert.Equal(t, "max", captured["reasoning_effort"])

	// 2. ThinkingOff mapped to "none"
	captured = nil
	_, done, errEv = drain(p.Stream(context.Background(), model, simpleContext(), &types.StreamOptions{
		ThinkingLevel: types.ThinkingOff,
	}))
	require.Nil(t, errEv)
	require.NotNil(t, done)
	assert.Equal(t, "none", captured["reasoning_effort"])

	// 3. ThinkingOff without "none" mapping is omitted
	modelWithoutNone := oaModel(srv.URL)
	modelWithoutNone.Reasoning = true
	captured = nil
	_, done, errEv = drain(p.Stream(context.Background(), modelWithoutNone, simpleContext(), &types.StreamOptions{
		ThinkingLevel: types.ThinkingOff,
	}))
	require.Nil(t, errEv)
	require.NotNil(t, done)
	_, hasEffort := captured["reasoning_effort"]
	assert.False(t, hasEffort)

	// 4. ThinkingMinimal explicitly disabled (nil) -> rejected
	_, _, errEv = drain(p.Stream(context.Background(), model, simpleContext(), &types.StreamOptions{
		ThinkingLevel: types.ThinkingMinimal,
	}))
	require.NotNil(t, errEv)
	assert.Contains(t, errEv.Message.ErrorMessage, "unsupported reasoning effort \"minimal\"")
}

// --- Google Generative AI ---

func gChunks(events ...string) []string { return events }

func TestGeminiStreamHappyPath(t *testing.T) {
	chunks := gChunks(
		`{"candidates":[{"content":{"parts":[{"text":"pondering","thought":true}]}}],"usageMetadata":{"promptTokenCount":50,"cachedContentTokenCount":10,"totalTokenCount":60}}`,
		`{"responseId":"resp-1","candidates":[{"content":{"role":"model","parts":[{"text":"Hi"},{"thoughtSignature":"QUJDRA==","text":"more"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"bash","args":{"cmd":"ls"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":50,"cachedContentTokenCount":10,"candidatesTokenCount":7,"thoughtsTokenCount":9,"totalTokenCount":66}}`,
	)
	var captured map[string]any
	var hdr http.Header
	srv := sseServer(t, chunks, &captured, &hdr, ":streamGenerateContent")
	defer srv.Close()

	p := NewGemini("g-key")
	evs, done, errEv := drain(p.Stream(context.Background(), gModel(srv.URL), simpleContext(), nil))
	if errEv != nil {
		t.Fatalf("unexpected error: %+v", errEv.Message.ErrorMessage)
	}
	if done == nil || done.Reason != types.StopToolUse {
		t.Fatalf("STOP with functionCall must upgrade to toolUse, got %+v", done)
	}
	kinds := eventKinds(evs)
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "thinking_start,thinking_delta,thinking_end") {
		t.Fatalf("thinking events missing: %s", joined)
	}
	if !strings.Contains(joined, "toolcall_start,toolcall_delta,toolcall_end") {
		t.Fatalf("toolcall events missing: %s", joined)
	}
	msg := done.Message
	if msg.ResponseID != "resp-1" {
		t.Fatalf("responseId not captured: %+v", msg)
	}
	thinking := msg.Content[0].(types.ThinkingContent)
	if thinking.Thinking != "pondering" {
		t.Fatalf("thinking: %q", thinking.Thinking)
	}
	text := msg.Content[1].(types.TextContent)
	if text.Text != "Himore" {
		t.Fatalf("text: %q", text.Text)
	}
	tc := msg.Content[2].(types.ToolCall)
	if tc.Name != "bash" || tc.ID == "" || string(tc.Arguments) != `{"cmd":"ls"}` {
		t.Fatalf("synthesized tool call wrong: %+v", tc)
	}
	if !strings.HasPrefix(tc.ID, "bash_") {
		t.Fatalf("synthesized id format: %q", tc.ID)
	}
	u := msg.Usage
	if u.Input != 40 || u.CacheRead != 10 || u.Output != 16 || u.TotalTokens != 66 {
		t.Fatalf("usage mapping wrong: %+v", u)
	}
	if u.Reasoning == nil || *u.Reasoning != 9 {
		t.Fatalf("thought tokens: %+v", u.Reasoning)
	}
	if got := hdr.Get("x-goog-api-key"); got != "g-key" {
		t.Fatalf("api key header missing")
	}
	if captured["systemInstruction"] == nil {
		t.Fatalf("systemInstruction missing from request")
	}
	si := captured["systemInstruction"].(map[string]any)
	parts := si["parts"].([]any)
	if parts[0].(map[string]any)["text"] != "be brief" {
		t.Fatalf("system instruction content: %v", parts)
	}
}

func TestGeminiMaxTokensLength(t *testing.T) {
	srv := sseServer(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"MAX_TOKENS"}]}`,
	}, nil, nil, "")
	defer srv.Close()
	p := NewGemini("g-key")
	evs, done, errEv := drain(p.Stream(context.Background(), gModel(srv.URL), simpleContext(), nil))
	if errEv != nil {
		t.Fatal(errEv.Message.ErrorMessage)
	}
	if done == nil || done.Reason != types.StopLength || done.Message.RawStopReason != "MAX_TOKENS" {
		t.Fatalf("want length/MAX_TOKENS, got %+v", done)
	}
	_ = evs
}

func TestGeminiNoFinishReasonFails(t *testing.T) {
	srv := sseServer(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`,
	}, nil, nil, "")
	defer srv.Close()
	p := NewGemini("g-key")
	_, _, errEv := drain(p.Stream(context.Background(), gModel(srv.URL), simpleContext(), nil))
	if errEv == nil || !strings.Contains(errEv.Message.ErrorMessage, "without a finish reason") {
		t.Fatalf("expected no-finish-reason error, got %+v", errEv)
	}
}

func TestGeminiSafetyFinishIsError(t *testing.T) {
	srv := sseServer(t, []string{
		`{"candidates":[{"finishReason":"SAFETY"}]}`,
	}, nil, nil, "")
	defer srv.Close()
	p := NewGemini("g-key")
	_, _, errEv := drain(p.Stream(context.Background(), gModel(srv.URL), simpleContext(), nil))
	if errEv == nil || !strings.Contains(errEv.Message.ErrorMessage, "SAFETY") {
		t.Fatalf("expected SAFETY error, got %+v", errEv)
	}
}

func TestGeminiConvertMessagesMergesToolResults(t *testing.T) {
	model := gModel("http://unused")
	model.ID = "gemini-3-pro" // gemini>=3 echoes tool call ids
	cx := &types.Context{
		Messages: []types.Message{
			&types.UserMessage{Content: types.TextOnly("go"), Timestamp: 1},
			&types.AssistantMessage{
				Content: []types.AssistantContent{
					types.ToolCall{Type: "toolCall", ID: "tc1", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)},
					types.ToolCall{Type: "toolCall", ID: "tc2", Name: "grep", Arguments: json.RawMessage(`{}`)},
				},
				Provider: model.Provider, API: model.API, Model: model.ID,
			},
			&types.ToolResultMessage{ToolCallID: "tc1", ToolName: "read",
				Content: json.RawMessage(`[{"type":"text","text":"file data"}]`)},
			&types.ToolResultMessage{ToolCallID: "tc2", ToolName: "grep", IsError: true,
				Content: json.RawMessage(`[{"type":"text","text":"boom"}]`)},
		},
	}
	p := NewGemini("k")
	contents, err := p.ConvertMessages(model, cx)
	if err != nil {
		t.Fatal(err)
	}
	// user, model(functionCalls), user(merged functionResponses)
	if len(contents) != 3 {
		t.Fatalf("want 3 contents, got %d: %+v", len(contents), contents)
	}
	modelParts := contents[1].Parts
	if len(modelParts) != 2 || modelParts[0].FunctionCall == nil || modelParts[0].FunctionCall.ID != "tc1" {
		t.Fatalf("gemini>=3 must echo tool call ids: %+v", modelParts)
	}
	respParts := contents[2].Parts
	if len(respParts) != 2 {
		t.Fatalf("consecutive tool results must merge into one user entry: %+v", respParts)
	}
	r0 := respParts[0].FunctionResponse
	r1 := respParts[1].FunctionResponse
	if r0.Name != "read" || r1.Name != "grep" {
		t.Fatalf("responses named wrongly: %+v %+v", r0, r1)
	}
	if r0.Response["output"] != "file data" {
		t.Fatalf("output framing: %v", r0.Response)
	}
	if r1.Response["error"] != "boom" {
		t.Fatalf("error framing: %v", r1.Response)
	}
}

func TestGeminiConvertThinkingSameModelOnly(t *testing.T) {
	model := gModel("http://unused")
	cx := &types.Context{
		Messages: []types.Message{
			&types.AssistantMessage{
				Content: []types.AssistantContent{
					types.ThinkingContent{Type: "thinking", Thinking: "secret plan"},
					types.TextContent{Type: "text", Text: "reply"},
				},
				Provider: "other-provider", API: model.API, Model: model.ID,
			},
		},
	}
	p := NewGemini("k")
	contents, err := p.ConvertMessages(model, cx)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 1 || len(contents[0].Parts) != 1 {
		t.Fatalf("cross-model thinking must be dropped: %+v", contents)
	}
	if contents[0].Parts[0].Thought {
		t.Fatalf("remaining part must be plain text: %+v", contents[0].Parts[0])
	}

	// Same-model thinking is preserved as a thought part.
	cx.Messages[0] = &types.AssistantMessage{
		Content: []types.AssistantContent{
			types.ThinkingContent{Type: "thinking", Thinking: "plan"},
			types.TextContent{Type: "text", Text: "reply"},
		},
		Provider: model.Provider, API: model.API, Model: model.ID,
	}
	contents, err = p.ConvertMessages(model, cx)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents[0].Parts) != 2 || !contents[0].Parts[0].Thought || contents[0].Parts[0].Text != "plan" {
		t.Fatalf("same-model thinking lost: %+v", contents[0].Parts)
	}
}

func TestBothProvidersRequireAPIKey(t *testing.T) {
	oa := NewOpenAICompat("")
	gm := NewGemini("")
	ctx := context.Background()
	if _, _, err := drain(oa.Stream(ctx, oaModel("http://127.0.0.1:1"), simpleContext(), nil)); err == nil {
		t.Fatal("openai provider must fail without key")
	}
	if _, _, err := drain(gm.Stream(ctx, gModel("http://127.0.0.1:1"), simpleContext(), nil)); err == nil {
		t.Fatal("gemini provider must fail without key")
	}
}

func TestParseStreamingJSONRepairsTruncation(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"a":1`, `{"a":1}`},
		{`{"a":[1,{"b":"tw`, `{"a":[1,{"b":"tw"}]}`},
		{`{"full":true}`, `{"full":true}`},
		{``, ``},
		{`{"open":"str`, `{"open":"str"}`},
	}
	for _, c := range cases {
		got := parseStreamingJSON(c.in)
		if c.want == "" {
			if got != nil {
				t.Errorf("parse(%q) = %s, want nil", c.in, got)
			}
			continue
		}
		if got == nil || string(got) != c.want {
			t.Errorf("parse(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestOpenAI_BeforeProviderRequest_PerAttempt(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	var receivedHeaders []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		receivedHeaders = append(receivedHeaders, r.Header.Clone())
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"error":"temporary unavailable"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		_, _ = fmt.Fprintf(w, "data: [DONE]\r\n\r\n")
		f := w.(http.Flusher)
		f.Flush()
	}))
	t.Cleanup(srv.Close)

	p := NewOpenAICompat("sk-test")
	m := oaModel(srv.URL)
	opts := &types.StreamOptions{
		RetryPolicy: &types.RetryPolicy{
			Enabled:     true,
			MaxRetries:  2,
			BaseDelayMs: 5,
			MaxDelayMs:  20,
		},
		BeforeProviderRequest: func(req *http.Request, body []byte) ([]byte, error) {
			req.Header.Set("X-Attempt-Hook", "true")
			return body, nil
		},
	}

	evs, done, errEv := drain(p.Stream(context.Background(), m, simpleContext(), opts))
	require.Nil(t, errEv)
	require.NotNil(t, done)
	assert.Equal(t, types.StopStop, done.Reason)
	assert.Equal(t, int32(2), attempts.Load())
	require.Len(t, receivedHeaders, 2)
	assert.Equal(t, "true", receivedHeaders[0].Get("X-Attempt-Hook"), "first attempt must have hook header")
	assert.Equal(t, "true", receivedHeaders[1].Get("X-Attempt-Hook"), "second attempt (retry) must also have hook header")

	var text string
	for _, ev := range evs {
		if pEv, ok := ev.(types.Partial); ok && pEv.Kind == types.EvTextDelta {
			text += pEv.Delta
		}
	}
	assert.Equal(t, "ok", text)
}

func TestOpenAI_OnProviderStreamEvent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", `{"choices":[{"delta":{"content":"hello"},"finish_reason":""}]}`)
		_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		_, _ = fmt.Fprintf(w, "data: [DONE]\r\n\r\n")
		f := w.(http.Flusher)
		f.Flush()
	}))
	t.Cleanup(srv.Close)

	var rawEvents []string
	var rawDatas []string

	p := NewOpenAICompat("sk-test")
	m := oaModel(srv.URL)
	opts := &types.StreamOptions{
		OnProviderStreamEvent: func(rawEvent string, rawData []byte) {
			rawEvents = append(rawEvents, rawEvent)
			rawDatas = append(rawDatas, string(rawData))
		},
	}

	_, done, errEv := drain(p.Stream(context.Background(), m, simpleContext(), opts))
	require.Nil(t, errEv)
	require.NotNil(t, done)
	assert.Equal(t, types.StopStop, done.Reason)

	require.Len(t, rawEvents, 3)
	assert.Equal(t, []string{"message", "message", "message"}, rawEvents)
	assert.Contains(t, rawDatas[0], `"content":"hello"`)
	assert.Contains(t, rawDatas[1], `"finish_reason":"stop"`)
	assert.Equal(t, "[DONE]", rawDatas[2])
}

func TestCalculateCost_Tiers(t *testing.T) {
	t.Parallel()

	model := &types.Model{
		ID: "tiered-model",
		Cost: types.ModelCost{
			ModelCostRates: types.ModelCostRates{
				Input:      3.0,
				Output:     15.0,
				CacheRead:  0.75,
				CacheWrite: 3.0,
			},
			Tiers: []types.ModelCostTier{
				{
					InputTokensAbove: 128000,
					ModelCostRates: types.ModelCostRates{
						Input:      6.0,
						Output:     30.0,
						CacheRead:  1.5,
						CacheWrite: 6.0,
					},
				},
				{
					InputTokensAbove: 256000,
					ModelCostRates: types.ModelCostRates{
						Input:      10.0,
						Output:     50.0,
						CacheRead:  2.5,
						CacheWrite: 10.0,
					},
				},
			},
		},
	}

	tests := []struct {
		name          string
		usage         types.Usage
		wantInputCost float64
		wantOutCost   float64
		wantTotalCost float64
	}{
		{
			name: "below tier 1 threshold uses base rates",
			usage: types.Usage{
				Input:      100000,
				Output:     1000,
				CacheRead:  10000, // total input: 110,000 < 128,000
				CacheWrite: 0,
			},
			// Base rates: input=3.0, output=15.0, cacheRead=0.75
			// inputCost = 3.0 / 1e6 * 100000 = 0.3
			// outputCost = 15.0 / 1e6 * 1000 = 0.015
			// cacheReadCost = 0.75 / 1e6 * 10000 = 0.0075
			// total = 0.3 + 0.015 + 0.0075 = 0.3225
			wantInputCost: 0.3,
			wantOutCost:   0.015,
			wantTotalCost: 0.3225,
		},
		{
			name: "hits tier 1 threshold (>= 128000)",
			usage: types.Usage{
				Input:      100000,
				Output:     1000,
				CacheRead:  20000,
				CacheWrite: 10000, // total input: 130,000 >= 128,000 but < 256,000
			},
			// Tier 1 rates: input=6.0, output=30.0, cacheRead=1.5, cacheWrite=6.0
			// inputCost = 6.0 / 1e6 * 100000 = 0.6
			// outputCost = 30.0 / 1e6 * 1000 = 0.03
			// cacheReadCost = 1.5 / 1e6 * 20000 = 0.03
			// cacheWriteCost = 6.0 / 1e6 * 10000 = 0.06
			// total = 0.6 + 0.03 + 0.03 + 0.06 = 0.72
			wantInputCost: 0.6,
			wantOutCost:   0.03,
			wantTotalCost: 0.72,
		},
		{
			name: "hits tier 2 threshold (>= 256000)",
			usage: types.Usage{
				Input:      200000,
				Output:     2000,
				CacheRead:  50000,
				CacheWrite: 10000, // total input: 260,000 >= 256,000
			},
			// Tier 2 rates: input=10.0, output=50.0, cacheRead=2.5, cacheWrite=10.0
			// inputCost = 10.0 / 1e6 * 200000 = 2.0
			// outputCost = 50.0 / 1e6 * 2000 = 0.1
			// cacheReadCost = 2.5 / 1e6 * 50000 = 0.125
			// cacheWriteCost = 10.0 / 1e6 * 10000 = 0.1
			// total = 2.0 + 0.1 + 0.125 + 0.1 = 2.325
			wantInputCost: 2.0,
			wantOutCost:   0.1,
			wantTotalCost: 2.325,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u := tt.usage
			CalculateCost(model, &u)

			assert.InDelta(t, tt.wantInputCost, u.Cost.Input, 1e-9)
			assert.InDelta(t, tt.wantOutCost, u.Cost.Output, 1e-9)
			assert.InDelta(t, tt.wantTotalCost, u.Cost.Total, 1e-9)
		})
	}
}

func TestEmitter_ContentIndexOrdering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		run         func(em *emitter)
		wantKinds   []string
		wantIndices []int
		wantContent func(t *testing.T, content []types.AssistantContent)
		wantStop    types.StopReason
	}{
		{
			name: "interleaved Text -> Thinking -> Text -> ToolCall -> ToolCall",
			run: func(em *emitter) {
				em.start("openai-completions", "openai", "gpt-test")
				// Block 0: Text
				em.textDelta("First ")
				em.textDelta("text chunk.")
				// Block 1: Thinking
				em.thinkingDelta("Thinking step 1. ", "sig-1")
				em.thinkingDelta("Thinking step 2.", "")
				// Block 2: Text
				em.textDelta("Second text chunk.")
				// Block 3: ToolCall
				idx1 := em.appendToolCall(types.ToolCall{Type: types.TypeToolCall, ID: "call_1", Name: "search"})
				em.toolCallDelta(idx1, `{"query":"test"}`, json.RawMessage(`{"query":"test"}`))
				em.toolCallEnd(idx1)
				// Block 4: ToolCall
				idx2 := em.appendToolCall(types.ToolCall{Type: types.TypeToolCall, ID: "call_2", Name: "exec"})
				em.toolCallDelta(idx2, `{"cmd":"ls"}`, json.RawMessage(`{"cmd":"ls"}`))
				em.toolCallEnd(idx2)

				em.done(types.StopToolUse)
			},
			wantKinds: []string{
				"start",
				"text_start", "text_delta", "text_delta", "text_end",
				"thinking_start", "thinking_delta", "thinking_delta", "thinking_end",
				"text_start", "text_delta", "text_end",
				"toolcall_start", "toolcall_delta", "toolcall_end",
				"toolcall_start", "toolcall_delta", "toolcall_end",
				"done",
			},
			wantIndices: []int{
				0,          // start
				0, 0, 0, 0, // text
				1, 1, 1, 1, // thinking
				2, 2, 2, // text
				3, 3, 3, // toolcall 1
				4, 4, 4, // toolcall 2
			},
			wantContent: func(t *testing.T, content []types.AssistantContent) {
				require.Len(t, content, 5)

				c0, ok := content[0].(types.TextContent)
				require.True(t, ok)
				assert.Equal(t, "First text chunk.", c0.Text)

				c1, ok := content[1].(types.ThinkingContent)
				require.True(t, ok)
				assert.Equal(t, "Thinking step 1. Thinking step 2.", c1.Thinking)
				assert.Equal(t, "sig-1", c1.Signature)

				c2, ok := content[2].(types.TextContent)
				require.True(t, ok)
				assert.Equal(t, "Second text chunk.", c2.Text)

				c3, ok := content[3].(types.ToolCall)
				require.True(t, ok)
				assert.Equal(t, "call_1", c3.ID)
				assert.Equal(t, "search", c3.Name)
				assert.JSONEq(t, `{"query":"test"}`, string(c3.Arguments))

				c4, ok := content[4].(types.ToolCall)
				require.True(t, ok)
				assert.Equal(t, "call_2", c4.ID)
				assert.Equal(t, "exec", c4.Name)
				assert.JSONEq(t, `{"cmd":"ls"}`, string(c4.Arguments))
			},
			wantStop: types.StopToolUse,
		},
		{
			name: "Thinking -> Text -> Done closes open text block",
			run: func(em *emitter) {
				em.start("openai-completions", "openai", "gpt-test")
				em.thinkingDelta("reasoning", "")
				em.textDelta("final answer")
				em.done(types.StopStop)
			},
			wantKinds: []string{
				"start",
				"thinking_start", "thinking_delta", "thinking_end",
				"text_start", "text_delta", "text_end",
				"done",
			},
			wantIndices: []int{
				0,       // start
				0, 0, 0, // thinking
				1, 1, 1, // text
			},
			wantContent: func(t *testing.T, content []types.AssistantContent) {
				require.Len(t, content, 2)

				c0, ok := content[0].(types.ThinkingContent)
				require.True(t, ok)
				assert.Equal(t, "reasoning", c0.Thinking)

				c1, ok := content[1].(types.TextContent)
				require.True(t, ok)
				assert.Equal(t, "final answer", c1.Text)
			},
			wantStop: types.StopStop,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ch := make(chan types.AssistantMessageEvent, 64)
			em := newEmitter(ch)

			go func() {
				defer close(ch)
				tt.run(em)
			}()

			evs, done, errEv := drain(ch)
			require.Nil(t, errEv)
			require.NotNil(t, done)
			assert.Equal(t, tt.wantStop, done.Reason)

			kinds := eventKinds(evs)
			assert.Equal(t, tt.wantKinds, kinds)

			// Collect Partial ContentIndex
			var gotIndices []int
			for _, ev := range evs {
				if p, ok := ev.(types.Partial); ok {
					gotIndices = append(gotIndices, p.ContentIndex)
				}
			}
			assert.Equal(t, tt.wantIndices, gotIndices)

			tt.wantContent(t, done.Message.Content)
		})
	}
}

func TestOpenAICompat_MetadataExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		modelID           string
		chunks            []string
		wantResponseID    string
		wantResponseModel string
		wantRawStopReason string
		wantStopReason    types.StopReason
	}{
		{
			name:    "upstream model differs from configured model",
			modelID: "gpt-4o",
			chunks: []string{
				`{"id":"chatcmpl-meta-1","model":"gpt-4o-2024-08-06","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
				`{"id":"chatcmpl-meta-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				"[DONE]",
			},
			wantResponseID:    "chatcmpl-meta-1",
			wantResponseModel: "gpt-4o-2024-08-06",
			wantRawStopReason: "stop",
			wantStopReason:    types.StopStop,
		},
		{
			name:    "upstream model identical to configured model leaves responseModel empty",
			modelID: "gpt-test",
			chunks: []string{
				`{"id":"chatcmpl-meta-2","model":"gpt-test","choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
				`{"id":"chatcmpl-meta-2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				"[DONE]",
			},
			wantResponseID:    "chatcmpl-meta-2",
			wantResponseModel: "",
			wantRawStopReason: "stop",
			wantStopReason:    types.StopStop,
		},
		{
			name:    "tool_calls finish reason captured in rawStopReason",
			modelID: "gpt-4o",
			chunks: []string{
				`{"id":"chatcmpl-meta-3","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"fetch","arguments":"{}"}}]}}]}`,
				`{"id":"chatcmpl-meta-3","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				"[DONE]",
			},
			wantResponseID:    "chatcmpl-meta-3",
			wantResponseModel: "gpt-4o-mini",
			wantRawStopReason: "tool_calls",
			wantStopReason:    types.StopToolUse,
		},
		{
			name:    "length finish reason captured in rawStopReason",
			modelID: "gpt-4o",
			chunks: []string{
				`{"id":"chatcmpl-meta-4","model":"gpt-4o-2024-11-20","choices":[{"index":0,"delta":{"content":"trunc"}}]}`,
				`{"id":"chatcmpl-meta-4","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
				"[DONE]",
			},
			wantResponseID:    "chatcmpl-meta-4",
			wantResponseModel: "gpt-4o-2024-11-20",
			wantRawStopReason: "length",
			wantStopReason:    types.StopLength,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := sseServer(t, tt.chunks, nil, nil, "/chat/completions")
			t.Cleanup(srv.Close)

			p := NewOpenAICompat("sk-test")
			m := oaModel(srv.URL)
			m.ID = tt.modelID

			evs, done, errEv := drain(p.Stream(context.Background(), m, simpleContext(), nil))
			require.Nil(t, errEv)
			require.NotNil(t, done)

			// Verify Partial events received after first chunk carries metadata
			seenPartial := false
			for _, ev := range evs {
				if pEv, ok := ev.(types.Partial); ok && pEv.Kind != types.EvStart {
					seenPartial = true
					require.NotNil(t, pEv.Partial)
					assert.Equal(t, tt.wantResponseID, pEv.Partial.ResponseID)
					assert.Equal(t, tt.wantResponseModel, pEv.Partial.ResponseModel)
				}
			}
			assert.True(t, seenPartial, "expected at least one non-start Partial event")

			// Verify DoneEvent message metadata
			assert.Equal(t, tt.wantResponseID, done.Message.ResponseID)
			assert.Equal(t, tt.wantResponseModel, done.Message.ResponseModel)
			assert.Equal(t, tt.wantRawStopReason, done.Message.RawStopReason)
			assert.Equal(t, tt.wantStopReason, done.Reason)
			assert.Equal(t, tt.wantStopReason, done.Message.StopReason)
		})
	}
}

func TestGoogleGemini_MetadataExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		chunks            []string
		wantResponseID    string
		wantResponseModel string
		wantRawStopReason string
		wantStopReason    types.StopReason
	}{
		{
			name: "STOP with responseId and modelVersion",
			chunks: []string{
				`{"responseId":"resp-gemini-1","modelVersion":"gemini-2.5-flash-001","candidates":[{"content":{"parts":[{"text":"Hello from Gemini"}]}}]}`,
				`{"candidates":[{"finishReason":"STOP"}]}`,
			},
			wantResponseID:    "resp-gemini-1",
			wantResponseModel: "gemini-2.5-flash-001",
			wantRawStopReason: "STOP",
			wantStopReason:    types.StopStop,
		},
		{
			name: "MAX_TOKENS with responseId and modelVersion",
			chunks: []string{
				`{"responseId":"resp-gemini-2","modelVersion":"gemini-3-pro-exp-02","candidates":[{"content":{"parts":[{"text":"Too long text..."}]}}]}`,
				`{"candidates":[{"finishReason":"MAX_TOKENS"}]}`,
			},
			wantResponseID:    "resp-gemini-2",
			wantResponseModel: "gemini-3-pro-exp-02",
			wantRawStopReason: "MAX_TOKENS",
			wantStopReason:    types.StopLength,
		},
		{
			name: "First non-empty modelVersion is preserved across chunks",
			chunks: []string{
				`{"responseId":"resp-gemini-3","modelVersion":"gemini-3-flash-preview","candidates":[{"content":{"parts":[{"text":"part1"}]}}]}`,
				`{"candidates":[{"content":{"parts":[{"text":"part2"}]}}]}`,
				`{"candidates":[{"finishReason":"STOP"}]}`,
			},
			wantResponseID:    "resp-gemini-3",
			wantResponseModel: "gemini-3-flash-preview",
			wantRawStopReason: "STOP",
			wantStopReason:    types.StopStop,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := sseServer(t, tt.chunks, nil, nil, ":streamGenerateContent")
			t.Cleanup(srv.Close)

			p := NewGemini("test-api-key")
			evs, done, errEv := drain(p.Stream(context.Background(), gModel(srv.URL), simpleContext(), nil))
			require.Nil(t, errEv)
			require.NotNil(t, done)

			// Verify Partial events received after first chunk carries metadata
			seenPartial := false
			for _, ev := range evs {
				if pEv, ok := ev.(types.Partial); ok && pEv.Kind != types.EvStart {
					seenPartial = true
					require.NotNil(t, pEv.Partial)
					assert.Equal(t, tt.wantResponseID, pEv.Partial.ResponseID)
					assert.Equal(t, tt.wantResponseModel, pEv.Partial.ResponseModel)
				}
			}
			assert.True(t, seenPartial, "expected at least one non-start Partial event")

			// Verify DoneEvent message metadata
			assert.Equal(t, tt.wantResponseID, done.Message.ResponseID)
			assert.Equal(t, tt.wantResponseModel, done.Message.ResponseModel)
			assert.Equal(t, tt.wantRawStopReason, done.Message.RawStopReason)
			assert.Equal(t, tt.wantStopReason, done.Reason)
			assert.Equal(t, tt.wantStopReason, done.Message.StopReason)
		})
	}
}
