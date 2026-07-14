package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

type opencodeLine struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		Text      string `json:"text"`
		Reason    string `json:"reason"`
		MessageID string `json:"messageID"`
		Tokens    struct {
			Total int `json:"total"`
			Input int `json:"input"`
		} `json:"tokens"`
		// Tool-call fields
		ToolName  string         `json:"toolName"`
		ToolInput map[string]any `json:"input"`
	} `json:"part"`
}

// classifyLine maps an opencode output line to an entry type.
func classifyLine(opl *opencodeLine) string {
	switch opl.Type {
	case "tool_use", "tool_result":
		return "tool_call"
	case "text":
		return "agent_response"
	default:
		if opl.Part.Reason == "tool_use" {
			return "tool_call"
		}
		return "other"
	}
}

// Prompt sends a prompt to opencode and parses its JSONL output in real-time.
// If opts.ReportCallback is set, it is called for each meaningful output line.
func Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	argv := []string{"run", "--format", "json", "--dangerously-skip-permissions"}
	if opts.SessionID != "" {
		argv = append(argv, "--session", opts.SessionID)
	}
	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	argv = append(argv, prompt)

	cmd := exec.CommandContext(ctx, "opencode", argv...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating opencode stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting opencode: %w", err)
	}

	var sessionID string
	var inputTokens int
	var totalTokens int
	var targetMessageID string
	stepIndex := 0

	// Map to accumulate text contents by messageID
	textMap := make(map[string]*strings.Builder)

	scanner := bufio.NewScanner(stdout)
	// Use a large buffer for potentially long lines.
	buf := make([]byte, 4*1024*1024)
	scanner.Buffer(buf, len(buf))

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		var opl opencodeLine
		if err := json.Unmarshal([]byte(trimmed), &opl); err != nil {
			continue
		}

		if opl.SessionID != "" {
			sessionID = opl.SessionID
		}

		if opl.Type == "text" && opl.Part.MessageID != "" {
			builder, exists := textMap[opl.Part.MessageID]
			if !exists {
				builder = &strings.Builder{}
				textMap[opl.Part.MessageID] = builder
			}
			builder.WriteString(opl.Part.Text)
		}

		if opl.Type == "step_finish" {
			if opl.Part.Tokens.Input > 0 {
				inputTokens = opl.Part.Tokens.Input
			}
			if opl.Part.Tokens.Total > 0 {
				totalTokens = opl.Part.Tokens.Total
			}
			if opl.Part.Reason == "stop" && opl.Part.MessageID != "" {
				targetMessageID = opl.Part.MessageID
			}
		}

		// Report incremental update if callback is set.
		if opts.ReportCallback != nil {
			entryType := classifyLine(&opl)
			if entryType != "other" {
				content := opl.Part.Text
				var metadata map[string]any
				if opl.Part.ToolName != "" {
					metadata = map[string]any{"tool_name": opl.Part.ToolName}
				}
				opts.ReportCallback(stepIndex, "MODEL", entryType, content, metadata)
			}
		}
		stepIndex++
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("running opencode prompt: %w", err)
	}

	var lastContent string
	if targetMessageID != "" {
		if builder, exists := textMap[targetMessageID]; exists {
			lastContent = builder.String()
		}
	}

	maxTokens := 1048576
	remaining := 1.0
	if maxTokens > 0 {
		remaining = 1.0 - (float64(totalTokens) / float64(maxTokens))
		if remaining < 0 {
			remaining = 0
		}
	}

	return &types.PromptResult{
		SessionID:   sessionID,
		InputTokens: inputTokens,
		MaxTokens:   maxTokens,
		Remaining:   remaining,
		LastContent: lastContent,
	}, nil
}
