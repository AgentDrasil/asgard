package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageCodecRoundTrip(t *testing.T) {
	msgs := []Message{
		&UserMessage{Content: TextOnly("hello"), Timestamp: 1},
		&AssistantMessage{
			Content: []AssistantContent{
				TextContent{Type: TypeText, Text: "hi"},
				ThinkingContent{Type: TypeThinking, Thinking: "hmm", Signature: "sig"},
				ToolCall{Type: TypeToolCall, ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
			},
			API:        APIOpenAICompat,
			Provider:   "p",
			Model:      "m",
			Usage:      Usage{Input: 1, Output: 2},
			StopReason: StopToolUse,
			Timestamp:  2,
		},
		&ToolResultMessage{
			ToolCallID: "c1",
			ToolName:   "bash",
			Content:    json.RawMessage(`[{"type":"text","text":"out"}]`),
			IsError:    true,
			Timestamp:  3,
		},
		&ForeignMessage{
			Role: "system",
			Raw:  json.RawMessage(`{"role":"system","content":"be nice","timestamp":4}`),
		},
	}
	for _, m := range msgs {
		raw, err := MarshalMessage(m)
		if err != nil {
			t.Fatal(err)
		}
		back, err := UnmarshalMessage(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if back.MessageRole() != m.MessageRole() {
			t.Fatalf("role mismatch: %s vs %s", back.MessageRole(), m.MessageRole())
		}
		raw2, err := MarshalMessage(back)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != string(raw2) {
			t.Fatalf("unstable round trip:\n%s\n%s", raw, raw2)
		}
	}
}

func TestUnmarshalNullContent(t *testing.T) {
	m, err := UnmarshalMessage(json.RawMessage(`{"role":"user","content":null,"timestamp":5}`))
	if err != nil {
		t.Fatal(err)
	}
	u := m.(*UserMessage)
	if string(u.Content) != "[]" {
		t.Fatalf("null content must normalize to [], got %s", u.Content)
	}

	am, err := UnmarshalMessage(json.RawMessage(`{"role":"assistant","content":[{"type":"toolCall","id":"x","name":"read","arguments":{"a":1}}],"stopReason":"toolUse","timestamp":6}`))
	if err != nil {
		t.Fatal(err)
	}
	a := am.(*AssistantMessage)
	if len(a.Content) != 1 || a.Content[0].BlockType() != TypeToolCall {
		t.Fatalf("assistant content not decoded: %+v", a.Content)
	}
	tc := a.Content[0].(ToolCall)
	if tc.Name != "read" || string(tc.Arguments) != `{"a":1}` {
		t.Fatalf("tool call fields lost: %+v", tc)
	}
}

func TestUnmarshalForeignRole(t *testing.T) {
	raw := json.RawMessage(`{"role":"bashExecution","content":null,"exitCode":0,"timestamp":5}`)
	m, err := UnmarshalMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	fm, ok := m.(*ForeignMessage)
	if !ok {
		t.Fatalf("want *ForeignMessage, got %T", m)
	}
	if fm.MessageRole() != Role("bashExecution") {
		t.Fatalf("role lost: %q", fm.MessageRole())
	}
	if string(fm.Raw) != string(raw) {
		t.Fatalf("raw JSON changed:\n got %s\nwant %s", fm.Raw, raw)
	}

	out, err := MarshalMessage(fm)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(raw) {
		t.Fatalf("marshal round trip changed bytes:\n got %s\nwant %s", out, raw)
	}
	// MarshalMessage must return a copy, not alias the foreign raw buffer.
	out[0] = 'X'
	if string(fm.Raw) != string(raw) {
		t.Fatal("MarshalMessage must copy the raw JSON")
	}
	if _, err := MarshalMessage(&ForeignMessage{Role: "custom"}); err == nil {
		t.Fatal("expected error for empty foreign raw JSON")
	}

	var head MessageHead
	if err := json.Unmarshal([]byte(`{"role":"user","x":1}`), &head); err != nil || head.Role != RoleUser {
		t.Fatalf("MessageHead decode failed: %+v %v", head, err)
	}
}

func TestModelSupportsImage(t *testing.T) {
	var nilModel *Model
	if nilModel.SupportsImage() {
		t.Error("nil model must not support image")
	}

	mNoImage := &Model{ID: "text-only", Input: []string{"text"}}
	if mNoImage.SupportsImage() {
		t.Error("model without image in Input must not support image")
	}

	mWithImage := &Model{ID: "vision-model", Input: []string{"text", "image"}}
	if !mWithImage.SupportsImage() {
		t.Error("model with image in Input must support image")
	}
}

func TestMessageCodecRoundTripRunMetadata(t *testing.T) {
	assistantMs := int64(1234)
	toolMs := int64(56)

	rawAssistant, err := MarshalMessage(&AssistantMessage{
		Content:       []AssistantContent{TextContent{Type: TypeText, Text: "hi"}},
		API:           APIOpenAICompat,
		Provider:      "p",
		Model:         "m",
		StopReason:    StopStop,
		ThinkingLevel: ThinkingHigh,
		DurationMs:    &assistantMs,
		Timestamp:     2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawAssistant), `"thinkingLevel":"high"`) {
		t.Fatalf("assistant thinkingLevel missing from wire form: %s", rawAssistant)
	}
	if !strings.Contains(string(rawAssistant), `"durationMs":1234`) {
		t.Fatalf("assistant durationMs missing from wire form: %s", rawAssistant)
	}

	back, err := UnmarshalMessage(rawAssistant)
	if err != nil {
		t.Fatal(err)
	}
	am := back.(*AssistantMessage)
	if am.ThinkingLevel != ThinkingHigh {
		t.Fatalf("thinkingLevel lost in round trip: %q", am.ThinkingLevel)
	}
	if am.DurationMs == nil || *am.DurationMs != assistantMs {
		t.Fatalf("assistant durationMs lost in round trip: %v", am.DurationMs)
	}

	rawTool, err := MarshalMessage(&ToolResultMessage{
		ToolCallID: "c1",
		ToolName:   "bash",
		Content:    json.RawMessage(`[{"type":"text","text":"out"}]`),
		IsError:    false,
		DurationMs: &toolMs,
		Timestamp:  3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawTool), `"durationMs":56`) {
		t.Fatalf("tool durationMs missing from wire form: %s", rawTool)
	}
	toolBack, err := UnmarshalMessage(rawTool)
	if err != nil {
		t.Fatal(err)
	}
	trm := toolBack.(*ToolResultMessage)
	if trm.DurationMs == nil || *trm.DurationMs != toolMs {
		t.Fatalf("tool durationMs lost in round trip: %v", trm.DurationMs)
	}

	// Absent metadata stays absent (omitempty).
	rawPlain, err := MarshalMessage(&AssistantMessage{
		Content:    []AssistantContent{},
		StopReason: StopStop,
		Timestamp:  4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawPlain), "thinkingLevel") || strings.Contains(string(rawPlain), "durationMs") {
		t.Fatalf("unset metadata must be omitted: %s", rawPlain)
	}

	// Symmetric omission for a tool result without a duration.
	rawPlainTool, err := MarshalMessage(&ToolResultMessage{
		ToolCallID: "c1",
		ToolName:   "bash",
		Content:    json.RawMessage(`[]`),
		Timestamp:  5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawPlainTool), "durationMs") {
		t.Fatalf("unset tool durationMs must be omitted: %s", rawPlainTool)
	}
}

func TestMessageCodecRoundTripPiMetadata(t *testing.T) {
	totalFromProvider := true
	diagnostics := json.RawMessage(`[{"code":"warn","message":"careful"}]`)
	deferred := json.RawMessage(`{"reason":"limit","retryAfter":5}`)
	nestedCalls := json.RawMessage(`[{"id":"n1","name":"read","arguments":{"p":"x"}}]`)

	assistant := &AssistantMessage{
		Content: []AssistantContent{
			TextContent{Type: TypeText, Text: "hi", TextSignature: "text-sig"},
			ThinkingContent{Type: TypeThinking, Thinking: "think", Signature: "think-sig"},
			ToolCall{
				Type: TypeToolCall, ID: "c1", Name: "read",
				Arguments:        json.RawMessage(`{"p":"x"}`),
				ThoughtSignature: "tsig", Namespace: "ns",
				ArgumentsError: "malformed args",
			},
		},
		API:                   APIOpenAICompat,
		Provider:              "p",
		Model:                 "m",
		Usage:                 Usage{Input: 1, Output: 2, CacheWrite1h: 3, TotalTokens: 3, Cost: Cost{TotalFromProvider: &totalFromProvider}},
		StopReason:            StopToolUse,
		ThinkingLevel:         ThinkingHigh,
		ProviderThinkingLevel: "provider-high",
		Diagnostics:           diagnostics,
		Deferred:              deferred,
		Timestamp:             7,
	}
	toolResult := &ToolResultMessage{
		ToolCallID:  "c1",
		ToolName:    "read",
		Content:     json.RawMessage(`[{"type":"text","text":"out","textSignature":"res-sig"}]`),
		NestedCalls: nestedCalls,
		Timestamp:   8,
	}

	for _, msg := range []Message{assistant, toolResult} {
		raw, err := MarshalMessage(msg)
		if err != nil {
			t.Fatal(err)
		}
		back, err := UnmarshalMessage(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		raw2, err := MarshalMessage(back)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != string(raw2) {
			t.Fatalf("unstable round trip:\n%s\n%s", raw, raw2)
		}
	}

	raw, err := MarshalMessage(assistant)
	if err != nil {
		t.Fatal(err)
	}
	backMsg, err := UnmarshalMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	back := backMsg.(*AssistantMessage)
	if back.ProviderThinkingLevel != "provider-high" {
		t.Fatalf("providerThinkingLevel lost: %q", back.ProviderThinkingLevel)
	}
	if string(back.Diagnostics) != string(diagnostics) {
		t.Fatalf("diagnostics lost: %s", back.Diagnostics)
	}
	if string(back.Deferred) != string(deferred) {
		t.Fatalf("deferred lost: %s", back.Deferred)
	}
	if back.Usage.CacheWrite1h != 3 {
		t.Fatalf("usage.cacheWrite1h lost: %d", back.Usage.CacheWrite1h)
	}
	if back.Usage.Cost.TotalFromProvider == nil || !*back.Usage.Cost.TotalFromProvider {
		t.Fatalf("cost.totalFromProvider lost: %+v", back.Usage.Cost)
	}
	if got := back.Content[0].(TextContent).TextSignature; got != "text-sig" {
		t.Fatalf("textSignature lost: %q", got)
	}
	tc := back.Content[2].(ToolCall)
	if tc.ThoughtSignature != "tsig" || tc.Namespace != "ns" || tc.ArgumentsError != "malformed args" {
		t.Fatalf("tool call metadata lost: %+v", tc)
	}

	rawTool, err := MarshalMessage(toolResult)
	if err != nil {
		t.Fatal(err)
	}
	backToolMsg, err := UnmarshalMessage(rawTool)
	if err != nil {
		t.Fatal(err)
	}
	backTool := backToolMsg.(*ToolResultMessage)
	if string(backTool.NestedCalls) != string(nestedCalls) {
		t.Fatalf("nestedCalls lost: %s", backTool.NestedCalls)
	}
}

func TestToolCallLegacySignature(t *testing.T) {
	legacy := json.RawMessage(`{"type":"toolCall","id":"c1","name":"read","arguments":{"p":"x"},"signature":"legacy","namespace":"ns"}`)

	var tc ToolCall
	if err := json.Unmarshal(legacy, &tc); err != nil {
		t.Fatal(err)
	}
	if tc.ThoughtSignature != "legacy" {
		t.Fatalf("legacy signature not folded into ThoughtSignature: %+v", tc)
	}
	if tc.Namespace != "ns" {
		t.Fatalf("namespace lost: %+v", tc)
	}
	out, err := json.Marshal(tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"thoughtSignature":"legacy"`) {
		t.Fatalf("thoughtSignature not written: %s", out)
	}
	if strings.Contains(string(out), `"signature"`) {
		t.Fatalf("legacy signature key must not be written: %s", out)
	}

	// The same legacy key must work through assistant message decode.
	msgRaw := json.RawMessage(`{"role":"assistant","content":[` + string(legacy) + `],"stopReason":"toolUse","timestamp":1}`)
	msg, err := UnmarshalMessage(msgRaw)
	if err != nil {
		t.Fatal(err)
	}
	am := msg.(*AssistantMessage)
	decoded := am.Content[0].(ToolCall)
	if decoded.ThoughtSignature != "legacy" {
		t.Fatalf("legacy signature lost in message decode: %+v", decoded)
	}
	rawOut, err := MarshalMessage(am)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawOut), `"signature"`) {
		t.Fatalf("message re-marshal wrote legacy signature: %s", rawOut)
	}
	if !strings.Contains(string(rawOut), `"thoughtSignature":"legacy"`) {
		t.Fatalf("message re-marshal lost thoughtSignature: %s", rawOut)
	}

	// An explicit thoughtSignature wins over the legacy key.
	both := json.RawMessage(`{"type":"toolCall","id":"c1","name":"read","arguments":{},"signature":"old","thoughtSignature":"new"}`)
	var tcb ToolCall
	if err := json.Unmarshal(both, &tcb); err != nil {
		t.Fatal(err)
	}
	if tcb.ThoughtSignature != "new" {
		t.Fatalf("thoughtSignature should win over legacy signature: %+v", tcb)
	}
}
