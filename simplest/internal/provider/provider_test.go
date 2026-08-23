package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		Cost:  types.ModelCostRates{Input: 1, Output: 2},
		Input: []string{"text", "image"},
	}
}

func gModel(url string) *types.Model {
	return &types.Model{
		ID: "gemini-3-flash", Name: "Gemini", API: types.APIGoogleGenerativeAI, Provider: "google",
		BaseURL: url, Reasoning: true, ContextWindow: 1e6, MaxTokens: 8192,
		Cost:  types.ModelCostRates{Input: 0.3, Output: 2.5},
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
	srv := sseServer(t, []string{`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]"}, &capturedBody, nil, "")
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

	b := capturedBody
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

var capturedBody map[string]any

func TestOpenAISystemPromptAndRoles(t *testing.T) {
	srv := sseServer(t, []string{`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]"}, &capturedBody2, nil, "")
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
	msgs := capturedBody2["messages"].([]any)
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

var capturedBody2 map[string]any

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
