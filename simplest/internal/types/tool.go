package types

import (
	"context"
	"encoding/json"
)

// ToolExecutionMode controls how a batch of tool calls runs.
type ToolExecutionMode string

const (
	ExecutionSequential ToolExecutionMode = "sequential"
	ExecutionParallel   ToolExecutionMode = "parallel"
)

// Tool describes a callable tool exposed to the model.
type Tool interface {
	Name() string
	Description() string
	Label() string
	// Parameters returns a JSON Schema object describing the arguments.
	Parameters() json.RawMessage
	// PromptSnippet is an optional one-line description included in the system
	// prompt; empty means omitted.
	PromptSnippet() string
	// PromptGuidelines are optional extra guideline paragraphs for the system prompt.
	PromptGuidelines() []string
	// ExecutionMode overrides the run default; empty means inherit.
	ExecutionMode() ToolExecutionMode
}

// ToolResult is what a tool execution produces.
type ToolResult struct {
	// Content blocks returned to the model (text and image only).
	Content []AssistantContent
	// Details is arbitrary structured data for logs/UI; never sent to the LLM.
	Details   any
	Usage     *Usage
	Terminate bool
}

// UpdateFunc receives streamed partial results during tool execution.
type UpdateFunc func(partial *ToolResult)

// AgentTool is a Tool with an executor.
type AgentTool interface {
	Tool
	// Execute runs the tool. Return an error to produce an error tool result.
	Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate UpdateFunc) (*ToolResult, error)
}
