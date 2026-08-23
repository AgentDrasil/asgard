package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AgentDrasil/asgard/simplest/types"
)

const lsSchemaJSON = `{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Directory to list (default: current directory)" },
    "limit": { "type": "number", "description": "Maximum number of entries to return (default: 500)" }
  },
  "additionalProperties": false
}`

const lsDefaultLimit = 500

// LsToolDetails carries truncation metadata.
type LsToolDetails struct {
	Truncation        *TruncationResult `json:"truncation,omitempty"`
	EntryLimitReached int               `json:"entryLimitReached,omitempty"`
}

// LsTool lists directory contents alphabetically with "/" suffix for dirs.
type LsTool struct {
	cwd string
}

// NewLsTool creates an ls tool rooted at cwd.
func NewLsTool(cwd string) *LsTool {
	return &LsTool{cwd: cwd}
}

func (t *LsTool) Name() string  { return "ls" }
func (t *LsTool) Label() string { return "ls" }
func (t *LsTool) Parameters() json.RawMessage {
	return json.RawMessage(lsSchemaJSON)
}
func (t *LsTool) PromptSnippet() string { return "List directory contents" }
func (t *LsTool) PromptGuidelines() []string {
	return []string{
		"When combining ls and grep, pass the ls result path as the grep path argument instead of repeating the original directory",
	}
}
func (t *LsTool) ExecutionMode() types.ToolExecutionMode { return "" }

func (t *LsTool) Description() string {
	return fmt.Sprintf("List directory contents. Returns entries sorted alphabetically, with '/' suffix for directories. Includes dotfiles. Output is truncated to %d entries or %dKB (whichever is hit first).", lsDefaultLimit, DefaultMaxBytes/1024)
}

type lsArgs struct {
	Path  string   `json:"path"`
	Limit *float64 `json:"limit"`
}

// Execute lists one directory level.
func (t *LsTool) Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("operation aborted")
	}
	var in lsArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}

	dirPath := resolveToCwd(orDefault(in.Path, "."), t.cwd)
	effectiveLimit := lsDefaultLimit
	if in.Limit != nil && *in.Limit >= 1 {
		effectiveLimit = int(*in.Limit)
	}

	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("path not found: %s", dirPath)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", dirPath)
	}

	dirEntries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read directory: %v", err)
	}

	sort.Slice(dirEntries, func(i, j int) bool {
		return strings.ToLower(dirEntries[i].Name()) < strings.ToLower(dirEntries[j].Name())
	})

	var results []string
	entryLimitReached := false
	for _, e := range dirEntries {
		if len(results) >= effectiveLimit {
			entryLimitReached = true
			break
		}
		fullPath := filepath.Join(dirPath, e.Name())
		suffix := ""
		if entryInfo, err := os.Stat(fullPath); err == nil {
			if entryInfo.IsDir() {
				suffix = "/"
			}
		} else {
			continue
		}
		results = append(results, e.Name()+suffix)
	}

	if len(results) == 0 {
		return &types.ToolResult{
			Content: []types.AssistantContent{types.TextContent{Type: types.TypeText, Text: "(empty directory)"}},
		}, nil
	}

	rawOutput := strings.Join(results, "\n")
	truncation := TruncateHead(rawOutput, TruncationOptions{MaxLines: maxInt32})
	output := truncation.Content
	details := &LsToolDetails{}
	var notices []string
	if entryLimitReached {
		notices = append(notices, fmt.Sprintf("%d entries limit reached. Use limit=%d for more", effectiveLimit, effectiveLimit*2))
		details.EntryLimitReached = effectiveLimit
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
