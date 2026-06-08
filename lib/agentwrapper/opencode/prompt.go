package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/AgentDrasil/asgard/lib/agentwrapper"
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
	} `json:"part"`
}

// Prompt sends a prompt to opencode and parses its JSONL output.
func Prompt(ctx context.Context, prompt string, opts agentwrapper.PromptOptions) (*agentwrapper.PromptResult, error) {
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

	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running opencode prompt: %w", err)
	}

	var sessionID string
	var inputTokens int
	var totalTokens int
	var targetMessageID string

	// Map to accumulate text contents by messageID
	textMap := make(map[string]*strings.Builder)

	lines := strings.Split(out.String(), "\n")
	for _, line := range lines {
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

	return &agentwrapper.PromptResult{
		SessionID:   sessionID,
		InputTokens: inputTokens,
		MaxTokens:   maxTokens,
		Remaining:   remaining,
		LastContent: lastContent,
	}, nil
}
