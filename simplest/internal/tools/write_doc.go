package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

const writeDocSchemaJSON = `{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Path to the markdown file to write (relative or absolute); must end with .md" },
    "content": { "type": "string", "description": "Markdown content to write to the file" }
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`

// WriteDocTool creates or overwrites markdown documentation files only. It is
// the documentation-writing counterpart of write for analysis and review
// agents that must not touch source code.
type WriteDocTool struct {
	write  *WriteTool
	policy DocPathPolicy
}

// NewWriteDocTool creates a documentation write tool rooted at cwd, governed
// by opts. Only markdown files are accepted; when opts.AllowedDirs is set,
// writes are additionally confined to those directories.
func NewWriteDocTool(cwd string, opts DocToolOptions) *WriteDocTool {
	return &WriteDocTool{write: NewWriteTool(cwd), policy: DocPathPolicy(opts)}
}

func (t *WriteDocTool) Name() string  { return "write_doc" }
func (t *WriteDocTool) Label() string { return "write_doc" }
func (t *WriteDocTool) Parameters() json.RawMessage {
	return json.RawMessage(writeDocSchemaJSON)
}
func (t *WriteDocTool) PromptSnippet() string {
	return "Create or overwrite markdown documentation files (.md only)"
}
func (t *WriteDocTool) PromptGuidelines() []string {
	return []string{"Use write_doc for markdown documents such as reports, plans, and session artifacts. Source code files cannot be written."}
}
func (t *WriteDocTool) ExecutionMode() types.ToolExecutionMode { return "" }

func (t *WriteDocTool) Description() string {
	return "Write markdown content to a documentation file. The path must end with .md; any other file type is rejected. Creates the file if it doesn't exist, overwrites if it does, and creates parent directories as needed."
}

// Execute validates the markdown path and delegates to the write tool,
// writing to the symlink-resolved path returned by the policy so the
// validated target and the written target are identical.
func (t *WriteDocTool) Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("operation aborted")
	}
	var in writeArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	resolved, err := t.policy.Resolve(in.Path, t.write.cwd)
	if err != nil {
		return nil, fmt.Errorf("write_doc: %w", err)
	}
	in.Path = resolved
	resolvedArgs, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	return t.write.Execute(ctx, toolCallID, resolvedArgs, onUpdate)
}
