// Package types defines the shared data model for the Go agent:
// messages, content blocks, streaming events, tools, models, and contexts.
//
// The JSON shape of messages is stable so session files can stay compatible.
package types

import "encoding/json"

// Role discriminates LLM message kinds.
type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "toolResult"
)

// StopReason explains why an assistant turn ended.
type StopReason string

const (
	StopPending  StopReason = "pending"
	StopStop     StopReason = "stop"
	StopLength   StopReason = "length"
	StopToolUse  StopReason = "toolUse"
	StopError    StopReason = "error"
	StopAborted  StopReason = "aborted"
	StopDeferred StopReason = "deferred"
)

// ThinkingLevel is the requested reasoning effort for future turns.
type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

// Usage holds token accounting for one assistant response.
type Usage struct {
	Input       int64  `json:"input"`
	Output      int64  `json:"output"`
	CacheRead   int64  `json:"cacheRead"`
	CacheWrite  int64  `json:"cacheWrite"`
	Reasoning   *int64 `json:"reasoning,omitempty"`
	TotalTokens int64  `json:"totalTokens"`
	Cost        Cost   `json:"cost"`
}

// Cost holds dollar accounting derived from Usage.
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

// Content blocks.

// TextContent is a plain text block.
type TextContent struct {
	Type string `json:"type"` // always "text"
	Text string `json:"text"`
}

// ThinkingContent is a reasoning block; Signature carries provider replay data.
type ThinkingContent struct {
	Type      string `json:"type"` // always "thinking"
	Thinking  string `json:"thinking"`
	Signature string `json:"thinkingSignature,omitempty"`
	Redacted  bool   `json:"redacted,omitempty"`
}

// ImageContent is a base64-encoded image block.
type ImageContent struct {
	Type     string `json:"type"` // always "image"
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// ToolCall is an assistant-requested tool invocation.
type ToolCall struct {
	Type      string          `json:"type"` // always "toolCall"
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	// Signature carries the provider's opaque thought signature (Gemini 3
	// thoughtSignature) that must be echoed back with the tool call.
	Signature string `json:"signature,omitempty"`
}

const (
	TypeText     = "text"
	TypeThinking = "thinking"
	TypeImage    = "image"
	TypeToolCall = "toolCall"
)

// Message is the sealed union of UserMessage, AssistantMessage, and
// ToolResultMessage. Implementations are the pointer structs below.
type Message interface {
	MessageRole() Role
}

// UserMessage is a user-authored turn.
type UserMessage struct {
	Content   json.RawMessage `json:"content"`   // string or []TextContent|ImageContent
	Timestamp int64           `json:"timestamp"` // unix millis
}

func (m *UserMessage) MessageRole() Role { return RoleUser }

// AssistantMessage is one model response.
type AssistantMessage struct {
	Content       []AssistantContent `json:"content"`
	API           string             `json:"api"`
	Provider      string             `json:"provider"`
	Model         string             `json:"model"`
	ResponseModel string             `json:"responseModel,omitempty"`
	ResponseID    string             `json:"responseId,omitempty"`
	Usage         Usage              `json:"usage"`
	StopReason    StopReason         `json:"stopReason"`
	ErrorMessage  string             `json:"errorMessage,omitempty"`
	RawStopReason string             `json:"rawStopReason,omitempty"`
	EndTurn       *bool              `json:"endTurn,omitempty"`
	Timestamp     int64              `json:"timestamp"` // unix millis
}

func (m *AssistantMessage) MessageRole() Role { return RoleAssistant }

// ToolResultMessage reports one executed tool call.
type ToolResultMessage struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Content    json.RawMessage `json:"content"` // []TextContent|ImageContent
	Details    json.RawMessage `json:"details,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
	IsError    bool            `json:"isError"`
	Timestamp  int64           `json:"timestamp"` // unix millis
}

func (m *ToolResultMessage) MessageRole() Role { return RoleToolResult }

// AssistantContent is a content block inside an AssistantMessage.
type AssistantContent interface {
	BlockType() string
}

func (TextContent) BlockType() string     { return TypeText }
func (ThinkingContent) BlockType() string { return TypeThinking }
func (ImageContent) BlockType() string    { return TypeImage }
func (ToolCall) BlockType() string        { return TypeToolCall }

// Helpers to decode polymorphic content.

// DecodeUserContent decodes UserMessage.Content into text and image blocks.
// A bare JSON string is accepted and returned as a single TextContent.
func DecodeUserContent(raw json.RawMessage) ([]AssistantContent, error) {
	return decodeBlockList(raw, true)
}

// DecodeToolResultContent decodes ToolResultMessage.Content.
func DecodeToolResultContent(raw json.RawMessage) ([]AssistantContent, error) {
	return decodeBlockList(raw, false)
}

// MarshalBlocks encodes a slice of content blocks as their JSON array form.
func MarshalBlocks(blocks []AssistantContent) (json.RawMessage, error) {
	if blocks == nil {
		return json.RawMessage("[]"), nil
	}
	b, err := json.Marshal(blocks)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// TextOnly builds a user message content value that is a bare JSON string,
// the compact representation for plain-text prompts.
func TextOnly(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}

// StringContentOf extracts concatenated text from decoded blocks.
func StringContentOf(blocks []AssistantContent) string {
	out := ""
	for _, blk := range blocks {
		switch t := blk.(type) {
		case TextContent:
			out += t.Text
		case *TextContent:
			out += t.Text
		}
	}
	return out
}

func decodeBlockList(raw json.RawMessage, allowString bool) ([]AssistantContent, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if allowString {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return []AssistantContent{TextContent{Type: TypeText, Text: s}}, nil
		}
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	var out []AssistantContent
	for _, item := range list {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &head); err != nil {
			return nil, err
		}
		switch head.Type {
		case TypeText:
			var v TextContent
			if err := json.Unmarshal(item, &v); err != nil {
				return nil, err
			}
			out = append(out, v)
		case TypeThinking:
			var v ThinkingContent
			if err := json.Unmarshal(item, &v); err != nil {
				return nil, err
			}
			out = append(out, v)
		case TypeImage:
			var v ImageContent
			if err := json.Unmarshal(item, &v); err != nil {
				return nil, err
			}
			out = append(out, v)
		case TypeToolCall:
			var v ToolCall
			if err := json.Unmarshal(item, &v); err != nil {
				return nil, err
			}
			out = append(out, v)
		}
	}
	return out, nil
}
