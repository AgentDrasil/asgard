package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

const findSchemaJSON = `{
  "type": "object",
  "properties": {
    "pattern": { "type": "string", "description": "Glob pattern to match files, e.g. '*.ts', '**/*.json', or 'src/**/*.spec.ts'" },
    "path": { "type": "string", "description": "Directory to search in (default: current directory)" },
    "limit": { "type": "number", "description": "Maximum number of results (default: 1000)" }
  },
  "required": ["pattern"],
  "additionalProperties": false
}`

const findDefaultLimit = 1000

// FindToolDetails carries truncation metadata.
type FindToolDetails struct {
	Truncation         *TruncationResult `json:"truncation,omitempty"`
	ResultLimitReached int               `json:"resultLimitReached,omitempty"`
}

// FindTool searches for files by glob pattern, gitignore-aware. Like grep, it
// uses a built-in gitignore-aware walker instead of shelling out to the
// fd binary.
type FindTool struct {
	cwd string
}

// NewFindTool creates a find tool rooted at cwd.
func NewFindTool(cwd string) *FindTool {
	return &FindTool{cwd: cwd}
}

func (t *FindTool) Name() string  { return "find" }
func (t *FindTool) Label() string { return "find" }
func (t *FindTool) Parameters() json.RawMessage {
	return json.RawMessage(findSchemaJSON)
}
func (t *FindTool) PromptSnippet() string { return "Find files by name using glob patterns" }
func (t *FindTool) PromptGuidelines() []string {
	return []string{
		"When combining find and read, pass the find result path as the read path argument instead of repeating the original directory",
	}
}
func (t *FindTool) ExecutionMode() types.ToolExecutionMode { return "" }

func (t *FindTool) Description() string {
	return fmt.Sprintf("Search for files by glob pattern. Returns matching file paths relative to the search directory. Respects .gitignore. Output is truncated to %d results or %dKB (whichever is hit first).", findDefaultLimit, DefaultMaxBytes/1024)
}

type findArgs struct {
	Pattern string   `json:"pattern"`
	Path    string   `json:"path"`
	Limit   *float64 `json:"limit"`
}

// Execute walks the search dir and returns relative paths matching the glob.
func (t *FindTool) Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("operation aborted")
	}
	var in findArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}

	searchPath := resolveToCwd(orDefault(in.Path, "."), t.cwd)
	if info, err := os.Stat(searchPath); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("path not found or not a directory: %s", searchPath)
	}
	effectiveLimit := findDefaultLimit
	if in.Limit != nil && *in.Limit >= 1 {
		effectiveLimit = int(*in.Limit)
	}

	var results []string
	resultLimitReached := false
	err := walkWithGitignore(searchPath, func(absPath, rel string, isDir bool) error {
		ok, gerr := matchGlob(in.Pattern, rel, false)
		if gerr != nil || !ok {
			return nil
		}
		results = append(results, rel)
		if len(results) >= effectiveLimit {
			resultLimitReached = true
			return errStopWalk
		}
		return nil
	})
	_ = err

	if ctx.Err() != nil {
		return nil, fmt.Errorf("operation aborted")
	}

	if len(results) == 0 {
		return &types.ToolResult{
			Content: []types.AssistantContent{types.TextContent{Type: types.TypeText, Text: "No files found matching pattern"}},
		}, nil
	}

	rawOutput := strings.Join(results, "\n")
	truncation := TruncateHead(rawOutput, TruncationOptions{MaxLines: maxInt32})
	output := truncation.Content
	details := &FindToolDetails{}
	var notices []string
	if resultLimitReached {
		notices = append(notices, fmt.Sprintf("%d results limit reached. Use limit=%d for more", effectiveLimit, effectiveLimit*2))
		details.ResultLimitReached = effectiveLimit
	}
	if truncation.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", FormatSize(DefaultMaxBytes)))
		details.Truncation = &truncation
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return &types.ToolResult{
		Content: []types.AssistantContent{types.TextContent{Type: types.TypeText, Text: output}},
		Details: details,
	}, nil
}
