package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

const grepSchemaJSON = `{
  "type": "object",
  "properties": {
    "pattern": { "type": "string", "description": "Search pattern (regex or literal string)" },
    "path": { "type": "string", "description": "Directory or file to search (default: current directory)" },
    "glob": { "type": "string", "description": "Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'" },
    "ignoreCase": { "type": "boolean", "description": "Case-insensitive search (default: false)" },
    "literal": { "type": "boolean", "description": "Treat pattern as literal string instead of regex (default: false)" },
    "context": { "type": "number", "description": "Number of lines to show before and after each match (default: 0)" },
    "limit": { "type": "number", "description": "Maximum number of matches to return (default: 100)" }
  },
  "required": ["pattern"],
  "additionalProperties": false
}`

// GrepToolDetails carries truncation metadata.
type GrepToolDetails struct {
	Truncation        *TruncationResult `json:"truncation,omitempty"`
	MatchLimitReached int               `json:"matchLimitReached,omitempty"`
	LinesTruncated    bool              `json:"linesTruncated,omitempty"`
}

const grepDefaultLimit = 100

// GrepTool searches file contents with regex or literal patterns using a
// built-in gitignore-aware walker.
type GrepTool struct {
	cwd string
}

// NewGrepTool creates a grep tool rooted at cwd.
func NewGrepTool(cwd string) *GrepTool {
	return &GrepTool{cwd: cwd}
}

func (t *GrepTool) Name() string  { return "grep" }
func (t *GrepTool) Label() string { return "grep" }
func (t *GrepTool) Parameters() json.RawMessage {
	return json.RawMessage(grepSchemaJSON)
}
func (t *GrepTool) PromptSnippet() string {
	return "Search file contents for patterns (respects .gitignore)"
}
func (t *GrepTool) PromptGuidelines() []string {
	return []string{
		"Use glob to narrow searches by file type, and use find instead when looking for files by name",
	}
}
func (t *GrepTool) ExecutionMode() types.ToolExecutionMode { return "" }

func (t *GrepTool) Description() string {
	return fmt.Sprintf("Search file contents for a pattern. Returns matching lines with file paths and line numbers. Respects .gitignore. Output is truncated to %d matches or %dKB (whichever is hit first). Long lines are truncated to %d chars.", grepDefaultLimit, DefaultMaxBytes/1024, GrepMaxLineLength)
}

type grepArgs struct {
	Pattern    string   `json:"pattern"`
	Path       string   `json:"path"`
	Glob       string   `json:"glob"`
	IgnoreCase *bool    `json:"ignoreCase"`
	Literal    *bool    `json:"literal"`
	Context    *float64 `json:"context"`
	Limit      *float64 `json:"limit"`
}

