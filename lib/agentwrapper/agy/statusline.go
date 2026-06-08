package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// parseStatusLineFromSession reads the statusline JSON for the given session ID from
// /tmp/agystatusline/<sessionID>.json, extracts the inputTokens, maxTokens, and remaining.
func parseStatusLineFromSession(sessionID string) (inputTokens, maxTokens int, remaining float64) {
	if sessionID == "" {
		return
	}
	filePath := filepath.Join("/tmp/agystatusline", sessionID+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	type contextWindow struct {
		TotalInputTokens    int     `json:"total_input_tokens"`
		ContextWindowSize   int     `json:"context_window_size"`
		RemainingPercentage float64 `json:"remaining_percentage"`
	}
	type payload struct {
		ContextWindow contextWindow `json:"context_window"`
	}
	var p payload
	if err := json.Unmarshal(data, &p); err == nil {
		inputTokens = p.ContextWindow.TotalInputTokens
		maxTokens = p.ContextWindow.ContextWindowSize
		remaining = p.ContextWindow.RemainingPercentage / 100.0
	}
	return
}
