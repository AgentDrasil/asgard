package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	// Isolate HOME so executor tmp/session dirs land in a test-owned directory
	t.Setenv("HOME", t.TempDir())

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
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

	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte(fmt.Sprintf(`
name: test-sync-wf
tmp_dir: "%s/tmp/${session_id}"
nodes:
  - id: step1
    type: command
    command: "echo test-sync-done"
`, tempDir)), 0644))

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

	t.Run("trigger message with attachments parses and accepts", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-trigger-attachments-1"
		subCh, _, cancel := hub.Subscribe(chatID, 0)
		t.Cleanup(cancel)

		payload := TriggerMessageRequest{
			Prompt: "analyze files",
			ChatID: chatID,
			Attachments: []dbmodels.Attachment{
				{
					Name:     "data.csv",
					Path:     "/malicious/client/path/data.csv",
					Size:     1024,
					MimeType: "text/csv",
				},
			},
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

		// The executor will persist the message and publish to event hub
		select {
		case ev := <-subCh:
			assert.Equal(t, "message", ev.Type)
			require.NotNil(t, ev.Message)
			assert.Equal(t, "analyze files", ev.Message.Content)
			require.Len(t, ev.Message.Attachments, 1)
			assert.Equal(t, "data.csv", ev.Message.Attachments[0].Name)
			assert.Equal(t, int64(1024), ev.Message.Attachments[0].Size)
		case <-time.After(1 * time.Second):
			t.Fatal("timed out waiting for triggered user message event with attachments")
		}
	})
}

func TestFormatPromptWithAttachments(t *testing.T) {
	t.Parallel()

	t.Run("empty attachments returns original prompt", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "Hello World", formatPromptWithAttachments("Hello World", nil))
		assert.Equal(t, "Hello World", formatPromptWithAttachments("Hello World", []dbmodels.Attachment{}))
	})

	t.Run("single and multiple attachments injects sandbox path", func(t *testing.T) {
		t.Parallel()
		atts := []dbmodels.Attachment{
			{Name: "report.pdf", Size: 2048, Path: "/ignored/path"},
			{Name: "data.csv", Size: 512},
		}
		res := formatPromptWithAttachments("Analyze data", atts)
		expected := "Analyze data\n\n[Attached Files]\n- report.pdf (/tmp/attachments/report.pdf, 2048 bytes)\n- data.csv (/tmp/attachments/data.csv, 512 bytes)\nPlease inspect and process these attachments directly from the sandbox filesystem."
		assert.Equal(t, expected, res)
	})

	t.Run("ignores client fake path and protects against path traversal", func(t *testing.T) {
		t.Parallel()
		atts := []dbmodels.Attachment{
			{Name: "../../../etc/passwd", Size: 100, Path: "/etc/passwd"},
			{Name: "..\\..\\windows\\system32\\calc.exe", Size: 200, Path: "C:\\calc.exe"},
			{Name: "normal.txt", Size: 50, Path: "/malicious/path"},
		}
		res := formatPromptWithAttachments("Check files", atts)
		// Directory traversals in Name are filtered out by base != rawName check
		assert.Contains(t, res, "- normal.txt (/tmp/attachments/normal.txt, 50 bytes)")
		assert.NotContains(t, res, "passwd")
		assert.NotContains(t, res, "calc.exe")
		assert.NotContains(t, res, "/malicious/path")
	})

	t.Run("enforces max 20 attachments limit", func(t *testing.T) {
		t.Parallel()
		atts := make([]dbmodels.Attachment, 25)
		for i := 0; i < 25; i++ {
			atts[i] = dbmodels.Attachment{
				Name: fmt.Sprintf("file_%d.txt", i),
				Size: int64(i),
			}
		}
		res := formatPromptWithAttachments("Many files", atts)
		assert.Contains(t, res, "file_0.txt")
		assert.Contains(t, res, "file_19.txt")
		assert.NotContains(t, res, "file_20.txt")
	})

	t.Run("filters out invalid names and names exceeding 255 chars", func(t *testing.T) {
		t.Parallel()
		tooLongName := strings.Repeat("a", 256) + ".txt"
		atts := []dbmodels.Attachment{
			{Name: tooLongName, Size: 100},
			{Name: "bad\x00name.txt", Size: 100},
			{Name: "bad\nname.txt", Size: 100},
			{Name: ".", Size: 100},
			{Name: "..", Size: 100},
			{Name: "/", Size: 100},
			{Name: "valid.txt", Size: 100},
		}
		res := formatPromptWithAttachments("Check invalid", atts)
		assert.Contains(t, res, "- valid.txt (/tmp/attachments/valid.txt, 100 bytes)")
		assert.NotContains(t, res, tooLongName)
		assert.NotContains(t, res, "bad")
	})
}
