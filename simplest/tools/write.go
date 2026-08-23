package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AgentDrasil/asgard/simplest/types"
)

const writeSchemaJSON = `{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Path to the file to write (relative or absolute)" },
    "content": { "type": "string", "description": "Content to write to the file" }
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`

// WriteTool creates or overwrites files, creating parent directories as needed.
type WriteTool struct {
	cwd string
}

// NewWriteTool creates a write tool rooted at cwd.
func NewWriteTool(cwd string) *WriteTool {
	return &WriteTool{cwd: cwd}
}

func (t *WriteTool) Name() string  { return "write" }
func (t *WriteTool) Label() string { return "write" }
func (t *WriteTool) Parameters() json.RawMessage {
	return json.RawMessage(writeSchemaJSON)
}
func (t *WriteTool) PromptSnippet() string { return "Create or overwrite files" }
func (t *WriteTool) PromptGuidelines() []string {
	return []string{"Use write only for new files or complete rewrites."}
}
func (t *WriteTool) ExecutionMode() types.ToolExecutionMode { return "" }

func (t *WriteTool) Description() string {
	return "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories."
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Execute writes content to the file after creating parent directories.
func (t *WriteTool) Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("operation aborted")
	}
	var in writeArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	absolutePath := resolveToCwd(in.Path, t.cwd)
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(absolutePath, []byte(in.Content), 0o644); err != nil {
		return nil, err
	}
	return &types.ToolResult{
		Content: []types.AssistantContent{
			types.TextContent{Type: types.TypeText, Text: fmt.Sprintf("Successfully wrote %d bytes to %s", len(in.Content), in.Path)},
		},
	}, nil
}
