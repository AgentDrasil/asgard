package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

const editDocSchemaJSON = `{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Path to the markdown file to edit (relative or absolute); must end with .md" },
    "edits": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "oldText": { "type": "string", "description": "Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call." },
          "newText": { "type": "string", "description": "Replacement text for this targeted edit." }
        },
        "required": ["oldText", "newText"],
        "additionalProperties": false
      },
      "description": "One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead."
    }
  },
  "required": ["path", "edits"],
  "additionalProperties": false
}`

// EditDocTool edits markdown documentation files only. It is the
// documentation-editing counterpart of edit for analysis and review agents
// that must not touch source code.
type EditDocTool struct {
	edit   *EditTool
	policy DocPathPolicy
}

// NewEditDocTool creates a documentation edit tool rooted at cwd, governed by
// opts. Only markdown files are accepted; when opts.AllowedDirs is set,
// edits are additionally confined to those directories.
func NewEditDocTool(cwd string, opts DocToolOptions) *EditDocTool {
	return &EditDocTool{edit: NewEditTool(cwd), policy: DocPathPolicy(opts)}
}

func (t *EditDocTool) Name() string  { return "edit_doc" }
func (t *EditDocTool) Label() string { return "edit_doc" }
func (t *EditDocTool) Parameters() json.RawMessage {
	return json.RawMessage(editDocSchemaJSON)
}
func (t *EditDocTool) PromptSnippet() string {
	return "Make precise edits to markdown documentation files (.md only)"
}
func (t *EditDocTool) PromptGuidelines() []string {
	return []string{
		"Use edit_doc for precise revisions of markdown documents such as plans and reports (edits[].oldText must match exactly)",
		"Each edits[].oldText is matched against the original file, not after earlier edits are applied. Merge nearby changes into one edit.",
	}
}
func (t *EditDocTool) ExecutionMode() types.ToolExecutionMode {
	return types.ExecutionSequential
}

func (t *EditDocTool) Description() string {
	return "Edit a markdown documentation file using exact text replacement. The path must end with .md; any other file type is rejected. Every edits[].oldText must match a unique, non-overlapping region of the original file."
}

// Execute validates the markdown path and delegates to the edit tool,
// editing the symlink-resolved path returned by the policy so the
// validated target and the edited target are identical.
func (t *EditDocTool) Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("operation aborted")
	}
	normArgs, err := NormalizeEditArgs(args)
	if err != nil {
		return nil, err
	}
	in, err := parseEditArgs(normArgs)
	if err != nil {
		return nil, err
	}
	resolved, err := t.policy.Resolve(in.Path, t.edit.cwd)
	if err != nil {
		return nil, fmt.Errorf("edit_doc: %w", err)
	}
	edits := make([]map[string]string, len(in.Edits))
	for i, e := range in.Edits {
		edits[i] = map[string]string{"oldText": e.OldText, "newText": e.NewText}
	}
	resolvedArgs, err := json.Marshal(map[string]any{"path": resolved, "edits": edits})
	if err != nil {
		return nil, err
	}
	return t.edit.Execute(ctx, toolCallID, resolvedArgs, onUpdate)
}
