package tools

import (
	"context"
	"encoding/json"

	"github.com/AgentDrasil/asgard/simplest/types"
)

// Func adapts a plain Go function into an AgentTool so plugins can register
// custom tools without implementing the full interface.
type Func struct {
	ToolName        string
	ToolDescription string
	ToolLabel       string
	ToolParams      json.RawMessage // JSON Schema object; defaults to empty object schema
	Snippet         string          // one-line description for the system prompt
	Guidelines      []string        // extra system-prompt guideline paragraphs
	Mode            types.ToolExecutionMode
	Fn              func(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error)
}

var _ types.AgentTool = (*Func)(nil)

func (f *Func) Name() string                           { return f.ToolName }
func (f *Func) Description() string                    { return f.ToolDescription }
func (f *Func) Label() string                          { return f.ToolLabel }
func (f *Func) PromptSnippet() string                  { return f.Snippet }
func (f *Func) PromptGuidelines() []string             { return f.Guidelines }
func (f *Func) ExecutionMode() types.ToolExecutionMode { return f.Mode }

func (f *Func) Parameters() json.RawMessage {
	if len(f.ToolParams) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return f.ToolParams
}

func (f *Func) Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	if f.Fn == nil {
		return nil, context.Canceled
	}
	return f.Fn(ctx, toolCallID, args, onUpdate)
}
