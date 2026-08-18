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
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/db"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
	"github.com/AgentDrasil/asgard/lib/workflow"
)

const humanNodeWorkflowYAML = `
name: human-stream
tmp_dir: "tmp/${session_id}"
nodes:
  - id: entry_question
    type: human
    prompt: "please approve the plan"
  - id: final
    type: command
    depends:
      - node: entry_question
    command: "echo done > ${tmp_dir}/final.txt"
`

func TestWorkflowHumanNodeEmitsAskUserViaEvents(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	engine := workflow.NewEngine(registry)
	engine.SetRunStore(newWorkflowRunStore(wfRepo))

	hub := NewSessionEventHubWithCapacity(20)
	t.Cleanup(hub.Close)

	tempDir := t.TempDir()
	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte(humanNodeWorkflowYAML), 0644))

	agent := &agents.Agent{
		Config: agents.AgentConfig{
			ID:   "wf-stream-agent",
			Name: "Workflow Stream Agent",
			Type: "workflow",
		},
		WorkflowPath: wfFile,
	}

	s := &Server{
		conf:           &config.Config{},
		repo:           repo,
		eventHub:       hub,
		workflowEngine: engine,
		agents:         []*agents.Agent{agent},
	}
	s.mux = s.buildMuxLocked()
	engine.SetHumanSuspender(s.suspendWorkflowHuman)

	chatID := "chat-wf-events-test"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-stream-agent"}))

	// Subscribe to SSE event hub
	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	// Trigger workflow message asynchronously
	triggerPayload := map[string]any{
		"prompt": "start the flow",
		"chatId": chatID,
	}
	raw, err := json.Marshal(triggerPayload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/wf-stream-agent/message", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusAccepted, rec.Code)

	// Collect events from event hub
	var askMessageID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && askMessageID == "" {
		select {
		case ev := <-subCh:
			if ev.Type == "message" && ev.Message != nil && ev.Message.Role == "ask_user" {
				askMessageID = ev.Message.ID
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	require.NotEmpty(t, askMessageID, "did not receive ask_user event from EventHub")

	// The run must stay WAITING_HUMAN in DB so user reply can resume it.
	waitForRunStatus(t, testDB, chatID, workflow.PersistStatusWaitingHuman)

	// User replies through ask-user reply endpoint
	replyRec := postAskUserReply(t, s, chatID, askMessageID, "Approved")
	assert.Equal(t, http.StatusOK, replyRec.Code)

	// Resumed run must complete
	waitForRunStatus(t, testDB, chatID, workflow.PersistStatusCompleted)

	// Verify ask_user message marked replied
	require.Eventually(t, func() bool {
		session, err := repo.GetSession(chatID)
		require.NoError(t, err)
		for _, m := range session.Messages {
			if m.Role == "ask_user" && m.ID == askMessageID && m.Replied {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond, "ask_user message not marked replied in DB")
}

func TestWorkflowHumanNodeSyncWaitReturnsImmediately(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	engine := workflow.NewEngine(registry)
	engine.SetRunStore(newWorkflowRunStore(wfRepo))

	hub := NewSessionEventHubWithCapacity(20)
	t.Cleanup(hub.Close)

	tempDir := t.TempDir()
	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte(humanNodeWorkflowYAML), 0644))

	agent := &agents.Agent{
		Config: agents.AgentConfig{
			ID:   "wf-stream-agent",
			Name: "Workflow Stream Agent",
			Type: "workflow",
		},
		WorkflowPath: wfFile,
	}

	s := &Server{
		conf:           &config.Config{},
		repo:           repo,
		eventHub:       hub,
		workflowEngine: engine,
		agents:         []*agents.Agent{agent},
	}
	s.mux = s.buildMuxLocked()
	engine.SetHumanSuspender(s.suspendWorkflowHuman)

	chatID := "chat-wf-sync-wait-human"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-stream-agent"}))

	triggerPayload := map[string]any{
		"prompt": "start flow wait mode",
		"chatId": chatID,
		"wait":   true,
	}
	raw, err := json.Marshal(triggerPayload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/wf-stream-agent/message?wait=true", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		s.ServeHTTP(rec, req)
	}()

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out: sync wait mode blocked instead of returning waiting_human immediately")
	}

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "waiting_human", resp["status"])
	assert.Equal(t, "", resp["output"])
	assert.Equal(t, chatID, resp["chatId"])

	// Check that agent is not locked in running state in DB
	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.False(t, sess.IsRunning())

	// The run must be in WAITING_HUMAN in DB
	waitForRunStatus(t, testDB, chatID, workflow.PersistStatusWaitingHuman)
}

func waitForRunStatus(t *testing.T, gdb *gorm.DB, chatID, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var runs []dbmodels.WorkflowRun
		if err := gdb.Where("session_id = ?", chatID).Order("updated_at DESC").Find(&runs).Error; err != nil {
			t.Fatalf("querying workflow runs: %v", err)
		}
		for _, run := range runs {
			if run.Status == want {
				return
			}
		}
		if time.Now().After(deadline) {
			statuses := make([]string, 0, len(runs))
			for _, run := range runs {
				statuses = append(statuses, fmt.Sprintf("%s=%s", run.RunID, run.Status))
			}
			t.Fatalf("workflow run did not reach status %s; runs: %v", want, statuses)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
