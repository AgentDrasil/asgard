package agy

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

// ── stream-json event types ────────────────────────────────────────────────
//
// These types map 1:1 to the NDJSON events emitted by
// `agy --output-format stream-json`.

// streamEvent is the top-level wrapper for every NDJSON line.
type streamEvent struct {
	Event          string            `json:"event"`
	ConversationID string            `json:"conversation_id"`
	Init           *streamInit       `json:"init,omitempty"`
	StepUpdate     *streamStepUpdate `json:"step_update,omitempty"`
	Result         *streamResult     `json:"result,omitempty"`
}

type streamInit struct {
	CWD            string   `json:"cwd"`
	Tools          []string `json:"tools"`
	PermissionMode string   `json:"permission_mode"`
}

type streamStepUpdate struct {
	ConversationID  string          `json:"conversation_id"`
	StepIndex       int             `json:"step_index"`
	State           string          `json:"state"`
	StepType        string          `json:"step_type"`
	ToolName        string          `json:"tool_name,omitempty"`
	ToolInfo        *streamToolInfo `json:"tool_info,omitempty"`
	TextDelta       string          `json:"text_delta,omitempty"`
	DurationSeconds float64         `json:"duration_seconds,omitempty"`
	Usage           *streamUsage    `json:"usage,omitempty"`
}

type streamToolInfo struct {
	Name       string           `json:"name"`
	Parameters map[string]any   `json:"parameters,omitempty"`
	Output     string           `json:"output,omitempty"`
	Error      *streamToolError `json:"error,omitempty"`
}

type streamToolError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type streamUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

type streamResult struct {
	ConversationID  string       `json:"conversation_id"`
	Status          string       `json:"status"`
	Response        string       `json:"response"`
	DurationSeconds float64      `json:"duration_seconds"`
	NumTurns        int          `json:"num_turns"`
	Usage           *streamUsage `json:"usage,omitempty"`
}

// ── parseStream ────────────────────────────────────────────────────────────

// parseStream reads NDJSON lines from r, processes each stream-json event,
// fires cb for tool and agent_response steps, and returns the final session ID,
// last content (= result.response), and total input tokens from the result event.
//
// Event handling:
//   - "init"        → captures conversation_id as sessionID
//   - "step_update" (tool ACTIVE)          → cb("TOOL", "tool_call",   "name({...params...})")
//   - "step_update" (tool DONE/ERROR)      → cb("TOOL", "tool_result", "name → output")
//   - "step_update" (agent_response DONE)  → cb("MODEL","agent_response", accumulated text_delta)
//   - "result"      → captures response + usage
func parseStream(r io.Reader, cb types.ReportFunc) (sessionID, lastContent string, inputTokens int) {
	// textByStep accumulates text_delta across ACTIVE→DONE events for the same
	// step_index so we deliver a single callback with the full response text.
	textByStep := make(map[int]string)

	scanner := bufio.NewScanner(r)
	buf := make([]byte, 4*1024*1024)
	scanner.Buffer(buf, len(buf))

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var ev streamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			log.Warn().Err(err).Str("line", line).Msg("agy/stream: failed to parse event")
			continue
		}

		switch ev.Event {
		case "init":
			sessionID = ev.ConversationID
			log.Debug().Str("session_id", sessionID).Msg("agy/stream: session started")

		case "step_update":
			if ev.StepUpdate == nil {
				continue
			}
			su := ev.StepUpdate

			// Always accumulate text_delta regardless of cb so lastContent
			// is correct even when no callback is registered.
			if su.StepType == "agent_response" && su.TextDelta != "" {
				textByStep[su.StepIndex] += su.TextDelta
			}

			if cb == nil {
				continue
			}

			switch {
			case su.StepType == "tool" && su.State == "ACTIVE" && su.ToolName != "":
				metadata := map[string]any{"tool_name": su.ToolName}
				content := formatToolCall(su.ToolName, su.ToolInfo)
				if su.ToolInfo != nil && len(su.ToolInfo.Parameters) > 0 {
					metadata["parameters"] = su.ToolInfo.Parameters
				}
				cb(su.StepIndex, "TOOL", "tool_call", content, metadata)

			case su.StepType == "tool" && su.ToolInfo != nil:
				metadata := map[string]any{"tool_name": su.ToolName}
				callStr := formatToolCall(su.ToolName, su.ToolInfo)
				output := su.ToolInfo.Output
				if su.ToolInfo.Error != nil {
					output = su.ToolInfo.Error.Message
					metadata["error_type"] = su.ToolInfo.Error.Type
				}
				var content string
				if output != "" {
					content = callStr + "\n\n" + output
				} else {
					content = callStr
				}
				cb(su.StepIndex, "TOOL", "tool_result", content, metadata)

			case su.StepType == "agent_response" && su.State == "DONE":
				// Response step complete — deliver the full accumulated text.
				full := textByStep[su.StepIndex]
				delete(textByStep, su.StepIndex)
				if full != "" {
					cb(su.StepIndex, "MODEL", "agent_response", full, nil)
				}
			}

		case "result":
			if ev.Result == nil {
				continue
			}
			res := ev.Result
			if res.ConversationID != "" {
				sessionID = res.ConversationID
			}
			lastContent = res.Response
			if res.Usage != nil {
				inputTokens = res.Usage.InputTokens
			}
			log.Debug().
				Str("status", res.Status).
				Str("session_id", sessionID).
				Int("num_turns", res.NumTurns).
				Msg("agy/stream: result received")
		}
	}

	if err := scanner.Err(); err != nil {
		log.Warn().Err(err).Msg("agy/stream: scanner error")
	}
	return
}

// formatToolCall extracts the primary command or path parameter from ToolInfo
// and formats it as a blockquote string (e.g. "> go version" or "> /path/to/file").
func formatToolCall(toolName string, info *streamToolInfo) string {
	if info == nil || len(info.Parameters) == 0 {
		return "> " + toolName
	}
	if cmd, ok := info.Parameters["CommandLine"].(string); ok && cmd != "" {
		return "> " + cmd
	}
	if path, ok := info.Parameters["AbsolutePath"].(string); ok && path != "" {
		return "> " + path
	}
	if dir, ok := info.Parameters["DirectoryPath"].(string); ok && dir != "" {
		return "> " + dir
	}
	if query, ok := info.Parameters["Query"].(string); ok && query != "" {
		return "> " + query
	}
	if paramJSON, err := json.Marshal(info.Parameters); err == nil {
		return "> " + toolName + "(" + string(paramJSON) + ")"
	}
	return "> " + toolName
}
