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

func TestRunWorkflow_PreExecutionError_Cleanup(t *testing.T) {
	testDB := db.NewDBForTest(t)
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)
	store := newWorkflowRunStore(wfRepo)

	registry := workflow.NewNodeRunnerRegistry()
	engine := workflow.NewEngine(registry)
	engine.SetRunStore(store)

	hub := NewSessionEventHubWithCapacity(50)
	t.Cleanup(hub.Close)

	tempDir := t.TempDir()
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
