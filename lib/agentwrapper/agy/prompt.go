package agy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

// ── stream-json event types ────────────────────────────────────────────────

// streamEvent is the top-level wrapper for every NDJSON line emitted by
// `agy --output-format stream-json`.
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

// ── Prompt ─────────────────────────────────────────────────────────────────

// Prompt runs `agy --dangerously-skip-permissions --output-format stream-json
// -p <prompt>` and streams NDJSON events until the process exits.
//
// Compared to the old PTY-based approach, this requires no terminal emulation,
// no statusline polling, and no transcript file tailing. The agy process
// manages its own I/O and exits cleanly when done.
//
// Session resumption is supported via --conversation=<sessionID>.
// Model selection is supported via --model <model>.
func Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	runDir := opts.Dir
	if runDir == "" {
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting current working directory: %w", err)
		}
	}
	if err := ensureWorkspaceTrusted(runDir); err != nil {
		return nil, fmt.Errorf("ensuring workspace is trusted: %w", err)
	}

	argv := []string{"agy", "--dangerously-skip-permissions", "--output-format", "stream-json", "--add-dir", runDir}
	if opts.SessionID != "" {
		argv = append(argv, "--conversation="+opts.SessionID)
	}
	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	argv = append(argv, "-p", prompt)

	log.Debug().Interface("argv", argv).Msg("agy/prompt: starting")

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = runDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agy: %w", err)
	}

	// textByStep accumulates text_delta across ACTIVE→DONE events for the same
	// step_index so we can deliver a single ReportCallback per response step.
	textByStep := make(map[int]string)

	var sessionID string
	var lastContent string
	var inputTokens int

	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 4*1024*1024)
	scanner.Buffer(buf, len(buf))

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var ev streamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			log.Warn().Err(err).Str("line", line).Msg("agy/prompt: failed to parse stream event")
			continue
		}

		switch ev.Event {
		case "init":
			sessionID = ev.ConversationID
			log.Debug().Str("session_id", sessionID).Msg("agy/prompt: session started")

		case "step_update":
			if ev.StepUpdate == nil {
				continue
			}
			su := ev.StepUpdate

			// Accumulate text_delta for agent_response steps so we can fire a
			// single callback with the complete text when the step is DONE.
			if su.StepType == "agent_response" && su.TextDelta != "" {
				textByStep[su.StepIndex] += su.TextDelta
			}

			if opts.ReportCallback == nil {
				continue
			}

			switch {
			case su.StepType == "tool" && su.State == "ACTIVE" && su.ToolName != "":
				// Tool invocation started — report name + parameters.
				metadata := map[string]any{"tool_name": su.ToolName}
				if su.ToolInfo != nil && len(su.ToolInfo.Parameters) > 0 {
					metadata["parameters"] = su.ToolInfo.Parameters
				}
				opts.ReportCallback(su.StepIndex, "TOOL", "tool_call", su.ToolName, metadata)

			case su.StepType == "tool" && su.State == "DONE" && su.ToolInfo != nil:
				// Tool completed — report output or error.
				metadata := map[string]any{"tool_name": su.ToolName}
				content := su.ToolInfo.Output
				if su.ToolInfo.Error != nil {
					content = su.ToolInfo.Error.Message
					metadata["error_type"] = su.ToolInfo.Error.Type
				}
				opts.ReportCallback(su.StepIndex, "TOOL", "tool_result", content, metadata)

			case su.StepType == "agent_response" && su.State == "DONE":
				// Agent response step complete — deliver the full accumulated text.
				full := textByStep[su.StepIndex]
				delete(textByStep, su.StepIndex)
				if full != "" {
					opts.ReportCallback(su.StepIndex, "MODEL", "agent_response", full, nil)
				}
			}

		case "result":
			if ev.Result == nil {
				continue
			}
			r := ev.Result
			if r.ConversationID != "" {
				sessionID = r.ConversationID
			}
			lastContent = r.Response
			if r.Usage != nil {
				inputTokens = r.Usage.InputTokens
			}
			log.Debug().
				Str("status", r.Status).
				Str("session_id", sessionID).
				Int("num_turns", r.NumTurns).
				Msg("agy/prompt: result received")
		}
	}

	if err := scanner.Err(); err != nil {
		log.Warn().Err(err).Msg("agy/prompt: scanner error")
	}

	// Wait for the subprocess. A non-zero exit after successful output is
	// non-fatal — log and continue.
	if err := cmd.Wait(); err != nil {
		log.Warn().Err(err).Msg("agy/prompt: agy exited with error")
	}

	return &types.PromptResult{
		SessionID:   sessionID,
		InputTokens: inputTokens,
		LastContent: lastContent,
	}, nil
}

// ── workspace trust ────────────────────────────────────────────────────────

func ensureWorkspaceTrusted(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving absolute path for %q: %w", dir, err)
	}
	absDir = filepath.Clean(absDir)

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}
	settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("reading settings file %s: %w", settingsPath, err)
	}

	var config struct {
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parsing settings JSON: %w", err)
	}

	for _, ws := range config.TrustedWorkspaces {
		if filepath.Clean(ws) == absDir {
			return nil
		}
	}

	// Read settings as map to preserve other keys.
	var settingsMap map[string]any
	if err := json.Unmarshal(data, &settingsMap); err != nil {
		return fmt.Errorf("parsing settings JSON for update: %w", err)
	}

	var trustedWorkspaces []any
	if tw, ok := settingsMap["trustedWorkspaces"]; ok {
		if arr, ok := tw.([]any); ok {
			trustedWorkspaces = arr
		}
	}

	trustedWorkspaces = append(trustedWorkspaces, absDir)
	settingsMap["trustedWorkspaces"] = trustedWorkspaces

	newData, err := json.MarshalIndent(settingsMap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling updated settings: %w", err)
	}

	log.Info().Str("path", absDir).Msg("Adding directory to trusted workspaces in settings.json")
	if err := os.WriteFile(settingsPath, newData, 0644); err != nil {
		return fmt.Errorf("writing updated settings file: %w", err)
	}

	return nil
}
