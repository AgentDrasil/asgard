package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func orModel(url string) *types.Model {
	m := oaModel(url)
	m.API = types.APIOpenAIResponses
	return m
}

func TestOpenAIResponsesStreamTextReasoningUsage(t *testing.T) {
	var captured map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			`{"type":"response.created","response":{"id":"resp_1"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"think"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"text":"thought"}]}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1"}}`,
			`{"type":"response.output_text.delta","output_index":1,"delta":"hel"}`,
			`{"type":"response.output_text.delta","output_index":1,"delta":"lo"}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"hello"}]}}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":40},"output_tokens_details":{"reasoning_tokens":5}}}}`,
		} {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", e)
		}
	}))
	defer srv.Close()

	temp := 0.3
	mt := int64(4) // below the API minimum
	opts := &types.StreamOptions{Temperature: &temp, MaxTokens: &mt, ThinkingLevel: types.ThinkingMedium}
	evs, done, errEv := drain(NewOpenAIResponses("k").Stream(context.Background(), orModel(srv.URL), simpleContext(), opts))
	require.Nil(t, errEv)
	require.NotNil(t, done)

	assert.Equal(t, "/responses", path)
	assert.Equal(t, "gpt-test", captured["model"])
	assert.Equal(t, true, captured["stream"])
	assert.Equal(t, false, captured["store"])
	assert.Equal(t, float64(16), captured["max_output_tokens"])
	assert.Equal(t, 0.3, captured["temperature"])
	assert.Equal(t, map[string]any{"effort": "medium", "summary": "auto"}, captured["reasoning"])
	assert.Equal(t, []any{"reasoning.encrypted_content"}, captured["include"])
	input := captured["input"].([]any)
	require.Len(t, input, 2)
	assert.Equal(t, map[string]any{"role": "developer", "content": "be brief"}, input[0])
	user := input[1].(map[string]any)
	assert.Equal(t, "user", user["role"])
	assert.Equal(t, "input_text", user["content"].([]any)[0].(map[string]any)["type"])

	assert.Equal(t, []string{"start", "thinking_start", "thinking_delta", "thinking_end", "text_start", "text_delta", "text_delta", "text_end", "done"}, eventKinds(evs))
	msg := done.Message
	assert.Equal(t, types.StopStop, done.Reason)
	assert.Equal(t, "resp_1", msg.ResponseID)
	th := msg.Content[0].(types.ThinkingContent)
	assert.Equal(t, "thought", th.Thinking)
	assert.Contains(t, th.Signature, `"rs_1"`)
	tx := msg.Content[1].(types.TextContent)
	assert.Equal(t, "hello", tx.Text)
	assert.Equal(t, `{"v":1,"id":"msg_1"}`, tx.TextSignature)
	assert.Equal(t, int64(60), msg.Usage.Input)
	assert.Equal(t, int64(40), msg.Usage.CacheRead)
	assert.Equal(t, int64(20), msg.Usage.Output)
	assert.Equal(t, int64(120), msg.Usage.TotalTokens)
	require.NotNil(t, msg.Usage.Reasoning)
	assert.Equal(t, int64(5), *msg.Usage.Reasoning)
}

func TestOpenAIResponsesToolCallStreaming(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"cmd\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"ls\""}`,
		`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"cmd\":\"ls\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	}, &captured, nil, "/responses")
	defer srv.Close()
	cx := simpleContext()
	cx.Tools = []types.ToolDef{{Name: "bash", Description: "run", Parameters: json.RawMessage(`{"type":"object"}`)}}
	evs, done, errEv := drain(NewOpenAIResponses("k").Stream(context.Background(), orModel(srv.URL), cx, nil))
	require.Nil(t, errEv)
	require.NotNil(t, done)
	assert.Equal(t, types.StopToolUse, done.Reason)
	tc := done.Message.Content[0].(types.ToolCall)
	assert.Equal(t, "call_1|fc_1", tc.ID)
	assert.Equal(t, "bash", tc.Name)
	assert.JSONEq(t, `{"cmd":"ls"}`, string(tc.Arguments))
	assert.Equal(t, []string{"start", "toolcall_start", "toolcall_delta", "toolcall_delta", "toolcall_delta", "toolcall_end", "done"}, eventKinds(evs))

	tools := captured["tools"].([]any)
	tool := tools[0].(map[string]any)
	assert.Equal(t, "function", tool["type"])
	assert.Equal(t, "bash", tool["name"])
	assert.Equal(t, false, tool["strict"])
	_, nested := tool["function"]
	assert.False(t, nested)
}

func TestOpenAIResponsesUnfinishedToolCallFails(t *testing.T) {
	srv := sseServer(t, []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	}, nil, nil, "")
	defer srv.Close()
	_, _, errEv := drain(NewOpenAIResponses("k").Stream(context.Background(), orModel(srv.URL), simpleContext(), nil))
	require.NotNil(t, errEv)
	assert.Contains(t, errEv.Message.ErrorMessage, "unfinished tool call")
}

