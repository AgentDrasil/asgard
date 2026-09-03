package api

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestRunWorkflow_PreExecutionError_Cleanup(t *testing.T) {
	testDB := db.NewDBForTest(t)
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	tempDir := t.TempDir()
	repo := dbmodels.NewSessionRepository(testDB)
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)
	store := newWorkflowRunStore(wfRepo)

	registry := workflow.NewNodeRunnerRegistry()
	engine := workflow.NewEngine(registry)
	engine.SetRunStore(store)

	hub := NewSessionEventHubWithCapacity(50)
	t.Cleanup(hub.Close)

	// Invalid YAML content to cause LoadDefinition failure
	wfFile := filepath.Join(tempDir, "invalid_workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte("invalid: yaml: : : ["), 0644))

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "wf-invalid-agent",
			Name: "Workflow Invalid Agent",
			Type: "workflow",
		},
		WorkflowPath: wfFile,
	}

	s := &Server{
		conf:            &config.Config{},
		repo:            repo,
		eventHub:        hub,
		workflowEngine:  engine,
		workflowRunRepo: wfRepo,
		agents:          []*agentspec.Agent{agent},
	}
	s.mux = s.buildMuxLocked()

	chatID := "chat-wf-pre-exec-error"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-invalid-agent"}))

	subCh, doneCh, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	receivedStatusFalse := false
	receivedDone := false
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for {
			select {
			case ev := <-subCh:
				if ev.Type == "status" {
					if isRunning, ok := ev.Payload["isRunning"].(bool); ok && !isRunning {
						receivedStatusFalse = true
					}
				}
				if ev.Type == "done" {
					receivedDone = true
				}
			case <-doneCh:
				for {
					select {
					case ev := <-subCh:
						if ev.Type == "status" {
							if isRunning, ok := ev.Payload["isRunning"].(bool); ok && !isRunning {
								receivedStatusFalse = true
							}
						}
						if ev.Type == "done" {
							receivedDone = true
						}
					default:
						return
					}
				}
			}
		}
	}()

	triggerPayload := map[string]any{
		"prompt": "start invalid flow",
		"chatId": chatID,
	}
	raw, err := json.Marshal(triggerPayload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/wf-invalid-agent/message?wait=true", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	// Since LoadDefinition fails, runWorkflow should return failed status with an error
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-eventsDone

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.False(t, s.isSessionRunning(sess), "Session must not be running after pre-execution error")
	assert.False(t, sess.IsRunning(), "Session agent status must be completed")
	assert.True(t, receivedStatusFalse, "Must receive status {isRunning: false}")
	assert.True(t, receivedDone, "Must receive done event")
}

func TestWorkflowHandler_PersistAttachmentsAndEntryPrompt(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	tempDir := t.TempDir()
	repo := dbmodels.NewSessionRepository(testDB)
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)
	store := newWorkflowRunStore(wfRepo)

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	engine := workflow.NewEngine(registry)
	engine.SetRunStore(store)

	hub := NewSessionEventHubWithCapacity(50)
	t.Cleanup(hub.Close)

	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte(fmt.Sprintf(`
name: test-wf-attachments
tmp_dir: "%s/tmp/${session_id}"
nodes:
  - id: step1
    type: command
    command: "echo wf-done"
`, tempDir)), 0644))

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "wf-attachments-agent",
			Name: "Workflow Attachments Agent",
			Type: "workflow",
		},
		WorkflowPath: wfFile,
	}

	s := &Server{
		conf:            &config.Config{},
		repo:            repo,
		eventHub:        hub,
		workflowEngine:  engine,
		workflowRunRepo: wfRepo,
		agents:          []*agentspec.Agent{agent},
	}
	s.mux = s.buildMuxLocked()

	chatID := "chat-wf-attachments-test"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-attachments-agent"}))

	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	attachments := []dbmodels.Attachment{
		{
			Name:     "input.json",
			Path:     "/ignored/path/input.json",
			Size:     256,
			MimeType: "application/json",
		},
	}

	triggerPayload := TriggerMessageRequest{
		Prompt:      "run workflow with file",
		ChatID:      chatID,
		Wait:        true,
		Attachments: attachments,
	}
	raw, err := json.Marshal(triggerPayload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/wf-attachments-agent/message?wait=true", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify DB message persistence
	session, err := repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotEmpty(t, session.Messages)

	var userMsg *dbmodels.ChatMessage
	for _, m := range session.Messages {
		if m.Role == "user" {
			userMsg = &m
			break
		}
	}
	require.NotNil(t, userMsg)
	// Content must remain raw prompt
	assert.Equal(t, "run workflow with file", userMsg.Content)
	require.Len(t, userMsg.Attachments, 1)
	assert.Equal(t, "input.json", userMsg.Attachments[0].Name)
	assert.Equal(t, int64(256), userMsg.Attachments[0].Size)

	// Verify SSE user message event
	select {
	case ev := <-subCh:
		assert.Equal(t, "message", ev.Type)
		require.NotNil(t, ev.Message)
		assert.Equal(t, "run workflow with file", ev.Message.Content)
		require.Len(t, ev.Message.Attachments, 1)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for SSE message event in workflow")
	}
}
