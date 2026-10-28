package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// streamLine is one line of `claude --output-format stream-json --verbose`.
type streamLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Message   struct {
		Model   string `json:"model"`
		Content []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Text      string          `json:"text"`
			Name      string          `json:"name"`
			Input     map[string]any  `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
		} `json:"content"`
		Usage struct {
			InputTokens         int `json:"input_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	// result line
	IsError bool     `json:"is_error"`
	Result  string   `json:"result"`
	Errors  []string `json:"errors"`
}

// maxLineBytes bounds a single stream line held in memory. Longer lines (e.g.
// a tool_result carrying a huge file) are skipped, never left unread: an
// unread pipe would block claude forever.
const maxLineBytes = 4 * 1024 * 1024

// lineReader yields newline-delimited lines, discarding any line longer than
// max while still consuming it from the underlying reader.
type lineReader struct {
	r       *bufio.Reader
	max     int
	skipped int // number of oversized lines discarded so far
}

func newLineReader(r io.Reader, max int) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(r, 64*1024), max: max}
}

// next returns the next line that fits in max; oversized lines yield "" so
// callers skip them. io.EOF is returned once the stream is exhausted.
func (lr *lineReader) next() (string, error) {
	var buf []byte
	oversized := false
	for {
		chunk, isPrefix, err := lr.r.ReadLine()
		if err != nil {
			return "", err
		}
		if !oversized {
			if len(buf)+len(chunk) > lr.max {
				oversized, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if !isPrefix {
			if oversized {
				lr.skipped++
				return "", nil
			}
			return string(buf), nil
		}
	}
}

// streamOutcome is what the stream yielded once fully consumed.
type streamOutcome struct {
	sessionID   string
	inputTokens int
	lastContent string
	errMessage  string
	sawResult   bool
}

// fileModifyingTools are claude's built-in tools that create or modify files;
// all carry the target in "file_path" (notebook_path for NotebookEdit).
var fileModifyingTools = map[string][]string{
	"Edit":         {"file_path"},
	"Write":        {"file_path"},
	"MultiEdit":    {"file_path"},
	"NotebookEdit": {"notebook_path", "file_path"},
}

func extractTargetFiles(tool string, input map[string]any) []string {
	for _, key := range fileModifyingTools[tool] {
		if v, ok := input[key].(string); ok && strings.TrimSpace(v) != "" {
			return []string{types.RemapSandboxPath(v)}
		}
	}
	return nil
}

// toolResultText flattens a tool_result content, which is either a string or
// an array of {type:"text", text:...} blocks.
func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// consumeStream reads stream-json from r, forwarding meaningful entries to
// report (may be nil), and returns the aggregate outcome.
func consumeStream(r io.Reader, maxTokens int, report types.ReportFunc) (*streamOutcome, error) {
	out := &streamOutcome{}
	toolNames := map[string]string{} // tool_use id -> tool name
	stepIndex := 0
	var lastText string

	emit := func(entryType, content string, meta map[string]any) {
		if content == "" {
			return
		}
		if report != nil {
			meta["max_tokens"] = maxTokens
			meta["is_append"] = false
			if out.inputTokens > 0 {
				meta["input_tokens"] = out.inputTokens
				meta["total_input_tokens"] = out.inputTokens
			}
			report(stepIndex, "MODEL", entryType, content, meta)
		}
		stepIndex++
	}

	lines := newLineReader(r, maxLineBytes)

	for {
		line, err := lines.next()
		if err != nil {
			if err != io.EOF {
				return nil, fmt.Errorf("reading claude output: %w", err)
			}
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var l streamLine
		if err := json.Unmarshal([]byte(trimmed), &l); err != nil {
			continue
		}
		if l.SessionID != "" {
			out.sessionID = l.SessionID
		}

		switch l.Type {
		case "assistant":
			// Skip claude's synthetic messages (e.g. slash-command output).
			if u := l.Message.Usage; l.Message.Model != "<synthetic>" && u.InputTokens+u.CacheReadTokens+u.CacheCreationTokens > 0 {
				out.inputTokens = u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
			}
			for _, c := range l.Message.Content {
				switch c.Type {
				case "text":
					lastText = c.Text
					emit("agent_response", c.Text, map[string]any{})
				case "tool_use":
					toolNames[c.ID] = c.Name
					content := fmt.Sprintf("Executing tool %s", c.Name)
					if len(c.Input) > 0 {
						if b, err := json.Marshal(c.Input); err == nil {
							content = string(b)
						}
					}
					meta := map[string]any{"tool_name": c.Name}
					if tfs := extractTargetFiles(c.Name, c.Input); len(tfs) > 0 {
						meta["target_files"] = tfs
					}
					emit("tool_call", content, meta)
				}
			}
		case "user":
			for _, c := range l.Message.Content {
				if c.Type != "tool_result" {
					continue
				}
				meta := map[string]any{"tool_name": toolNames[c.ToolUseID]}
				if c.IsError {
					meta["is_error"] = true
				}
				emit("tool_call", toolResultText(c.Content), meta)
			}
		case "result":
			// A run can emit several result lines (e.g. async subagents add
			// interim and task-notification ones). Empty non-error results
			// carry nothing, and the last meaningful one decides the outcome.
			// A non-success subtype is an error whatever is_error says.
			out.sawResult = true
			failed := l.IsError || (l.Subtype != "" && l.Subtype != "success")
			msg := l.Result
			if msg == "" {
				msg = strings.Join(l.Errors, "; ")
			}
			if failed {
				if msg == "" {
					msg = l.Subtype
				}
				out.errMessage = msg
			} else if l.Result != "" {
				out.errMessage = ""
				out.lastContent = l.Result
			}
		}
	}
	if lines.skipped > 0 && !out.sawResult && out.errMessage == "" {
		// The result line itself may have been the oversized one; do not
		// pass the last assistant text off as a successful outcome.
		out.errMessage = "claude result line exceeded the size limit"
	}
	if out.lastContent == "" {
		out.lastContent = lastText
	}
	return out, nil
}

// extractResultText returns the text of the final `result` line (or the last
// assistant text block) from a complete stream, used for slash commands.
func extractResultText(data []byte) (string, error) {
	out, err := consumeStream(strings.NewReader(string(data)), 0, nil)
	if err != nil {
		return "", err
	}
	if out.errMessage != "" {
		return "", fmt.Errorf("claude: %s", out.errMessage)
	}
	return out.lastContent, nil
}
