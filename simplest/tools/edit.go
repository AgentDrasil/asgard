package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/AgentDrasil/asgard/simplest/types"
)

const editSchemaJSON = `{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Path to the file to edit (relative or absolute)" },
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

// EditToolDetails carries the display diff and unified patch of the change.
type EditToolDetails struct {
	Diff             string `json:"diff"`
	Patch            string `json:"patch"`
	FirstChangedLine int    `json:"firstChangedLine,omitempty"` // -1 when unchanged
}

// EditTool edits files via exact (with fuzzy fallback) text replacement.
type EditTool struct {
	cwd string
}

// NewEditTool creates an edit tool rooted at cwd.
func NewEditTool(cwd string) *EditTool {
	return &EditTool{cwd: cwd}
}

func (t *EditTool) Name() string  { return "edit" }
func (t *EditTool) Label() string { return "edit" }
func (t *EditTool) Parameters() json.RawMessage {
	return json.RawMessage(editSchemaJSON)
}
func (t *EditTool) PromptSnippet() string {
	return "Make precise file edits with exact text replacement, including multiple disjoint edits in one call"
}
func (t *EditTool) PromptGuidelines() []string {
	return []string{
		"Use edit for precise changes (edits[].oldText must match exactly)",
		"When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls",
		"Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.",
		"Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
	}
}
func (t *EditTool) ExecutionMode() types.ToolExecutionMode {
	return types.ExecutionSequential
}

func (t *EditTool) Description() string {
	return "Edit a single file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes."
}

type editArgsRaw struct {
	Path    string          `json:"path"`
	Edits   json.RawMessage `json:"edits"`
	OldText string          `json:"oldText,omitempty"`
	NewText string          `json:"newText,omitempty"`
}

type editArgs struct {
	Path  string
	Edits []Edit
}

// NormalizeEditArgs applies pi's prepareArguments shim: some models send edits
// as a JSON string instead of an array, a single edit object instead of a
// one-element array, or legacy top-level oldText/newText fields.
func NormalizeEditArgs(args json.RawMessage) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return args, nil
	}

	fixEdits := func(v json.RawMessage) ([]json.RawMessage, bool) {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			var parsed json.RawMessage
			if err := json.Unmarshal([]byte(s), &parsed); err != nil {
				return nil, false
			}
			v = parsed
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(v, &arr); err == nil {
			return arr, true
		}
		var single struct {
			OldText *string `json:"oldText"`
			NewText *string `json:"newText"`
		}
		if err := json.Unmarshal(v, &single); err == nil && single.OldText != nil && single.NewText != nil {
			return []json.RawMessage{v}, true
		}
		return nil, false
	}

	out := map[string]json.RawMessage{}
	for k, v := range raw {
		out[k] = v
	}

	if v, ok := raw["edits"]; ok {
		if arr, fixed := fixEdits(v); fixed {
			b, err := json.Marshal(arr)
			if err != nil {
				return args, nil
			}
			out["edits"] = b
		}
	} else if v, ok := raw["oldText"]; ok && len(v) > 0 {
		var oldText, newText string
		_ = json.Unmarshal(v, &oldText)
		_ = json.Unmarshal(raw["newText"], &newText)
		edit := map[string]string{"oldText": oldText, "newText": newText}
		b, err := json.Marshal([]map[string]string{edit})
		if err != nil {
			return args, nil
		}
		out["edits"] = b
		delete(out, "oldText")
		delete(out, "newText")
	}

	b, err := json.Marshal(out)
	if err != nil {
		return args, nil
	}
	return b, nil
}

func parseEditArgs(args json.RawMessage) (*editArgs, error) {
	var raw editArgsRaw
	if err := json.Unmarshal(args, &raw); err != nil {
		return nil, fmt.Errorf("Edit tool input is invalid. %v", err)
	}
	var list []struct {
		OldText string `json:"oldText"`
		NewText string `json:"newText"`
	}
	if err := json.Unmarshal(raw.Edits, &list); err != nil {
		return nil, fmt.Errorf("Edit tool input is invalid. %v", err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("edit tool input is invalid: edits must contain at least one replacement")
	}
	edits := make([]Edit, len(list))
	for i, e := range list {
		edits[i] = Edit{OldText: e.OldText, NewText: e.NewText}
	}
	return &editArgs{Path: raw.Path, Edits: edits}, nil
}

// Execute applies all edits against the original content and writes the result,
// preserving the file's BOM and dominant line ending.
func (t *EditTool) Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
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
	absolutePath := resolveToCwd(in.Path, t.cwd)

	rawContent, err := os.ReadFile(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("could not edit file: %s, error code: %v", in.Path, permNotFound(err))
	}
	bom, content := SplitBom(string(rawContent))
	originalEnding := DetectLineEnding(content)
	normalizedContent := NormalizeToLF(content)

	applied, err := ApplyEditsToNormalizedContent(normalizedContent, in.Edits, in.Path)
	if err != nil {
		return nil, err
	}

	finalContent := bom + RestoreLineEndings(applied.newContent, originalEnding)
	if err := os.WriteFile(absolutePath, []byte(finalContent), 0o644); err != nil {
		return nil, err
	}

	diffResult := GenerateDiffString(applied.baseContent, applied.newContent, 4)
	patch := GenerateUnifiedPatch(in.Path, applied.baseContent, applied.newContent, 4)
	details := &EditToolDetails{
		Diff:             diffResult.diff,
		Patch:            patch,
		FirstChangedLine: diffResult.firstChangedLine,
	}
	text := fmt.Sprintf("Successfully replaced %d block(s) in %s.", len(in.Edits), in.Path)
	if details.FirstChangedLine < 0 {
		text = strings.TrimSpace(text + "\n(no textual change detected)")
	}
	return &types.ToolResult{
		Content: []types.AssistantContent{types.TextContent{Type: types.TypeText, Text: text}},
		Details: details,
	}, nil
}

func permNotFound(err error) error {
	var pe *os.PathError
	if asErr(err, &pe) {
		return pe.Err
	}
	return err
}

func asErr(err error, target **os.PathError) bool {
	pe, ok := err.(*os.PathError)
	if ok {
		*target = pe
	}
	return ok
}
