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

func TestSessionHandler(t *testing.T) {
	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	conf := &config.Config{
		Host: "http://localhost:8080",
	}

	server := &Server{
		conf: conf,
		repo: repo,
	}
	server.mux = server.buildMuxLocked()

	// 1. GET /api/sessions should start empty
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	var sessions []ChatSession
	err = json.Unmarshal(rr.Body.Bytes(), &sessions)
	require.NoError(t, err)
	assert.Empty(t, sessions)

	// 1b. POST /api/sessions should create a new session with UUIDv7 chatID
	postReq := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"currentAgent":"agent-alpha","runDir":"/tmp"}`))
	postReq.Header.Set("Content-Type", "application/json")
	rrPost := httptest.NewRecorder()
	server.ServeHTTP(rrPost, postReq)
	assert.Equal(t, http.StatusCreated, rrPost.Code)

	var createdSession ChatSession
	err = json.Unmarshal(rrPost.Body.Bytes(), &createdSession)
	require.NoError(t, err)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	expectedSessionTmp := filepath.Join(home, "tmp", createdSession.ChatID)

	assert.NotEmpty(t, createdSession.ChatID)
	assert.Equal(t, "agent-alpha", createdSession.CurrentAgent)
	assert.Equal(t, expectedSessionTmp, createdSession.RunDir)

	// 2. Insert session via repo
	err = repo.SaveSession(&dbmodels.Session{
		ChatID:       "chat-1",
		Title:        "My First Chat",
		CurrentAgent: "agent-alpha",
		RunDir:       "/path/to/run",
		Messages: []dbmodels.ChatMessage{
			{
				ID:      "msg-1",
				Role:    "user",
				Content: "Hello",
			},
			{
				ID:      "msg-2",
				Role:    "assistant",
				Content: "Hi there",
			},
		},
	})
	require.NoError(t, err)

	// 3. GET /api/sessions/{id} should return the created session with messages
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/chat-1", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	var fetchedSession ChatSession
	err = json.Unmarshal(rr.Body.Bytes(), &fetchedSession)
	require.NoError(t, err)
	assert.Equal(t, "chat-1", fetchedSession.ChatID)
	assert.Equal(t, "My First Chat", fetchedSession.Title)
	assert.Equal(t, "agent-alpha", fetchedSession.CurrentAgent)
	assert.Equal(t, "/path/to/run", fetchedSession.RunDir)
	require.Len(t, fetchedSession.Messages, 2)
	assert.Equal(t, "msg-1", fetchedSession.Messages[0].ID)
	assert.Equal(t, "Hello", fetchedSession.Messages[0].Content)
	assert.False(t, fetchedSession.IsRunning)

	// Update agent status to running and test isRunning == true
	err = repo.UpdateAgentStatus("chat-1", "agent-alpha", dbmodels.AgentStatusRunning)
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/api/sessions/chat-1", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	var runningSession ChatSession
	err = json.Unmarshal(rr.Body.Bytes(), &runningSession)
	require.NoError(t, err)
	assert.True(t, runningSession.IsRunning)

	// Set agent status back to completed, but store in activeExecutions
	err = repo.UpdateAgentStatus("chat-1", "agent-alpha", dbmodels.AgentStatusCompleted)
	require.NoError(t, err)

	server.activeExecutions.Store("chat-1", struct{}{})
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/chat-1", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	err = json.Unmarshal(rr.Body.Bytes(), &runningSession)
	require.NoError(t, err)
	assert.True(t, runningSession.IsRunning)
	server.activeExecutions.Delete("chat-1")

	// Test workflowRunRepo status RUNNING detection
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)
	server.workflowRunRepo = wfRepo
	require.NoError(t, wfRepo.SaveRun(&dbmodels.WorkflowRun{
		RunID:     "run-wf-1",
		SessionID: "chat-1",
		Status:    dbmodels.WorkflowStatusRunning,
	}))

	req = httptest.NewRequest(http.MethodGet, "/api/sessions/chat-1", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	err = json.Unmarshal(rr.Body.Bytes(), &runningSession)
	require.NoError(t, err)
	assert.True(t, runningSession.IsRunning)

	require.NoError(t, wfRepo.SaveRun(&dbmodels.WorkflowRun{
		RunID:     "run-wf-1",
		SessionID: "chat-1",
		Status:    dbmodels.WorkflowStatusCompleted,
	}))

	req = httptest.NewRequest(http.MethodGet, "/api/sessions/chat-1", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	err = json.Unmarshal(rr.Body.Bytes(), &runningSession)
	require.NoError(t, err)
	assert.False(t, runningSession.IsRunning)

	// 4. Test limit 20 and ordering by update time
	// Delete chat-1 first so we start clean
	req = httptest.NewRequest(http.MethodDelete, "/api/sessions?chat_id=chat-1", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	// Insert 22 sessions directly via repo
	for i := 1; i <= 22; i++ {
		err := repo.SaveSession(&dbmodels.Session{
			ChatID:       fmt.Sprintf("chat-%d", i),
			Title:        fmt.Sprintf("Chat %d", i),
			CurrentAgent: "agent",
			RunDir:       "/",
		})
		require.NoError(t, err)
	}

	// GET /api/sessions should return 20 sessions
	req = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	err = json.Unmarshal(rr.Body.Bytes(), &sessions)
	require.NoError(t, err)
	assert.Len(t, sessions, 20)

	// The first session in the list should be the last one inserted (chat-22)
	assert.Equal(t, "chat-22", sessions[0].ChatID)
	assert.Equal(t, "chat-3", sessions[19].ChatID) // chat-1 and chat-2 are pushed out of the top 20
}

func TestGetSessionByID_WorkflowRunningStatus(t *testing.T) {
	testDB := db.NewDBForTest(t)
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)
	store := newWorkflowRunStore(wfRepo)

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	engine := workflow.NewEngine(registry)
	engine.SetRunStore(store)

	hub := NewSessionEventHubWithCapacity(50)
	t.Cleanup(hub.Close)

	tempDir := t.TempDir()
	wfFile := filepath.Join(tempDir, "workflow.yaml")
	statusFlowYAML := fmt.Sprintf(`
