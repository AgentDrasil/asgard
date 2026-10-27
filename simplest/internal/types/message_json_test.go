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

func TestUnmarshalUnknownRole(t *testing.T) {
	if _, err := UnmarshalMessage(json.RawMessage(`{"role":"weird"}`)); err == nil {
		t.Fatal("expected error for unknown role")
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
