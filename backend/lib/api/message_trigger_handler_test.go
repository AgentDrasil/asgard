package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func TestMessageTriggerHandler(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)

	agentConfig := agentspec.AgentConfig{
		ID:          "test-agent",
		Name:        "Test Agent",
		Description: "A test agent",
		Type:        "agent",
	}
	agent := &agentspec.Agent{
		Config: agentConfig,
	}

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	engine := workflow.NewEngine(registry)

	tempDir := t.TempDir()
	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte(`
name: test-sync-wf
tmp_dir: "tmp/${session_id}"
nodes:
  - id: step1
    type: command
    command: "echo test-sync-done"
`), 0644))

	wfAgent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "test-wf-agent",
			Name: "Test Workflow Agent",
			Type: "workflow",
		},
		WorkflowPath: wfFile,
	}

	server := &Server{
		conf:           &config.Config{},
		repo:           repo,
		eventHub:       hub,
		workflowEngine: engine,
		agents:         []*agentspec.Agent{agent, wfAgent},
	}
	server.mux = server.buildMuxLocked()

	t.Run("agent not found", func(t *testing.T) {
		t.Parallel()

		body := bytes.NewBufferString(`{"prompt":"hello"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/agents/unknown-agent/message", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("empty prompt rejected", func(t *testing.T) {
		t.Parallel()

		body := bytes.NewBufferString(`{"prompt":"   "}`)
		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("trigger message accepted and published", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-trigger-1"
		subCh, _, cancel := hub.Subscribe(chatID, 0)
		t.Cleanup(cancel)

		payload := TriggerMessageRequest{
			Prompt: "trigger prompt",
			ChatID: chatID,
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusAccepted, rr.Code)

		var resp map[string]any
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, "accepted", resp["status"])
		assert.Equal(t, chatID, resp["chatId"])

		// The executor will run in background and append user message, publishing it to EventHub
		select {
		case ev := <-subCh:
			assert.Equal(t, "message", ev.Type)
			require.NotNil(t, ev.Message)
			assert.Equal(t, "trigger prompt", ev.Message.Content)
		case <-time.After(1 * time.Second):
			t.Fatal("timed out waiting for triggered user message event")
		}
	})

	t.Run("concurrent trigger on same chat rejected with conflict", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-conflict-1"
		server.activeExecutions.Store(chatID, struct{}{})
		t.Cleanup(func() { server.activeExecutions.Delete(chatID) })

		payload := TriggerMessageRequest{
			Prompt: "another prompt",
			ChatID: chatID,
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusConflict, rr.Code)
	})

	t.Run("workflow sync wait mode returns 200 with result", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-sync-wait-1"
		payload := TriggerMessageRequest{
			Prompt: "sync prompt",
			ChatID: chatID,
			Wait:   true,
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-wf-agent/message", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)

		var resp map[string]any
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, "completed", resp["status"])
		assert.Equal(t, chatID, resp["chatId"])
	})

	t.Run("single agent sync wait returns error on execution failure", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-single-agent-wait-fail"
		payload := TriggerMessageRequest{
			Prompt: "single agent fail prompt",
			ChatID: chatID,
			Wait:   true,
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusInternalServerError, rr.Code)

		var resp map[string]any
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, "failed", resp["status"])
		assert.NotEmpty(t, resp["error"])
		assert.Equal(t, chatID, resp["chatId"])
	})
}
