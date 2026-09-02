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

	// 4. Test default limit (returns all 22 sessions) and ordering by update time
	// Delete chat-1 and createdSession first so we start clean
	req = httptest.NewRequest(http.MethodDelete, "/api/sessions?chat_id=chat-1", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	req = httptest.NewRequest(http.MethodDelete, "/api/sessions?chat_id="+createdSession.ChatID, nil)
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

	// GET /api/sessions should return all 22 sessions (default limit is 500)
	req = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	err = json.Unmarshal(rr.Body.Bytes(), &sessions)
	require.NoError(t, err)
	assert.Len(t, sessions, 22)

	// The first session in the list should be the last one inserted (chat-22)
	assert.Equal(t, "chat-22", sessions[0].ChatID)
	assert.Equal(t, "chat-1", sessions[21].ChatID)
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

func TestSessionHandler_SearchSessions(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	conf := &config.Config{
		Host: "http://localhost:8080",
	}

	server := &Server{
		conf: conf,
		repo: repo,
	}
	server.mux = server.buildMuxLocked()

	now := time.Now()
	// Seed sessions
	require.NoError(t, repo.SaveSession(&dbmodels.Session{
		ChatID:       "chat-alpha-1",
		Title:        "Project Alpha Architecture",
		CurrentAgent: "agent-1",
		RunDir:       "/tmp/alpha1",
		Agents: dbmodels.Agents{
			{Name: "agent-1", Status: dbmodels.AgentStatusRunning},
		},
		UpdatedAt: now.Add(-5 * time.Minute),
	}))
	require.NoError(t, repo.SaveSession(&dbmodels.Session{
		ChatID:       "chat-beta-1",
		Title:        "Project Beta Plan",
		CurrentAgent: "agent-2",
		RunDir:       "/tmp/beta1",
		Agents: dbmodels.Agents{
			{Name: "agent-2", Status: dbmodels.AgentStatusCompleted},
		},
		UpdatedAt: now.Add(-2 * time.Minute),
	}))
	require.NoError(t, repo.SaveSession(&dbmodels.Session{
		ChatID:       "chat-alpha-2",
		Title:        "alpha feature implementation",
		CurrentAgent: "agent-1",
		RunDir:       "/tmp/alpha2",
		Agents: dbmodels.Agents{
			{Name: "agent-1", Status: dbmodels.AgentStatusCompleted},
		},
		UpdatedAt: now.Add(-1 * time.Minute),
	}))

	// 1. Search with q=alpha -> should match chat-alpha-2 and chat-alpha-1 ordered by updated_at desc
	req := httptest.NewRequest(http.MethodGet, "/api/sessions?q=alpha", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	var alphaSessions []ChatSession
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &alphaSessions))
	require.Len(t, alphaSessions, 2)
	assert.Equal(t, "chat-alpha-2", alphaSessions[0].ChatID)
	assert.Equal(t, "alpha feature implementation", alphaSessions[0].Title)
	assert.Equal(t, "agent-1", alphaSessions[0].CurrentAgent)
	assert.Equal(t, "/tmp/alpha2", alphaSessions[0].RunDir)
	assert.False(t, alphaSessions[0].IsRunning)

	assert.Equal(t, "chat-alpha-1", alphaSessions[1].ChatID)
	assert.Equal(t, "Project Alpha Architecture", alphaSessions[1].Title)
	assert.Equal(t, "agent-1", alphaSessions[1].CurrentAgent)
	assert.Equal(t, "/tmp/alpha1", alphaSessions[1].RunDir)
	assert.True(t, alphaSessions[1].IsRunning)

	// 2. Search with query=Beta -> should match chat-beta-1
	reqBeta := httptest.NewRequest(http.MethodGet, "/api/sessions?query=Beta", nil)
	rrBeta := httptest.NewRecorder()
	server.ServeHTTP(rrBeta, reqBeta)
	assert.Equal(t, http.StatusOK, rrBeta.Code)

	var betaSessions []ChatSession
	require.NoError(t, json.Unmarshal(rrBeta.Body.Bytes(), &betaSessions))
	require.Len(t, betaSessions, 1)
	assert.Equal(t, "chat-beta-1", betaSessions[0].ChatID)
	assert.Equal(t, "Project Beta Plan", betaSessions[0].Title)

	// 3. Search for nonexistent query -> should return 200 OK and empty array []
	reqNone := httptest.NewRequest(http.MethodGet, "/api/sessions?q=nonexistent", nil)
	rrNone := httptest.NewRecorder()
	server.ServeHTTP(rrNone, reqNone)
	assert.Equal(t, http.StatusOK, rrNone.Code)

	var noneSessions []ChatSession
	require.NoError(t, json.Unmarshal(rrNone.Body.Bytes(), &noneSessions))
	require.NotNil(t, noneSessions)
	assert.Empty(t, noneSessions)
}