name: status-flow
tmp_dir: "%s/tmp/${session_id}"
nodes:
  - id: pre_step
    type: command
    command: "sleep 0.2 && echo ready > ${tmp_dir}/pre.txt"
  - id: entry_question
    type: human
    depends:
      - node: pre_step
    prompt: "please approve the plan"
  - id: final
    type: command
    depends:
      - node: entry_question
    command: "sleep 0.2 && echo done > ${tmp_dir}/final.txt"
`, tempDir)
	require.NoError(t, os.WriteFile(wfFile, []byte(statusFlowYAML), 0644))

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "wf-status-agent",
			Name: "Workflow Status Agent",
			Type: "workflow",
		},
		WorkflowPath: wfFile,
	}

	server := &Server{
		conf:            &config.Config{},
		repo:            repo,
		eventHub:        hub,
		workflowEngine:  engine,
		workflowRunRepo: wfRepo,
		agents:          []*agentspec.Agent{agent},
	}
	server.mux = server.buildMuxLocked()
	engine.SetHumanSuspender(server.suspendWorkflowHuman)

	chatID := "chat-wf-session-status"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-status-agent"}))

	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	// Trigger workflow
	triggerPayload := map[string]any{
		"prompt": "start status check flow",
		"chatId": chatID,
	}
	raw, err := json.Marshal(triggerPayload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/wf-status-agent/message", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusAccepted, rec.Code)

	// Stage 1: Initial execution -> GET /api/sessions/:id should have IsRunning == true
	reqGet := httptest.NewRequest(http.MethodGet, "/api/sessions/"+chatID, nil)
	recGetInitial := httptest.NewRecorder()
	server.ServeHTTP(recGetInitial, reqGet)
	assert.Equal(t, http.StatusOK, recGetInitial.Code)

	var sessRespInitial ChatSession
	require.NoError(t, json.Unmarshal(recGetInitial.Body.Bytes(), &sessRespInitial))
	assert.True(t, sessRespInitial.IsRunning, "Session IsRunning must be true during initial execution")

	var askMessageID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && askMessageID == "" {
		select {
		case ev := <-subCh:
			if ev.Type == "message" && ev.Message != nil && ev.Message.Role == "ask_user" {
				askMessageID = ev.Message.ID
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	require.NotEmpty(t, askMessageID)

	// Stage 2: Suspended / waiting human stage -> GET /api/sessions/:id should have IsRunning == false
	recGet := httptest.NewRecorder()
	server.ServeHTTP(recGet, reqGet)
	assert.Equal(t, http.StatusOK, recGet.Code)

	var sessResp ChatSession
	require.NoError(t, json.Unmarshal(recGet.Body.Bytes(), &sessResp))
	assert.False(t, sessResp.IsRunning, "Session IsRunning must be false while waiting for human input")

	// Resume the workflow
	replyRec := postAskUserReply(t, server, chatID, askMessageID, "Approved Status")
	assert.Equal(t, http.StatusOK, replyRec.Code)

	// Stage 3: Resumed / running stage -> GET /api/sessions/:id should have IsRunning == true
	recGetResumed := httptest.NewRecorder()
	server.ServeHTTP(recGetResumed, reqGet)
	assert.Equal(t, http.StatusOK, recGetResumed.Code)

	var sessRespResumed ChatSession
	require.NoError(t, json.Unmarshal(recGetResumed.Body.Bytes(), &sessRespResumed))
	assert.True(t, sessRespResumed.IsRunning, "Session IsRunning must be true during resumed execution")

	// Wait for completion
	waitForRunStatus(t, testDB, chatID, workflow.PersistStatusCompleted)
	time.Sleep(50 * time.Millisecond)

	// Stage 4: Execution finished -> GET /api/sessions/:id should have IsRunning == false
	recGetCompleted := httptest.NewRecorder()
	server.ServeHTTP(recGetCompleted, reqGet)
	assert.Equal(t, http.StatusOK, recGetCompleted.Code)

	var sessRespCompleted ChatSession
	require.NoError(t, json.Unmarshal(recGetCompleted.Body.Bytes(), &sessRespCompleted))
	assert.False(t, sessRespCompleted.IsRunning, "Session IsRunning must be false once completed")
}
