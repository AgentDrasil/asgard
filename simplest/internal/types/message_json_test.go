package types

import (
	"encoding/json"
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
