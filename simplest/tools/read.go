package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/AgentDrasil/asgard/simplest/types"
)

const readSchemaJSON = `{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Path to the file to read (relative or absolute)" },
    "offset": { "type": "number", "description": "Line number to start reading from (1-indexed)" },
    "limit": { "type": "number", "description": "Maximum number of lines to read" }
  },
  "required": ["path"],
  "additionalProperties": false
}`

// ReadToolDetails carries truncation metadata for UI rendering.
type ReadToolDetails struct {
	Truncation *TruncationResult `json:"truncation,omitempty"`
}

// imageExts maps supported image extensions to MIME types.
var imageExts = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
}

// ReadTool reads file contents, with head truncation and image support.
// Images are returned as base64 ImageContent blocks
// without resizing.
type ReadTool struct {
	cwd string
}

// NewReadTool creates a read tool rooted at cwd.
func NewReadTool(cwd string) *ReadTool {
	return &ReadTool{cwd: cwd}
}

func (t *ReadTool) Name() string  { return "read" }
func (t *ReadTool) Label() string { return "read" }
func (t *ReadTool) Parameters() json.RawMessage {
	return json.RawMessage(readSchemaJSON)
}
func (t *ReadTool) PromptSnippet() string { return "Read file contents" }
func (t *ReadTool) PromptGuidelines() []string {
	return []string{"Use read to examine files instead of cat or sed."}
}
func (t *ReadTool) ExecutionMode() types.ToolExecutionMode { return "" }

func (t *ReadTool) Description() string {
	return fmt.Sprintf("Read the contents of a file. Supports text files and images (jpg, png, gif, webp, bmp). Images are sent as attachments. For text files, output is truncated to %d lines or %dKB (whichever is hit first). Use offset/limit for large files. When you need the full file, continue with offset until complete.", DefaultMaxLines, DefaultMaxBytes/1024)
}

type readArgs struct {
	Path   string   `json:"path"`
	Offset *float64 `json:"offset"`
	Limit  *float64 `json:"limit"`
}

// Execute reads a file and returns truncated text content or an image block.
func (t *ReadTool) Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("operation aborted")
	}
	var in readArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	absolutePath := resolveToCwd(in.Path, t.cwd)

	ext := strings.ToLower(filepath.Ext(absolutePath))
	if mime, ok := imageExts[ext]; ok {
		data, err := os.ReadFile(absolutePath)
		if err != nil {
			return nil, err
		}
		note := fmt.Sprintf("Read image file [%s]", mime)
		return &types.ToolResult{
			Content: []types.AssistantContent{
				types.TextContent{Type: types.TypeText, Text: note},
				types.ImageContent{Type: types.TypeImage, Data: base64.StdEncoding.EncodeToString(data), MimeType: mime},
			},
		}, nil
	}

	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return nil, err
	}
	textContent := string(data)
	allLines := strings.Split(textContent, "\n")
	totalFileLines := len(allLines)

	var offset, limit float64
	offsetSet := in.Offset != nil
	limitSet := in.Limit != nil
	if offsetSet {
		offset = *in.Offset
	}
	if limitSet {
		limit = *in.Limit
	}

	startLine := 0
	if offsetSet && offset > 0 {
		startLine = int(offset) - 1
	}
	startLineDisplay := startLine + 1
	if startLine >= len(allLines) {
		return nil, fmt.Errorf("offset %s is beyond end of file (%d lines total)", formatNumber(in.Offset), totalFileLines)
	}

	var selectedContent string
	var userLimitedLines int
	userLimited := false
	if limitSet {
		endLine := startLine + int(limit)
		if endLine > len(allLines) {
			endLine = len(allLines)
		}
		selectedContent = strings.Join(allLines[startLine:endLine], "\n")
		userLimitedLines = endLine - startLine
		userLimited = true
	} else {
		selectedContent = strings.Join(allLines[startLine:], "\n")
	}

	truncation := TruncateHead(selectedContent, TruncationOptions{})
	details := &ReadToolDetails{}
	var outputText string
	if truncation.FirstLineExceedsLimit {
		firstLineSize := FormatSize(len(allLines[startLine]))
		outputText = fmt.Sprintf("[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
			startLineDisplay, firstLineSize, FormatSize(DefaultMaxBytes), startLineDisplay, in.Path, DefaultMaxBytes)
		details.Truncation = &truncation
	} else if truncation.Truncated {
		endLineDisplay := startLineDisplay + truncation.OutputLines - 1
		nextOffset := endLineDisplay + 1
		outputText = truncation.Content
		if truncation.TruncatedBy == "lines" {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, nextOffset)
		} else {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, FormatSize(DefaultMaxBytes), nextOffset)
		}
		details.Truncation = &truncation
	} else if userLimited && startLine+userLimitedLines < totalFileLines {
		remaining := totalFileLines - (startLine + userLimitedLines)
		nextOffset := startLine + userLimitedLines + 1
		outputText = fmt.Sprintf("%s\n\n[%d more lines in file. Use offset=%d to continue.]",
			truncation.Content, remaining, nextOffset)
	} else {
		outputText = truncation.Content
	}

	result := &types.ToolResult{
		Content: []types.AssistantContent{types.TextContent{Type: types.TypeText, Text: outputText}},
	}
	if details.Truncation != nil {
		result.Details = details
	}
	return result, nil
}

func formatNumber(v *float64) string {
	if v == nil {
		return "undefined"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}
