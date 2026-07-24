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
		Type      string `json:"type"`
		Text      string `json:"text"`
		Reason    string `json:"reason"`
		MessageID string `json:"messageID"`
		Tool      string `json:"tool"`
		State     struct {
			Status string         `json:"status"`
			Input  map[string]any `json:"input"`
			Output string         `json:"output"`
		} `json:"state"`
		Tokens struct {
			Total int `json:"total"`
			Input int `json:"input"`
		} `json:"tokens"`
		// Legacy / alternative tool-call fields
		ToolName  string         `json:"toolName"`
		ToolInput map[string]any `json:"input"`
	} `json:"part"`
}

// classifyLine maps an opencode output line to an entry type.
func classifyLine(opl *opencodeLine) string {
	switch opl.Type {
	case "tool_use", "tool_result", "tool":
		return "tool_call"
	case "text":
		return "agent_response"
	default:
		if opl.Part.Type == "tool" || opl.Part.Reason == "tool_use" || opl.Part.Tool != "" || opl.Part.ToolName != "" {
			return "tool_call"
		}
		return "other"
	}
}

// Prompt sends a prompt to opencode and parses its JSONL output in real-time.
// If opts.ReportCallback is set, it is called for each meaningful output line.
func Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	argv := []string{"run", "--format", "json", "--auto"}
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
	var lastToolOutput string
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

		// Classify line and track tool/text updates.
		entryType := classifyLine(&opl)
		if entryType != "other" {
			content := opl.Part.Text
			toolName := opl.Part.ToolName
			if toolName == "" {
				toolName = opl.Part.Tool
			}
			if entryType == "tool_call" {
				if content == "" {
					content = opl.Part.State.Output
				}
				if content == "" && len(opl.Part.State.Input) > 0 {
					if inputBytes, err := json.Marshal(opl.Part.State.Input); err == nil {
						content = string(inputBytes)
					}
				}
				if content == "" && len(opl.Part.ToolInput) > 0 {
					if inputBytes, err := json.Marshal(opl.Part.ToolInput); err == nil {
						content = string(inputBytes)
					}
				}
				if content == "" {
					content = fmt.Sprintf("Executing tool %s", toolName)
				}
				lastToolOutput = content
			}

			if content != "" {
				if opts.ReportCallback != nil {
					var metadata map[string]any
					if toolName != "" {
						metadata = map[string]any{"tool_name": toolName}
					}
					opts.ReportCallback(stepIndex, "MODEL", entryType, content, metadata)
				}
				stepIndex++
			}
		}
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
	if lastContent == "" && len(textMap) > 0 {
		// Fallback to the text of any completed/recorded message
		for _, builder := range textMap {
			if bStr := builder.String(); bStr != "" {
				lastContent = bStr
				break
			}
		}
	}
	if lastContent == "" && lastToolOutput != "" {
		lastContent = lastToolOutput
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
