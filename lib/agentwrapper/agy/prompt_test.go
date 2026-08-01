package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

func TestEnsureWorkspaceTrusted(t *testing.T) {
	tempHome := t.TempDir()

	oldHome := os.Getenv("HOME")
	err := os.Setenv("HOME", tempHome)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Setenv("HOME", oldHome)
	})

	cliDir := filepath.Join(tempHome, ".gemini", "antigravity-cli")
	err = os.MkdirAll(cliDir, 0755)
	require.NoError(t, err)

	settingsPath := filepath.Join(cliDir, "settings.json")

	// 1. Write an initial settings.json with a trusted workspace
	initialSettings := `{
  "model": "test-model",
  "trustedWorkspaces": [
    "/some/trusted/path"
  ]
}`
	err = os.WriteFile(settingsPath, []byte(initialSettings), 0644)
	require.NoError(t, err)

	// 2. Checking the already trusted path should succeed
	err = ensureWorkspaceTrusted("/some/trusted/path")
	require.NoError(t, err)

	// 3. Checking an untrusted path should add it to settings.json and succeed
	untrustedPath, err := filepath.Abs(".")
	require.NoError(t, err)
	untrustedPath = filepath.Clean(untrustedPath)

	err = ensureWorkspaceTrusted(untrustedPath)
	require.NoError(t, err)

	// 4. Verify settings.json was updated and contains the new path while preserving other keys
	updatedData, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var config struct {
		Model             string   `json:"model"`
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	err = json.Unmarshal(updatedData, &config)
	require.NoError(t, err)

	assert.Equal(t, "test-model", config.Model)
	assert.Contains(t, config.TrustedWorkspaces, "/some/trusted/path")
	assert.Contains(t, config.TrustedWorkspaces, untrustedPath)
}

// TestParseStreamEvents validates that individual stream-json event lines are
// parsed correctly into the expected streamEvent structures.
func TestParseStreamEvents(t *testing.T) {
	t.Run("init event", func(t *testing.T) {
		raw := `{"event":"init","conversation_id":"abc-123","init":{"cwd":"/home/user","tools":["run_command"],"permission_mode":"always-proceed"}}`
		var ev streamEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &ev))
		assert.Equal(t, "init", ev.Event)
		assert.Equal(t, "abc-123", ev.ConversationID)
		require.NotNil(t, ev.Init)
		assert.Equal(t, "/home/user", ev.Init.CWD)
		assert.Equal(t, []string{"run_command"}, ev.Init.Tools)
	})

	t.Run("step_update tool ACTIVE", func(t *testing.T) {
		raw := `{"event":"step_update","step_update":{"conversation_id":"abc-123","step_index":3,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"go version"}}}}`
		var ev streamEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &ev))
		assert.Equal(t, "step_update", ev.Event)
		require.NotNil(t, ev.StepUpdate)
		su := ev.StepUpdate
		assert.Equal(t, 3, su.StepIndex)
		assert.Equal(t, "ACTIVE", su.State)
		assert.Equal(t, "tool", su.StepType)
		assert.Equal(t, "run_command", su.ToolName)
		require.NotNil(t, su.ToolInfo)
		assert.Equal(t, map[string]any{"CommandLine": "go version"}, su.ToolInfo.Parameters)
	})

	t.Run("step_update tool DONE with output", func(t *testing.T) {
		raw := `{"event":"step_update","step_update":{"conversation_id":"abc-123","step_index":3,"state":"DONE","step_type":"tool","tool_name":"run_command","duration_seconds":0.1,"tool_info":{"name":"run_command","parameters":{"CommandLine":"go version"},"output":"go version go1.26.0"}}}`
		var ev streamEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &ev))
		require.NotNil(t, ev.StepUpdate)
		su := ev.StepUpdate
		assert.Equal(t, "DONE", su.State)
		require.NotNil(t, su.ToolInfo)
		assert.Equal(t, "go version go1.26.0", su.ToolInfo.Output)
	})

	t.Run("step_update tool DONE with error", func(t *testing.T) {
		raw := `{"event":"step_update","step_update":{"conversation_id":"abc-123","step_index":6,"state":"ERROR","step_type":"tool","tool_name":"list_dir","duration_seconds":0.05,"tool_info":{"name":"list_dir","parameters":{"DirectoryPath":"/restricted"},"error":{"type":"TOOL_ERROR","message":"Permission denied"}}}}`
		var ev streamEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &ev))
		require.NotNil(t, ev.StepUpdate)
		su := ev.StepUpdate
		require.NotNil(t, su.ToolInfo)
		require.NotNil(t, su.ToolInfo.Error)
		assert.Equal(t, "TOOL_ERROR", su.ToolInfo.Error.Type)
		assert.Equal(t, "Permission denied", su.ToolInfo.Error.Message)
	})

	t.Run("step_update agent_response DONE", func(t *testing.T) {
		raw := `{"event":"step_update","step_update":{"conversation_id":"abc-123","step_index":5,"state":"DONE","step_type":"agent_response","text_delta":"The answer is 42.\n","duration_seconds":0.8,"usage":{"input_tokens":100,"output_tokens":20,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":120}}}`
		var ev streamEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &ev))
		require.NotNil(t, ev.StepUpdate)
		su := ev.StepUpdate
		assert.Equal(t, "agent_response", su.StepType)
		assert.Equal(t, "DONE", su.State)
		assert.Equal(t, "The answer is 42.\n", su.TextDelta)
		require.NotNil(t, su.Usage)
		assert.Equal(t, 100, su.Usage.InputTokens)
	})

	t.Run("result event", func(t *testing.T) {
		raw := `{"event":"result","result":{"conversation_id":"abc-123","status":"SUCCESS","response":"The answer is 42.","duration_seconds":5.0,"num_turns":2,"usage":{"input_tokens":200,"output_tokens":40,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":240}}}`
		var ev streamEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &ev))
		assert.Equal(t, "result", ev.Event)
		require.NotNil(t, ev.Result)
		r := ev.Result
		assert.Equal(t, "abc-123", r.ConversationID)
		assert.Equal(t, "SUCCESS", r.Status)
		assert.Equal(t, "The answer is 42.", r.Response)
		assert.Equal(t, 2, r.NumTurns)
		require.NotNil(t, r.Usage)
		assert.Equal(t, 200, r.Usage.InputTokens)
	})
}

