package types

import (
	"encoding/json"
	"fmt"
)

// MarshalMessage encodes a Message as its role envelope,
// i.e. a JSON object whose "role" field discriminates the concrete type.
func MarshalMessage(m Message) (json.RawMessage, error) {
	switch t := m.(type) {
	case *UserMessage:
		return marshalEnveloped(RoleUser, t)
	case *AssistantMessage:
		return marshalEnveloped(RoleAssistant, t)
	case *ToolResultMessage:
		return marshalEnveloped(RoleToolResult, t)
	default:
		return nil, fmt.Errorf("unknown message role %T", m)
	}
}

// marshalEnveloped marshals the role field first,
// then splices in the body object's remaining fields.
func marshalEnveloped(role Role, body any) (json.RawMessage, error) {
	bb, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	head := `{"role":` + mustQuote(string(role))
	if string(bb) == "null" || len(bb) < 2 || bb[0] != '{' {
		return nil, fmt.Errorf("cannot envelope message body %s", bb)
	}
	out := make(json.RawMessage, 0, len(head)+len(bb))
	out = append(out, head...)
	out = append(out, ',')
	out = append(out, bb[1:]...)
	return out, nil
}

func mustQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// DecodeAssistantContent decodes AssistantMessage.Content blocks
// (text | thinking | image | toolCall). A bare JSON string is rejected.
func DecodeAssistantContent(raw json.RawMessage) ([]AssistantContent, error) {
	return decodeBlockList(raw, false)
}

// MessageHead is the role discriminator prefix of an enveloped message.
type MessageHead struct {
	Role Role `json:"role"`
}

// UnmarshalMessage decodes a role-enveloped message produced by MarshalMessage
// or other writers of the same format. Unknown roles yield an error; null content is
// normalized to an empty block list.
func UnmarshalMessage(raw json.RawMessage) (Message, error) {
	var head MessageHead
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, err
	}
	switch head.Role {
	case RoleUser:
		var m UserMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		if len(m.Content) == 0 || string(m.Content) == "null" {
			m.Content = json.RawMessage("[]")
		}
		return &m, nil
	case RoleAssistant:
		var probe struct {
			Content       json.RawMessage `json:"content"`
			API           string          `json:"api"`
			Provider      string          `json:"provider"`
			Model         string          `json:"model"`
			ResponseModel string          `json:"responseModel,omitempty"`
			ResponseID    string          `json:"responseId,omitempty"`
			Usage         Usage           `json:"usage"`
			StopReason    StopReason      `json:"stopReason"`
			ErrorMessage  string          `json:"errorMessage,omitempty"`
			RawStopReason string          `json:"rawStopReason,omitempty"`
			EndTurn       *bool           `json:"endTurn,omitempty"`
			Timestamp     int64           `json:"timestamp"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, err
		}
		blocks, err := DecodeAssistantContent(probe.Content)
		if err != nil {
			return nil, err
		}
		if blocks == nil {
			blocks = []AssistantContent{}
		}
		return &AssistantMessage{
			Content:       blocks,
			API:           probe.API,
			Provider:      probe.Provider,
			Model:         probe.Model,
			ResponseModel: probe.ResponseModel,
			ResponseID:    probe.ResponseID,
			Usage:         probe.Usage,
			StopReason:    probe.StopReason,
			ErrorMessage:  probe.ErrorMessage,
			RawStopReason: probe.RawStopReason,
			EndTurn:       probe.EndTurn,
			Timestamp:     probe.Timestamp,
		}, nil
	case RoleToolResult:
		var m ToolResultMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		if len(m.Content) == 0 || string(m.Content) == "null" {
			m.Content = json.RawMessage("[]")
		}
		return &m, nil
	default:
		return nil, fmt.Errorf("unknown message role %q", string(head.Role))
	}
}
