package agy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const pollInterval time.Duration = 200 * time.Millisecond

// isIdle reads the statusline JSON for the given session ID from
// /tmp/agystatusline/<sessionID>.json, and returns true if the agent state
// is idle, background tasks are empty, and all subagents are idle.
func isIdle(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	filePath := filepath.Join("/tmp/agystatusline", sessionID+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		return false
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

	type payload struct {
		AgentState      string           `json:"agent_state"`
		BackgroundTasks []backgroundTask `json:"background_tasks"`
		Subagents       []subagent       `json:"subagents"`
	}

	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return false
	}

	if !strings.EqualFold(p.AgentState, "idle") {
		return false
	}

	if len(p.BackgroundTasks) > 0 {
		return false
	}

	for _, s := range p.Subagents {
		if !strings.EqualFold(s.Status, "idle") {
			return false
		}
	}

	return true
}

// pollUntilIdle polls the statusline JSON file every pollInterval until isIdle returns
// true or timeout elapses. The timeout is soft: expiry causes the function to
// return (timedOut=true, err=nil) so the caller can decide whether to proceed
// or abort. ctx cancellation and unexpected agy exit are hard errors.
func pollUntilIdle(ctx context.Context, sessionID string, done <-chan error, timeout time.Duration) (timedOut bool, err error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		select {
		case <-tick.C:
			if isIdle(sessionID) {
				return false, nil
			}
		case <-timer.C:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		case err := <-done:
			return false, fmt.Errorf("agy exited unexpectedly: %w", err)
		}
	}
}
