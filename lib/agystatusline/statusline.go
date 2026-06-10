package agystatusline

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	k = 1024
	m = k * k
)

// Payload is the subset of the JSON document we care about.
type Payload struct {
	SessionID       string           `json:"session_id"`
	AgentState      string           `json:"agent_state"`
	ContextWindow   ContextWindow    `json:"context_window"`
	BackgroundTasks []BackgroundTask `json:"background_tasks"`
	Subagents       []Subagent       `json:"subagents"`
	Model           ModelInfo        `json:"model"`
}

type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type ContextWindow struct {
	TotalInputTokens    int     `json:"total_input_tokens"`
	ContextWindowSize   int     `json:"context_window_size"`
	RemainingPercentage float64 `json:"remaining_percentage"`
}

type BackgroundTask struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Index  int    `json:"index"`
}

type Subagent struct {
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

func Run(data []byte) (string, Payload, error) {
	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		return "", p, fmt.Errorf("parsing JSON: %w", err)
	}

	color := remainingColor(p.ContextWindow.RemainingPercentage)

	modelName := p.Model.DisplayName
	if modelName == "" {
		modelName = p.Model.ID
	}

	stateUpper := strings.ToUpper(p.AgentState)

	res := fmt.Sprintf("%s | %s/%s (%s%.0f%%%s)",
		stateUpper,
		formatTokens(p.ContextWindow.TotalInputTokens),
		formatTokens(p.ContextWindow.ContextWindowSize),
		color,
		p.ContextWindow.RemainingPercentage,
		ansiReset,
	)
	if modelName != "" {
		res += fmt.Sprintf(" | %s", modelName)
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
