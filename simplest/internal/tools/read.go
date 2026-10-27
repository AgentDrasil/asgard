package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/AgentDrasil/asgard/simplest/internal/images"
	"github.com/AgentDrasil/asgard/simplest/internal/types"
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

// ReadTool reads file contents, with head truncation and image support.
// Images are returned as base64 ImageContent blocks, resized and bounded
// according to model limits.
type ReadTool struct {
	cwd   string
	model *types.Model
}

// NewReadTool creates a read tool rooted at cwd.
func NewReadTool(cwd string) *ReadTool {
	return &ReadTool{cwd: cwd}
}

// SetModel configures the active model for vision capability checks and image input limits.
func (t *ReadTool) SetModel(m *types.Model) {
	t.model = m
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

	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return nil, err
	}

	// Port of pi 005af57d8: detect images by content, not file extension, and
	// degrade gracefully: unrecognized content falls through to the text path.
	mime := images.DetectMimeType(data)
	if mime != "" {
		if t.model != nil && !t.model.SupportsImage() {
			modelID := t.model.ID
			if modelID == "" {
				modelID = t.model.WireID()
			}
			msg := fmt.Sprintf("[image omitted: model %s does not support images]", modelID)
			return &types.ToolResult{
				Content: []types.AssistantContent{
					types.TextContent{Type: types.TypeText, Text: msg},
				},
			}, nil
		}

		limits := t.model.GetImageLimits()
		processed, err := images.ProcessImage(data, mime, limits)
		if err != nil {
			// Port of pi 005af57d8: graceful fallback — a decode failure
			// yields a text result describing the error, not a tool error.
			note := fmt.Sprintf("Read image file [%s]\n%s", mime, err.Error())
			return &types.ToolResult{
				Content: []types.AssistantContent{
					types.TextContent{Type: types.TypeText, Text: note},
				},
			}, nil
		}

		note := fmt.Sprintf("Read image file [%s]", processed.MimeType)
		for _, hint := range processed.Notes {
			note += "\n" + hint
		}

		return &types.ToolResult{
			Content: []types.AssistantContent{
				types.TextContent{Type: types.TypeText, Text: note},
				types.ImageContent{Type: types.TypeImage, Data: processed.Data, MimeType: processed.MimeType},
			},
		}, nil
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

	// Guard float->int overflow and negative slices; JS Array.slice degrades gracefully (pi 005af57d8).
	startLine := 0
	if offsetSet && offset > 0 {
		// Saturating conversion: 1e300 overflows int and would wrap negative,
		// letting startLine pass the bounds check before the slice panics.
		// Clamp like upstream Math.max(0, offset - 1) so fractional offsets
		// such as 0.5 never yield a negative startLine.
		offsetInt := saturatingFloatToInt(offset)
		if offsetInt > len(allLines) {
			// Too large to represent or past EOF: report "beyond end of file".
			startLine = len(allLines)
		} else {
			startLine = offsetInt - 1
			if startLine < 0 {
				startLine = 0
			}
		}
	}
	startLineDisplay := startLine + 1
	if startLine >= len(allLines) {
		return nil, fmt.Errorf("offset %s is beyond end of file (%d lines total)", formatNumber(in.Offset), totalFileLines)
	}

	var selectedContent string
	var userLimitedLines int
	userLimited := false
	if limitSet {
		endLine := startLine
		if limit > 0 {
			// Clamp to [startLine, len(allLines)]; JS slice ignores excess.
			if limitInt := saturatingFloatToInt(limit); limitInt < len(allLines)-startLine {
				endLine = startLine + limitInt
			} else {
				endLine = len(allLines)
			}
		}
		selectedContent = strings.Join(allLines[startLine:endLine], "\n")
		userLimitedLines = endLine - startLine
		userLimited = limit > 0
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
		outputText = truncation.Content
		if outputText != "" {
			outputText += "\n\n"
		}
		outputText += fmt.Sprintf("[%d more lines in file. Use offset=%d to continue.]",
			remaining, nextOffset)
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

// saturatingFloatToInt converts a float to int with arch-independent clamping
// to the range of int, instead of relying on the undefined (platform-specific)
// float->int conversion for out-of-range values. Genuine negative values are
// preserved; NaN maps to 0; ±Inf saturates to min/max int.
func saturatingFloatToInt(v float64) int {
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1
	if math.IsNaN(v) {
		return 0
	}
	if v >= float64(maxInt) {
		return maxInt
	}
	if v <= float64(minInt) {
		return minInt
	}
	return int(v)
}

func formatNumber(v *float64) string {
	if v == nil {
		return "undefined"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}
