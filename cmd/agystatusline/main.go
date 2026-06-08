// agystatusline reads the JSON payload that the antigravity-cli (agy) pipes to
// a custom status line command via stdin, extracts the fields we care about,
// and prints a compact one-line status string to stdout:
//
//	state: <agent_state> | input_tokens: <total_input_tokens> | max: <context_window_size> | remaining: <remaining>% | tasks: <N> | subagents: <N> [| <model_name>]
//
// The remaining-percentage segment is coloured green (≥ 80 %), yellow (≥ 50 %),
// or red (< 50 %) using ANSI escape codes so the value stands out in the
// status bar.
//
// Usage – settings.json:
//
//	{
//	  "statusLine": {
//	    "type":    "command",
//	    "command": "/path/to/agystatusline"
//	  }
//	}
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// payload is the subset of the JSON document we care about.
type payload struct {
	SessionID       string           `json:"session_id"`
	AgentState      string           `json:"agent_state"`
	ContextWindow   contextWindow    `json:"context_window"`
	BackgroundTasks []backgroundTask `json:"background_tasks"`
	Subagents       []subagent       `json:"subagents"`
	Model           modelInfo        `json:"model"`
}

type modelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type contextWindow struct {
	TotalInputTokens    int     `json:"total_input_tokens"`
	ContextWindowSize   int     `json:"context_window_size"`
	RemainingPercentage float64 `json:"remaining_percentage"`
}

type backgroundTask struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Index  int    `json:"index"`
}

type subagent struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

// ANSI colour helpers.
const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiGreen  = "\033[32m"
)

func remainingColor(pct float64) string {
	switch {
	case pct >= 80:
		return ansiGreen
	case pct >= 50:
		return ansiYellow
	default:
		return ansiRed
	}
}

func run(data []byte) (string, payload, error) {
	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return "", p, fmt.Errorf("parsing JSON: %w", err)
	}

	color := remainingColor(p.ContextWindow.RemainingPercentage)

	modelName := p.Model.DisplayName
	if modelName == "" {
		modelName = p.Model.ID
	}

	stateUpper := strings.ToUpper(p.AgentState)

	res := fmt.Sprintf("%s | %d/%d (%s%.0f%%%s)",
		stateUpper,
		p.ContextWindow.TotalInputTokens,
		p.ContextWindow.ContextWindowSize,
		color,
		p.ContextWindow.RemainingPercentage,
		ansiReset,
	)
	if modelName != "" {
		res += fmt.Sprintf(" | %s", modelName)
	}
	return res, p, nil
}

func main() {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agystatusline: reading stdin: %v\n", err)
		os.Exit(1)
	}

	line, _, err := run(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agystatusline: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(line)

	if sessionID := os.Getenv("AW_SESSION_ID"); sessionID != "" {
		if err := os.MkdirAll("/tmp/agystatusline", 0755); err != nil {
			fmt.Fprintf(os.Stderr, "agystatusline: creating directory: %v\n", err)
		} else {
			filePath := filepath.Join("/tmp/agystatusline", sessionID+".json")
			if err := os.WriteFile(filePath, data, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "agystatusline: writing statusline JSON: %v\n", err)
			}
		}
	}
}