func TestOpenAIResponsesTerminalStates(t *testing.T) {
	run := func(events ...string) (*types.DoneEvent, *types.StreamErrorEvent) {
		srv := sseServer(t, events, nil, nil, "")
		defer srv.Close()
		_, d, e := drain(NewOpenAIResponses("k").Stream(context.Background(), orModel(srv.URL), simpleContext(), nil))
		return d, e
	}

	d, e := run(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`)
	require.Nil(t, e)
	assert.Equal(t, types.StopLength, d.Reason)
	assert.Equal(t, "incomplete.max_output_tokens", d.Message.RawStopReason)

	_, e = run(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`)
	require.NotNil(t, e)
	assert.Equal(t, "Response incomplete: content_filter", e.Message.ErrorMessage)

	_, e = run(`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}`)
	require.NotNil(t, e)
	assert.Equal(t, "server_error: boom", e.Message.ErrorMessage)

	_, e = run(`{"type":"error","code":"rate_limit","message":"slow down"}`)
	require.NotNil(t, e)
	assert.Equal(t, "Error Code rate_limit: slow down", e.Message.ErrorMessage)

	_, e = run(`{"type":"response.output_text.delta","output_index":0,"delta":"x"}`)
	require.NotNil(t, e)
	assert.Contains(t, e.Message.ErrorMessage, "terminal response event")
}

func TestOpenAIResponsesHTTPErrorAndMissingKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()
	_, _, e := drain(NewOpenAIResponses("k").Stream(context.Background(), orModel(srv.URL), simpleContext(), nil))
	require.NotNil(t, e)
	assert.Contains(t, e.Message.ErrorMessage, "401")
	assert.Contains(t, e.Message.ErrorMessage, "bad key")

	_, _, e = drain(NewOpenAIResponses("").Stream(context.Background(), orModel(srv.URL), simpleContext(), nil))
	require.NotNil(t, e)
	assert.Contains(t, e.Message.ErrorMessage, "no API key")
}

func TestOpenAIResponsesConvertMessagesReplay(t *testing.T) {
	m := orModel("")
	m.Input = []string{"text", "image"}
	sig := `{"type":"reasoning","id":"rs_1","summary":[]}`
	cx := &types.Context{
		Messages: []types.Message{
			&types.UserMessage{Content: types.TextOnly("go"), Timestamp: 1},
			&types.AssistantMessage{
				Provider: m.Provider, API: m.API, Model: m.ID,
				Content: []types.AssistantContent{
					types.ThinkingContent{Type: types.TypeThinking, Thinking: "t", Signature: sig},
					types.TextContent{Type: types.TypeText, Text: "ok", TextSignature: `{"v":1,"id":"msg_9","phase":"commentary"}`},
					types.ToolCall{Type: types.TypeToolCall, ID: "call/1|fc_7", Name: "bash", Arguments: json.RawMessage(`{"a":1}`)},
				},
			},
			&types.AssistantMessage{
				Provider: "other", API: "gemini", Model: "g",
				Content: []types.AssistantContent{
					types.ThinkingContent{Type: types.TypeThinking, Thinking: "x", Signature: "foreign"},
					types.ToolCall{Type: types.TypeToolCall, ID: "call_2|abc", Name: "bash", Arguments: json.RawMessage(`{}`)},
				},
			},
		},
	}
	input, err := NewOpenAIResponses("k").ConvertMessages(m, cx)
	require.NoError(t, err)
	b, _ := json.Marshal(input)
	var items []map[string]any
	require.NoError(t, json.Unmarshal(b, &items))
	require.Len(t, items, 5)
	assert.Equal(t, "rs_1", items[1]["id"])
	assert.Equal(t, "msg_9", items[2]["id"])
	assert.Equal(t, "commentary", items[2]["phase"])
	assert.Equal(t, "function_call", items[3]["type"])
	assert.Equal(t, "call_1", items[3]["call_id"])
	assert.Equal(t, "fc_7", items[3]["id"])
	assert.Equal(t, `{"a":1}`, items[3]["arguments"])
	assert.Equal(t, "call_2", items[4]["call_id"])
	assert.True(t, strings.HasPrefix(items[4]["id"].(string), "fc_"))
}

func TestOpenAIResponsesToolResultOutput(t *testing.T) {
	m := orModel("")
	text, _ := json.Marshal([]types.AssistantContent{types.TextContent{Type: types.TypeText, Text: "done"}})
	cx := &types.Context{Messages: []types.Message{
		&types.ToolResultMessage{ToolCallID: "call_1|fc_1", ToolName: "bash", Content: text},
	}}
	input, err := NewOpenAIResponses("k").ConvertMessages(m, cx)
	require.NoError(t, err)
	require.Len(t, input, 1)
	item := input[0].(map[string]any)
	assert.Equal(t, "function_call_output", item["type"])
	assert.Equal(t, "call_1", item["call_id"])
	assert.Equal(t, "done", item["output"])
}

func TestOpenAIResponsesReasoningOffAndInvalid(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{`{"type":"response.completed","response":{"status":"completed"}}`}, &captured, nil, "")
	defer srv.Close()
	_, _, e := drain(NewOpenAIResponses("k").Stream(context.Background(), orModel(srv.URL), simpleContext(), &types.StreamOptions{ThinkingLevel: types.ThinkingOff}))
	require.Nil(t, e)
	assert.Equal(t, map[string]any{"effort": "none"}, captured["reasoning"])

	m := orModel(srv.URL)
	m.ReasoningEffort = []string{"low"}
	_, _, e = drain(NewOpenAIResponses("k").Stream(context.Background(), m, simpleContext(), &types.StreamOptions{ThinkingLevel: types.ThinkingHigh}))
	require.NotNil(t, e)
	assert.Contains(t, e.Message.ErrorMessage, "unsupported reasoning effort")
}