// TestPromptReportCallback validates that ReportCallback is invoked with the
// correct arguments by simulating parsed stream events inline.
func TestPromptReportCallback(t *testing.T) {
	type call struct {
		stepIndex int
		source    string
		entryType string
		content   string
	}

	var calls []call
	cb := types.ReportFunc(func(stepIndex int, source, entryType, content string, metadata map[string]any) {
		calls = append(calls, call{stepIndex, source, entryType, content})
	})

	// Simulate what Prompt does internally for tool + agent_response events.
	textByStep := make(map[int]string)

	events := []string{
		// tool ACTIVE
		`{"event":"step_update","step_update":{"step_index":3,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"go version"}}}}`,
		// tool DONE
		`{"event":"step_update","step_update":{"step_index":3,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{},"output":"go version go1.26.0"}}}`,
		// agent_response ACTIVE (partial)
		`{"event":"step_update","step_update":{"step_index":5,"state":"ACTIVE","step_type":"agent_response","text_delta":"The go version is "}}`,
		// agent_response DONE (remainder)
		`{"event":"step_update","step_update":{"step_index":5,"state":"DONE","step_type":"agent_response","text_delta":"go1.26.0.\n"}}`,
	}

	for _, raw := range events {
		var ev streamEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &ev))
		su := ev.StepUpdate
		if su == nil {
			continue
		}

		if su.StepType == "agent_response" && su.TextDelta != "" {
			textByStep[su.StepIndex] += su.TextDelta
		}

		switch {
		case su.StepType == "tool" && su.State == "ACTIVE" && su.ToolName != "":
			metadata := map[string]any{"tool_name": su.ToolName}
			cb(su.StepIndex, "TOOL", "tool_call", su.ToolName, metadata)
		case su.StepType == "tool" && su.State == "DONE" && su.ToolInfo != nil:
			metadata := map[string]any{"tool_name": su.ToolName}
			content := su.ToolInfo.Output
			if su.ToolInfo.Error != nil {
				content = su.ToolInfo.Error.Message
				metadata["error_type"] = su.ToolInfo.Error.Type
			}
			cb(su.StepIndex, "TOOL", "tool_result", content, metadata)
		case su.StepType == "agent_response" && su.State == "DONE":
			full := textByStep[su.StepIndex]
			delete(textByStep, su.StepIndex)
			if full != "" {
				cb(su.StepIndex, "MODEL", "agent_response", full, nil)
			}
		}
	}

	require.Len(t, calls, 3)
	assert.Equal(t, call{3, "TOOL", "tool_call", "run_command"}, calls[0])
	assert.Equal(t, call{3, "TOOL", "tool_result", "go version go1.26.0"}, calls[1])
	assert.Equal(t, call{5, "MODEL", "agent_response", "The go version is go1.26.0.\n"}, calls[2])
}
