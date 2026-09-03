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
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

func humanNodeWorkflowYAML(tempDir string) string {
	return fmt.Sprintf(`
name: human-stream
tmp_dir: "%s/tmp/${session_id}"
nodes:
  - id: entry_question
    type: human
    prompt: "please approve the plan"
  - id: final
    type: command
    depends:
      - node: entry_question
    command: "echo done > ${tmp_dir}/final.txt"
`, tempDir)
}

func TestWorkflowHumanNodeEmitsAskUserViaEvents(t *testing.T) {
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

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	engine := workflow.NewEngine(registry)
	engine.SetRunStore(newWorkflowRunStore(wfRepo))

	hub := NewSessionEventHubWithCapacity(20)
	t.Cleanup(hub.Close)

	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte(humanNodeWorkflowYAML(tempDir)), 0644))

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
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
		agents:         []*agentspec.Agent{agent},
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
	tempDir := t.TempDir()
	repo := dbmodels.NewSessionRepository(testDB)
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	engine := workflow.NewEngine(registry)
	engine.SetRunStore(newWorkflowRunStore(wfRepo))

	hub := NewSessionEventHubWithCapacity(20)
	t.Cleanup(hub.Close)

	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte(humanNodeWorkflowYAML(tempDir)), 0644))

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
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
		agents:         []*agentspec.Agent{agent},
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

// ---------------------------------------------------------------------------
// Fan-out SSE Event Metadata Propagation Tests (§2.7)
// ---------------------------------------------------------------------------

func TestWorkflowEvents_Fanout_MetadataPropagated(t *testing.T) {
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

	childYAML := `
name: child-event-wf
nodes:
  - id: child_cmd
    type: command
    sandbox: false
    command: "echo child-${input}"
`
	childDefn, err := workflowspec.ParseDefinition([]byte(childYAML))
	require.NoError(t, err)

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	wfRunner := workflow.NewSubWorkflowRunner(func(name string) (*workflowspec.WorkflowDefinition, error) {
		if name == "child-event-wf" {
			return childDefn, nil
		}
		return nil, fmt.Errorf("unknown workflow: %s", name)
	})
	registry.Register(wfRunner)

	engine := workflow.NewEngine(registry)
	wfRunner.SetEngine(engine)
	engine.SetRunStore(newWorkflowRunStore(wfRepo))

	hub := NewSessionEventHubWithCapacity(50)
	t.Cleanup(hub.Close)

	itemsFile := filepath.Join(tempDir, "items.txt")
	require.NoError(t, os.WriteFile(itemsFile, []byte("item1\nitem2"), 0644))

	parentYAML := fmt.Sprintf(`
name: parent-event-wf
tmp_dir: "%s"
nodes:
  - id: fanout_task
    type: workflow
    workflow: child-event-wf
    fanout:
      items_file: "%s"
      output_file: "output.jsonl"
`, tempDir, itemsFile)

	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte(parentYAML), 0644))

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "wf-fanout-event-agent",
			Name: "Workflow Fanout Event Agent",
			Type: "workflow",
		},
		WorkflowPath: wfFile,
	}

	s := &Server{
		conf:           &config.Config{},
		repo:           repo,
		eventHub:       hub,
		workflowEngine: engine,
		agents:         []*agentspec.Agent{agent},
	}
	s.mux = s.buildMuxLocked()

	chatID := "chat-wf-fanout-events"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-fanout-event-agent"}))

	// Subscribe to SSE hub
	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	// Trigger workflow message asynchronously
	triggerPayload := map[string]any{
		"prompt": "start fanout",
		"chatId": chatID,
	}
	raw, err := json.Marshal(triggerPayload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/wf-fanout-event-agent/message", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusAccepted, rec.Code)

	// Collect SSE events and assert metadata
	var fanoutProgressEvents []SessionEvent
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-subCh:
			if ev.Type == "message" && ev.Message != nil {
				// Check for bubbled fanout progress messages
				if strings.Contains(ev.Message.ID, "fanout_task") && strings.Contains(ev.Message.ID, "child_cmd") {
					fanoutProgressEvents = append(fanoutProgressEvents, ev)
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
		if len(fanoutProgressEvents) >= 2 {
			break
		}
	}

	waitForRunStatus(t, testDB, chatID, workflow.PersistStatusCompleted)

	// Verify we received bubbled fanout progress events with item indices in the Message IDs
	require.NotEmpty(t, fanoutProgressEvents, "expected at least one fanout progress event delivered via SSE")
	var foundItem1, foundItem2 bool
	for _, ev := range fanoutProgressEvents {
		if strings.Contains(ev.Message.ID, "fanout_task-1-child_cmd") {
			foundItem1 = true
		}
		if strings.Contains(ev.Message.ID, "fanout_task-2-child_cmd") {
			foundItem2 = true
		}
	}
	assert.True(t, foundItem1, "expected item 1 fanout progress event")
	assert.True(t, foundItem2, "expected item 2 fanout progress event")
}
