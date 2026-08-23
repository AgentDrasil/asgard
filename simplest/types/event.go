package types

// AssistantMessageEvent is the per-provider streaming protocol. A stream emits
// Start first, then partial updates, then terminates with exactly one Done or
// StreamError event.
type AssistantMessageEvent interface{}

type AssistantMessageEventKind string

const (
	EvStart         AssistantMessageEventKind = "start"
	EvTextStart     AssistantMessageEventKind = "text_start"
	EvTextDelta     AssistantMessageEventKind = "text_delta"
	EvTextEnd       AssistantMessageEventKind = "text_end"
	EvThinkingStart AssistantMessageEventKind = "thinking_start"
	EvThinkingDelta AssistantMessageEventKind = "thinking_delta"
	EvThinkingEnd   AssistantMessageEventKind = "thinking_end"
	EvToolcallStart AssistantMessageEventKind = "toolcall_start"
	EvToolcallDelta AssistantMessageEventKind = "toolcall_delta"
	EvToolcallEnd   AssistantMessageEventKind = "toolcall_end"
	EvDone          AssistantMessageEventKind = "done"
	EvStreamError   AssistantMessageEventKind = "error"
)

// Partial is embedded in every non-terminal assistant stream event.
type Partial struct {
	Kind         AssistantMessageEventKind `json:"type"`
	ContentIndex int                       `json:"contentIndex"`
	Delta        string                    `json:"delta,omitempty"`
	Content      string                    `json:"content,omitempty"`
	ToolCall     *ToolCall                 `json:"toolCall,omitempty"`
	Partial      *AssistantMessage         `json:"partial"`
}

// DoneEvent terminates a successful stream.
type DoneEvent struct {
	Kind    AssistantMessageEventKind `json:"type"`   // "done"
	Reason  StopReason                `json:"reason"` // stop | length | toolUse | deferred
	Message *AssistantMessage         `json:"message"`
}

// StreamErrorEvent terminates a failed or aborted stream.
type StreamErrorEvent struct {
	Kind    AssistantMessageEventKind `json:"type"`   // "error"
	Reason  StopReason                `json:"reason"` // aborted | error
	Message *AssistantMessage         `json:"message"`
}

// Agent-level events, delivered on the Run event channel.

type AgentEventKind string

const (
	AgentStart          AgentEventKind = "agent_start"
	AgentEnd            AgentEventKind = "agent_end"
	TurnStart           AgentEventKind = "turn_start"
	TurnEnd             AgentEventKind = "turn_end"
	MessageStart        AgentEventKind = "message_start"
	MessageUpdate       AgentEventKind = "message_update"
	MessageEnd          AgentEventKind = "message_end"
	ToolExecutionStart  AgentEventKind = "tool_execution_start"
	ToolExecutionUpdate AgentEventKind = "tool_execution_update"
	ToolExecutionEnd    AgentEventKind = "tool_execution_end"
)

// AgentEvent is one lifecycle event of a run. Kind discriminates the union;
// unused fields are zero.
type AgentEvent struct {
	Kind AgentEventKind `json:"type"`

	// agent_end: all messages produced by this run.
	Messages []Message `json:"messages,omitempty"`

	// turn_end: the assistant message that completed the turn plus its tool results.
	Message     *AssistantMessage      `json:"-"`
	AssistantEv *AssistantMessageEvent `json:"-"`
	ToolResults []*ToolResultMessage   `json:"-"`

	// message_*: the message being started/updated/ended (user, assistant, toolResult).
	UserMsg *UserMessage       `json:"-"`
	ToolMsg *ToolResultMessage `json:"-"`

	// tool_execution_*
	ToolCallID    string      `json:"toolCallId,omitempty"`
	ToolName      string      `json:"toolName,omitempty"`
	Args          any         `json:"-"`
	PartialResult *ToolResult `json:"-"`
	Result        *ToolResult `json:"-"`
	IsError       bool        `json:"isError,omitempty"`
}
