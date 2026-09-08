package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

const redriveTestYAML = `
name: redrive-api-test
nodes:
  - id: ok_step
    type: command
    command: "echo ok"
  - id: retry_step
    type: command
    depends:
      - node: ok_step
    command: "echo retried"
  - id: after_step
    type: command
    depends:
      - node: retry_step
    command: "echo after"
`

func newRedriveTestServer(t *testing.T) (*Server, *workflowRunStore, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	testDB := db.NewDBForTest(t)
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)
	wfRepo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
	store := newWorkflowRunStore(wfRepo)

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	engine := workflow.NewEngine(registry)
	engine.SetRunStore(store)

	s := &Server{repo: repo, workflowEngine: engine, workflowRunRepo: wfRepo}
	engine.SetHumanSuspender(s.suspendWorkflowHuman)
	return s, store, t.TempDir()
}

func seedFailedRun(t *testing.T, store *workflowRunStore, chatID, runID, runDir string) {
	t.Helper()
	require.NoError(t, store.StartRun(&workflow.RunSnapshot{
		RunID:     runID,
		SessionID: chatID,
		Status:    workflow.PersistStatusFailed,
		DAGSpec:   redriveTestYAML,
		RunDir:    runDir,
	}))
	require.NoError(t, store.SettleRun(runID, workflow.PersistStatusFailed, map[string]workflow.PersistedNodeState{
		"ok_step":    {Status: string(workflowspec.StatusSucceeded)},
		"retry_step": {Status: string(workflowspec.StatusFailed), Error: "boom"},
		"after_step": {Status: string(workflowspec.StatusSkipped), SkipReason: string(workflowspec.SkipReasonCascadedFailure)},
	}))
}

func TestHandleWorkflowRedrive(t *testing.T) {
	s, store, runDir := newRedriveTestServer(t)
	chatID := "chat-wf-redrive"
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))
	seedFailedRun(t, store, chatID, "run-failed-1", runDir)

	req := httptest.NewRequest(http.MethodPost, "/api/workflows/run-failed-1/redrive", nil)
	req.SetPathValue("runID", "run-failed-1")
	rec := httptest.NewRecorder()
	s.handleWorkflowRedrive(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code)

	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := store.GetRun("run-failed-1")
		require.NoError(t, err)
		if run != nil && run.Status == workflow.PersistStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("workflow run did not complete after redrive; status=%v", run)
		}
		time.Sleep(10 * time.Millisecond)
	}

	run, err := store.GetRun("run-failed-1")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, string(workflowspec.StatusSucceeded), run.NodeStates["retry_step"].Status)
	assert.Equal(t, string(workflowspec.StatusSucceeded), run.NodeStates["after_step"].Status)

	// Non-FAILED runs are rejected with a conflict.
	req2 := httptest.NewRequest(http.MethodPost, "/api/workflows/run-failed-1/redrive", nil)
	req2.SetPathValue("runID", "run-failed-1")
	rec2 := httptest.NewRecorder()
	s.handleWorkflowRedrive(rec2, req2)
	assert.Equal(t, http.StatusConflict, rec2.Code)

	// Unknown runs are rejected with 404.
	req3 := httptest.NewRequest(http.MethodPost, "/api/workflows/run-nope/redrive", nil)
	req3.SetPathValue("runID", "run-nope")
	rec3 := httptest.NewRecorder()
	s.handleWorkflowRedrive(rec3, req3)
	assert.Equal(t, http.StatusNotFound, rec3.Code)
}

// TestHandleWorkflowRedrive_MarksRunningInChat ensures the redriven run keeps
// the session's isRunning lifecycle consistent (running during execution,
// settled at the end).
func TestHandleWorkflowRedrive_MarksRunningInChat(t *testing.T) {
	s, store, runDir := newRedriveTestServer(t)
	chatID := "chat-wf-redrive-status"
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))
	seedFailedRun(t, store, chatID, "run-failed-2", runDir)

	req := httptest.NewRequest(http.MethodPost, "/api/workflows/run-failed-2/redrive", nil)
	req.SetPathValue("runID", "run-failed-2")
	rec := httptest.NewRecorder()
	s.handleWorkflowRedrive(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code)

	deadline := time.Now().Add(5 * time.Second)
	for {
		sess, err := s.repo.GetSession(chatID)
		require.NoError(t, err)
		if sess != nil && len(sess.Agents) > 0 && sess.Agents[0].Status == dbmodels.AgentStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session agent did not settle to completed; session=%v", fmt.Sprintf("%+v", sess))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandleSessionWorkflowRuns(t *testing.T) {
	s, store, runDir := newRedriveTestServer(t)
	chatID := "chat-wf-list"
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))
	seedFailedRun(t, store, chatID, "run-list-1", runDir)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+chatID+"/workflows", nil)
	req.SetPathValue("id", chatID)
	rec := httptest.NewRecorder()
	s.handleSessionWorkflowRuns(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Runs []struct {
			RunID  string `json:"runId"`
			Status string `json:"status"`
		} `json:"runs"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
	require.Len(t, payload.Runs, 1)
	assert.Equal(t, "run-list-1", payload.Runs[0].RunID)
	assert.Equal(t, "FAILED", payload.Runs[0].Status)

	// Invalid session id is rejected.
	req2 := httptest.NewRequest(http.MethodGet, "/api/sessions/bad/id/workflows", nil)
	req2.SetPathValue("id", "../traversal")
	rec2 := httptest.NewRecorder()
	s.handleSessionWorkflowRuns(rec2, req2)
	assert.Equal(t, http.StatusBadRequest, rec2.Code)
}
