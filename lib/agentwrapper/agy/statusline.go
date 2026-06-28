package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// parseStatusLineFromSession reads the statusline JSON for the given session ID from
// /tmp/agystatusline/<sessionID>.json, extracts the session_id, inputTokens, maxTokens, and remaining.
func parseStatusLineFromSession(awSessionID string) (sessionID string, inputTokens, maxTokens int, remaining float64) {
	if awSessionID == "" {
		return
	}
	filePath := filepath.Join("/tmp/agystatusline", awSessionID+".json")
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
		SessionID     string        `json:"session_id"`
		ContextWindow contextWindow `json:"context_window"`
	}
	var p payload
	if err := json.Unmarshal(data, &p); err == nil {
		sessionID = p.SessionID
		inputTokens = p.ContextWindow.TotalInputTokens
		maxTokens = p.ContextWindow.ContextWindowSize
		remaining = p.ContextWindow.RemainingPercentage / 100.0
	}
	return
}