// Execute walks the search path gitignore-aware, collecting up to limit
// matching lines formatted as path:line: text with optional context blocks.
func (t *GrepTool) Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("operation aborted")
	}
	var in grepArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}

	searchPath := resolveToCwd(orDefault(in.Path, "."), t.cwd)
	info, err := os.Stat(searchPath)
	if err != nil {
		return nil, fmt.Errorf("path not found: %s", searchPath)
	}
	isDirectory := info.IsDir()

	pattern := in.Pattern
	if in.Literal != nil && *in.Literal {
		pattern = regexp.QuoteMeta(pattern)
	}
	flags := ""
	if in.IgnoreCase != nil && *in.IgnoreCase {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return nil, err
	}

	contextValue := 0
	if in.Context != nil && *in.Context > 0 {
		contextValue = int(*in.Context)
	}
	effectiveLimit := grepDefaultLimit
	if in.Limit != nil && *in.Limit >= 1 {
		effectiveLimit = int(*in.Limit)
	}

	formatPath := func(filePath string) string {
		if isDirectory {
			rel, err := filepath.Rel(searchPath, filePath)
			if err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
				return filepath.ToSlash(rel)
			}
		}
		return filepath.Base(filePath)
	}

	type match struct {
		filePath string
		lineNum  int
	}
	var matches []match
	matchLimitReached := false

	root := searchPath
	if !isDirectory {
		root = filepath.Dir(searchPath)
	}

	fileMatches := func(absFile string, budget int) []int {
		if budget <= 0 {
			return nil
		}
		f, err := os.Open(absFile)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		var hitLines []int
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNum := 0
		for sc.Scan() {
			lineNum++
			if re.MatchString(sc.Text()) {
				hitLines = append(hitLines, lineNum)
				if len(hitLines) >= budget {
					return hitLines
				}
			}
		}
		if sc.Err() != nil {
			// Lines over the 1MB scanner cap abort the scan; skip such files
			// rather than reporting partial results as complete.
			return nil
		}
		return hitLines
	}

	collect := func(absPath, rel string, isDir bool) error {
		if isDir {
			return nil
		}
		if in.Glob != "" {
			ok, err := matchGlob(in.Glob, rel, false)
			if err != nil || !ok {
				return nil
			}
		}
		for _, ln := range fileMatches(absPath, effectiveLimit-len(matches)) {
			matches = append(matches, match{filePath: absPath, lineNum: ln})
			if len(matches) >= effectiveLimit {
				matchLimitReached = true
				return errStopWalk
			}
		}
		return nil
	}

	if isDirectory {
		_ = walkWithGitignore(root, collect)
	} else {
		_ = collect(searchPath, filepath.Base(searchPath), false)
	}

	if ctx.Err() != nil {
		return nil, fmt.Errorf("operation aborted")
	}

	if len(matches) == 0 {
		return &types.ToolResult{
			Content: []types.AssistantContent{types.TextContent{Type: types.TypeText, Text: "No matches found"}},
		}, nil
	}

	linesTruncated := false
	var outputLines []string
	fileCache := map[string][]string{}
	getFileLines := func(p string) []string {
		if lines, ok := fileCache[p]; ok {
			return lines
		}
		data, err := os.ReadFile(p)
		var lines []string
		if err == nil {
			content := strings.ReplaceAll(string(data), "\r\n", "\n")
			content = strings.ReplaceAll(content, "\r", "\n")
			lines = strings.Split(content, "\n")
		}
		fileCache[p] = lines
		return lines
	}

	for _, m := range matches {
		relPath := formatPath(m.filePath)
		lines := getFileLines(m.filePath)
		if len(lines) == 0 {
			outputLines = append(outputLines, fmt.Sprintf("%s:%d: (unable to read file)", relPath, m.lineNum))
			continue
		}
		start := m.lineNum
		end := m.lineNum
		if contextValue > 0 {
			start = maxInt(1, m.lineNum-contextValue)
			end = minInt(len(lines), m.lineNum+contextValue)
		}
		for current := start; current <= end; current++ {
			lineText := strings.ReplaceAll(lines[current-1], "\r", "")
			truncatedText, wasTruncated := truncateLine(lineText, GrepMaxLineLength)
			if wasTruncated {
				linesTruncated = true
			}
			if current == m.lineNum {
				outputLines = append(outputLines, fmt.Sprintf("%s:%d: %s", relPath, current, truncatedText))
			} else {
				outputLines = append(outputLines, fmt.Sprintf("%s-%d- %s", relPath, current, truncatedText))
			}
		}
	}

	rawOutput := strings.Join(outputLines, "\n")
	truncation := TruncateHead(rawOutput, TruncationOptions{MaxLines: maxInt32})
	output := truncation.Content
	details := &GrepToolDetails{}
	var notices []string
	if matchLimitReached {
		notices = append(notices, fmt.Sprintf("%d matches limit reached. Use limit=%d for more, or refine pattern", effectiveLimit, effectiveLimit*2))
		details.MatchLimitReached = effectiveLimit
	}
	if truncation.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", FormatSize(DefaultMaxBytes)))
		details.Truncation = &truncation
	}
	if linesTruncated {
		notices = append(notices, fmt.Sprintf("Some lines truncated to %d chars. Use read tool to see full lines", GrepMaxLineLength))
		details.LinesTruncated = true
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return &types.ToolResult{
		Content: []types.AssistantContent{types.TextContent{Type: types.TypeText, Text: output}},
		Details: details,
	}, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
