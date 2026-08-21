package agystatusline

import (
	"encoding/json"
	"fmt"
)

const (
	k = 1024
	m = k * k
)

// Payload is the subset of the JSON document we care about.
type Payload struct {
	SessionID     string        `json:"session_id"`
	AgentState    string        `json:"agent_state"`
	ContextWindow ContextWindow `json:"context_window"`
	Subagents     []Subagent    `json:"subagents"`
	Model         ModelInfo     `json:"model"`
	TaskCount     int           `json:"task_count"`
	TerminalWidth int           `json:"terminal_width"`
}

type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type ContextWindow struct {
	TotalInputTokens    int     `json:"total_input_tokens"`
	ContextWindowSize   int     `json:"context_window_size"`
	RemainingPercentage float64 `json:"remaining_percentage"`
	UsedPercentage      float64 `json:"used_percentage"`
}

type Subagent struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

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

type iconMode int

const (
	simple iconMode = iota
	nf
	emoji
)

func Run(data []byte, icon string) (string, Payload, error) {
	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		return "", p, fmt.Errorf("parsing JSON: %w", err)
	}

	var res string
	switch icon {
	case "nf":
		res = renderIcon(p, nf)
	case "emoji":
		res = renderIcon(p, emoji)
	default:
		res = renderSimple(p)
	}

	return res, p, nil
}

func formatTokens(v int) string {
	if v >= m {
		return fmt.Sprintf("%dM", v/m)
	}
	if v >= k {
		return fmt.Sprintf("%dK", v/k)
	}
	return fmt.Sprintf("%d", v)
}