func TestSessionHandler_ArchiveSession(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	conf := &config.Config{
		Host: "http://localhost:8080",
	}

	server := &Server{
		conf: conf,
		repo: repo,
	}
	server.mux = server.buildMuxLocked()

	now := time.Now()
	chatID := "test-archive-chat-id"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{
		ChatID:       chatID,
		Title:        "Archive Target Session",
		CurrentAgent: "test-agent",
		RunDir:       "/tmp/test",
		IsArchived:   false,
		CreatedAt:    now.Add(-10 * time.Minute),
		UpdatedAt:    now.Add(-5 * time.Minute),
		Messages: []dbmodels.ChatMessage{
			{
				ID:      "msg-ask-1",
				Role:    "ask_user",
				Content: "Need approval",
				Replied: false,
			},
		},
	}))

	// 1. Verify GET /api/sessions/:id returns isWaitingForUser: true, isArchived: false, createdAt, updatedAt
	reqGet := httptest.NewRequest(http.MethodGet, "/api/sessions/"+chatID, nil)
	rrGet := httptest.NewRecorder()
	server.ServeHTTP(rrGet, reqGet)
	assert.Equal(t, http.StatusOK, rrGet.Code)

	var initialSession ChatSession
	require.NoError(t, json.Unmarshal(rrGet.Body.Bytes(), &initialSession))
	assert.Equal(t, chatID, initialSession.ChatID)
	assert.False(t, initialSession.IsArchived)
	assert.True(t, initialSession.IsWaitingForUser)
	require.NotNil(t, initialSession.CreatedAt)
	require.NotNil(t, initialSession.UpdatedAt)

	// 2. Call POST /api/sessions/:id/archive with invalid ID -> 400 Bad Request
	reqInvalid := httptest.NewRequest(http.MethodPost, "/api/sessions/invalid%20id/archive", nil)
	rrInvalid := httptest.NewRecorder()
	server.ServeHTTP(rrInvalid, reqInvalid)
	assert.Equal(t, http.StatusBadRequest, rrInvalid.Code)

	// 3. Call POST /api/sessions/:id/archive with valid ID -> 200 OK {"status": "success"}
	reqArchive := httptest.NewRequest(http.MethodPost, "/api/sessions/"+chatID+"/archive", nil)
	rrArchive := httptest.NewRecorder()
	server.ServeHTTP(rrArchive, reqArchive)
	assert.Equal(t, http.StatusOK, rrArchive.Code)

	var archiveResp map[string]string
	require.NoError(t, json.Unmarshal(rrArchive.Body.Bytes(), &archiveResp))
	assert.Equal(t, "success", archiveResp["status"])

	// 4. Verify GET /api/sessions does NOT return the archived session
	reqListActive := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rrListActive := httptest.NewRecorder()
	server.ServeHTTP(rrListActive, reqListActive)
	assert.Equal(t, http.StatusOK, rrListActive.Code)

	var activeList []ChatSession
	require.NoError(t, json.Unmarshal(rrListActive.Body.Bytes(), &activeList))
	assert.Empty(t, activeList)

	// 5. Verify GET /api/sessions?archived=true DOES return the archived session
	reqListArchived := httptest.NewRequest(http.MethodGet, "/api/sessions?archived=true", nil)
	rrListArchived := httptest.NewRecorder()
	server.ServeHTTP(rrListArchived, reqListArchived)
	assert.Equal(t, http.StatusOK, rrListArchived.Code)

	var archivedList []ChatSession
	require.NoError(t, json.Unmarshal(rrListArchived.Body.Bytes(), &archivedList))
	require.Len(t, archivedList, 1)
	assert.Equal(t, chatID, archivedList[0].ChatID)
	assert.True(t, archivedList[0].IsArchived)
	assert.True(t, archivedList[0].IsWaitingForUser)
	require.NotNil(t, archivedList[0].CreatedAt)
	require.NotNil(t, archivedList[0].UpdatedAt)
}

func TestSessionHandler_GetSessionsLimit(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	conf := &config.Config{Host: "http://localhost:8080"}
	server := &Server{conf: conf, repo: repo}
	server.mux = server.buildMuxLocked()

	now := time.Now()
	for i := 1; i <= 25; i++ {
		require.NoError(t, repo.SaveSession(&dbmodels.Session{
			ChatID:     fmt.Sprintf("limit-chat-%02d", i),
			Title:      fmt.Sprintf("Limit Chat %02d", i),
			IsArchived: false,
			UpdatedAt:  now.Add(time.Duration(i) * time.Minute),
		}))
	}

	tests := []struct {
		name          string
		queryURL      string
		expectedCount int
		firstChatID   string
		lastChatID    string
	}{
		{
			name:          "Default limit returns all 25",
			queryURL:      "/api/sessions",
			expectedCount: 25,
			firstChatID:   "limit-chat-25",
			lastChatID:    "limit-chat-01",
		},
		{
			name:          "Custom limit=5 returns 5 newest sessions",
			queryURL:      "/api/sessions?limit=5",
			expectedCount: 5,
			firstChatID:   "limit-chat-25",
			lastChatID:    "limit-chat-21",
		},
		{
			name:          "Invalid limit string falls back to default 500",
			queryURL:      "/api/sessions?limit=invalid",
			expectedCount: 25,
			firstChatID:   "limit-chat-25",
			lastChatID:    "limit-chat-01",
		},
		{
			name:          "Negative limit falls back to default 500",
			queryURL:      "/api/sessions?limit=-10",
			expectedCount: 25,
			firstChatID:   "limit-chat-25",
			lastChatID:    "limit-chat-01",
		},
		{
			name:          "Large limit clamped to 1000",
			queryURL:      "/api/sessions?limit=5000",
			expectedCount: 25,
			firstChatID:   "limit-chat-25",
			lastChatID:    "limit-chat-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.queryURL, nil)
			rr := httptest.NewRecorder()
			server.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusOK, rr.Code)

			var sessions []ChatSession
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &sessions))
			assert.Len(t, sessions, tt.expectedCount)
			if tt.expectedCount > 0 {
				assert.Equal(t, tt.firstChatID, sessions[0].ChatID)
				assert.Equal(t, tt.lastChatID, sessions[len(sessions)-1].ChatID)
			}
		})
	}
}
