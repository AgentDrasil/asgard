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

	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
)

const askUserReplyTestYAML = `
name: ask-reply-loop
tmp_dir: "tmp/${session_id}"
nodes:
  - id: plan_approval
    type: human
    prompt: "please approve the plan"
    output_file: "user_feedback.md"
  - id: final
    type: command
    depends:
      - node: plan_approval
    command: "cat ${tmp_dir}/user_feedback.md > ${tmp_dir}/final.txt"
`

func newAskReplyTestServer(t *testing.T) (*Server, *workflowRunStore, string) {
	t.Helper()
	testDB := db.NewDBForTest(t)
	// Pin the in-memory sqlite DB to a single pooled connection so the
	// background resume goroutine always sees the migrated tables.
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

	s := &Server{repo: repo, workflowEngine: engine}
	engine.SetHumanSuspender(s.suspendWorkflowHuman)

	runDir := t.TempDir()
	return s, store, runDir
}

func seedWaitingRun(t *testing.T, store *workflowRunStore, chatID, runID, runDir string) {
	t.Helper()
	require.NoError(t, store.MarkWaitingHuman(&workflow.RunSnapshot{
		RunID:              runID,
		SessionID:          chatID,
		Status:             workflow.PersistStatusWaitingHuman,
		DAGSpec:            askUserReplyTestYAML,
		RunDir:             runDir,
		NodeStates:         map[string]workflow.PersistedNodeState{},
		SuspendedNodeID:    "plan_approval",
		SuspendedMessageID: workflow.HumanMessageID(runID, "plan_approval"),
	}))
}

func postAskUserReply(t *testing.T, s *Server, chatID, messageID, reply string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(AskUserReplyRequest{
		ChatID:    chatID,
		MessageID: messageID,
		ReplyText: reply,
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/ask-user/reply", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAskUserReply(rec, req)
	return rec
}

func TestAskUserReplyResumesWorkflowRun(t *testing.T) {
	s, store, runDir := newAskReplyTestServer(t)
	chatID := "chat-wf-resume"

	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))
	require.NoError(t, s.repo.AppendMessage(chatID, dbmodels.ChatMessage{
		ID: "wf-run1-plan_approval", Role: "ask_user", Content: "please approve the plan",
	}))

	seedWaitingRun(t, store, chatID, "run1", runDir)

	rec := postAskUserReply(t, s, chatID, "wf-run1-plan_approval", "Approved")
	assert.Equal(t, http.StatusOK, rec.Code)

	// The run is re-driven asynchronously; wait for terminal status.
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := store.GetRun("run1")
		require.NoError(t, err)
		if run != nil && run.Status == workflow.PersistStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("workflow run did not complete; status=%v", run)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The reply landed in the artifact and the ask_user message was marked.
	feedback, err := os.ReadFile(filepath.Join(runDir, "tmp", chatID, "user_feedback.md"))
	require.NoError(t, err)
	assert.Equal(t, "Approved", string(feedback))

	session, err := s.repo.GetSession(chatID)
	require.NoError(t, err)
	require.Len(t, session.Messages, 2)
	assert.True(t, session.Messages[0].Replied)
	assert.Equal(t, "Approved", session.Messages[0].ReplyText)
	// The re-driven run's completion summary is persisted for the transcript.
	assert.Equal(t, "assistant", session.Messages[1].Role)
	assert.Contains(t, session.Messages[1].Content, "COMPLETED")
}

func TestAskUserReplyMismatchedMessageIDDoesNotResume(t *testing.T) {
	s, store, runDir := newAskReplyTestServer(t)
	chatID := "chat-wf-mismatch"

	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID}))
	seedWaitingRun(t, store, chatID, "run2", runDir)

	rec := postAskUserReply(t, s, chatID, "ask-some-other-message", "Approved")
	assert.Equal(t, http.StatusOK, rec.Code)

	time.Sleep(200 * time.Millisecond)

	run, err := store.GetRun("run2")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, workflow.PersistStatusWaitingHuman, run.Status)

	_, err = os.Stat(filepath.Join(runDir, "tmp", chatID, "user_feedback.md"))
	assert.True(t, os.IsNotExist(err), "artifact must not be written on mismatched reply")
}

func TestSuspendWorkflowHumanRegistersArtifacts(t *testing.T) {
	s, _, _ := newAskReplyTestServer(t)
	chatID := "chat-wf-artifacts"
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))

	err := s.suspendWorkflowHuman(workflow.SuspendRequest{
		RunID:     "run3",
		SessionID: chatID,
		NodeID:    "plan_approval",
		MessageID: "wf-run3-plan_approval",
		Prompt:    "review /tmp/plan/plan.md",
		Artifacts: []string{"/tmp/plan/plan.md", "/tmp/plan/review_feedback.md"},
	})
	require.NoError(t, err)

	session, err := s.repo.GetSession(chatID)
	require.NoError(t, err)

	// Artifacts are registered on the session for the artifact viewer.
	assert.Equal(t, dbmodels.Artifacts{"/tmp/plan/plan.md", "/tmp/plan/review_feedback.md"}, session.Artifacts)

	// The ask_user message carries the same references for the frontend card.
	require.Len(t, session.Messages, 1)
	msg := session.Messages[0]
	assert.Equal(t, "ask_user", msg.Role)
	assert.Equal(t, "wf-run3-plan_approval", msg.ID)
	assert.Equal(t, []string{"/tmp/plan/plan.md", "/tmp/plan/review_feedback.md"}, msg.ArtifactFiles)
}

func TestHandleWorkflowEventNodeStatusUpdate(t *testing.T) {
	s, _, _ := newAskReplyTestServer(t)
	chatID := "chat-wf-status-update"
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))

	s.handleWorkflowEvent(chatID, workflow.WorkflowEvent{
		Type:      workflow.EventNodeStatusUpdate,
		NodeID:    "intend_agent",
		NodeType:  workflow.NodeTypeAgent,
		AgentName: "Intent Analyst",
		Status:    workflow.StatusRunning,
		Message:   "Writing requirements to /tmp/intend.md",
		EntryType: "tool_call",
		Metadata: map[string]any{
			"step_index":   1,
			"target_files": []string{"/tmp/intend.md"},
		},
		Artifacts: []string{"/tmp/intend.md"},
	})

	session, err := s.repo.GetSession(chatID)
	require.NoError(t, err)
	assert.Equal(t, dbmodels.Artifacts{"/tmp/intend.md"}, session.Artifacts)

	require.Len(t, session.Messages, 1)
	msg := session.Messages[0]
	assert.Equal(t, "wf-step-intend_agent-1", msg.ID)
	assert.Equal(t, "tool_call", msg.Role)
	assert.Equal(t, "Intent Analyst", msg.AgentName)
	assert.Equal(t, 1, msg.StepIndex)
	assert.Equal(t, []string{"/tmp/intend.md"}, msg.ArtifactFiles)
	assert.Equal(t, "Writing requirements to /tmp/intend.md", msg.Content)

	// Second step update with different step_index should append a new message, not overwrite
	s.handleWorkflowEvent(chatID, workflow.WorkflowEvent{
		Type:      workflow.EventNodeStatusUpdate,
		NodeID:    "intend_agent",
		NodeType:  workflow.NodeTypeAgent,
		AgentName: "Intent Analyst",
		Status:    workflow.StatusRunning,
		Message:   "Requirements written successfully",
		EntryType: "tool_result",
		Metadata: map[string]any{
			"step_index": 2,
		},
	})

	session, err = s.repo.GetSession(chatID)
	require.NoError(t, err)
	require.Len(t, session.Messages, 2)
	assert.Equal(t, "wf-step-intend_agent-1", session.Messages[0].ID)
	assert.Equal(t, "wf-step-intend_agent-2", session.Messages[1].ID)
	assert.Equal(t, "tool_result", session.Messages[1].Role)
	assert.Equal(t, 2, session.Messages[1].StepIndex)
	assert.Equal(t, "Requirements written successfully", session.Messages[1].Content)
}

func TestHandleWorkflowEventWorkflowSuspended(t *testing.T) {
	s, _, _ := newAskReplyTestServer(t)
	chatID := "chat-wf-suspended-event"
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))

	s.handleWorkflowEvent(chatID, workflow.WorkflowEvent{
		Type:      workflow.EventWorkflowSuspended,
		NodeID:    "plan_approval",
		NodeType:  workflow.NodeTypeHuman,
		AgentName: "Dev Workflow",
		Status:    workflow.NodeStatus(workflow.RunStatusWaitingHuman),
		Message:   "Please review Plan (/tmp/plan/plan.md)",
		MessageID: "wf-run-plan_approval",
		Artifacts: []string{"/tmp/plan/plan.md", "/tmp/plan/todo.yaml"},
	})

	session, err := s.repo.GetSession(chatID)
	require.NoError(t, err)
	assert.Equal(t, dbmodels.Artifacts{"/tmp/plan/plan.md", "/tmp/plan/todo.yaml"}, session.Artifacts)

	require.Len(t, session.Messages, 1)
	msg := session.Messages[0]
	assert.Equal(t, "ask_user", msg.Role)
	assert.Equal(t, "wf-run-plan_approval", msg.ID)
	assert.Equal(t, "Dev Workflow", msg.AgentName)
	assert.Equal(t, []string{"/tmp/plan/plan.md", "/tmp/plan/todo.yaml"}, msg.ArtifactFiles)
	assert.Equal(t, "Please review Plan (/tmp/plan/plan.md)", msg.Content)
}

// ---------------------------------------------------------------------------
// Fan-out DB Persistence & Message Deconfliction Tests (§2.6)
// ---------------------------------------------------------------------------

func TestWorkflowPersist_Fanout_ZeroExtraRunRecordsAndMessageDeconfliction(t *testing.T) {
	testDB := db.NewDBForTest(t)
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)
	store := newWorkflowRunStore(wfRepo)

	childYAML := `
name: fanout-child-persist
nodes:
  - id: step_child
    type: command
    sandbox: false
    command: "echo result-${input}"
`
	childDefn, err := workflow.ParseDefinition([]byte(childYAML))
	require.NoError(t, err)

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	wfRunner := workflow.NewSubWorkflowRunner(func(name string) (*workflow.WorkflowDefinition, error) {
		if name == "fanout-child-persist" {
			return childDefn, nil
		}
		return nil, fmt.Errorf("unknown workflow: %s", name)
	})
	registry.Register(wfRunner)
	engine := workflow.NewEngine(registry)
	wfRunner.SetEngine(engine)
	engine.SetRunStore(store)

	s := &Server{
		repo:           repo,
		workflowEngine: engine,
		eventHub:       NewSessionEventHubWithCapacity(50),
	}
	t.Cleanup(s.eventHub.Close)

	chatID := "chat-fanout-persist"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-persist-agent"}))

	tmpDir := t.TempDir()
	itemsFile := filepath.Join(tmpDir, "items.txt")
	require.NoError(t, os.WriteFile(itemsFile, []byte("item1\nitem2\nitem3"), 0644))

	parentYAML := fmt.Sprintf(`
name: fanout-parent-persist
tmp_dir: "%s"
nodes:
  - id: fanout_node
    type: workflow
    workflow: fanout-child-persist
    fanout:
      items_file: %s
      output_file: output.jsonl
`, tmpDir, itemsFile)

	parentDefn, err := workflow.ParseDefinition([]byte(parentYAML))
	require.NoError(t, err)

	runID := "run-fanout-persist-top"
	res, err := engine.Execute(t.Context(), parentDefn, workflow.RunContext{
		SessionID: chatID,
		RunID:     runID,
		RunDir:    tmpDir,
		TmpDir:    tmpDir,
		Store:     store,
		EmitEvent: func(ev workflow.WorkflowEvent) {
			s.handleWorkflowEvent(chatID, ev)
		},
	})
	require.NoError(t, err)
	assert.Equal(t, workflow.RunStatusCompleted, res.Status)

	// 1. Assert DB level: exactly 1 WorkflowRun record (no child Run records)
	var runs []dbmodels.WorkflowRun
	require.NoError(t, testDB.Where("session_id = ?", chatID).Find(&runs).Error)
	assert.Len(t, runs, 1, "only top-level workflow run should be persisted in DB")
	assert.Equal(t, runID, runs[0].RunID)
	assert.Equal(t, workflow.PersistStatusCompleted, runs[0].Status)

	// 2. Simulate / verify bubbled status update events with item_index & sub_node_id
	// emit multiple bubbled events to test derived msgID deconfliction
	s.handleWorkflowEvent(chatID, workflow.WorkflowEvent{
		Type:      workflow.EventNodeStatusUpdate,
		NodeID:    "fanout_node",
		NodeType:  workflow.NodeTypeWorkflow,
		Status:    workflow.StatusRunning,
		Message:   "Item 1 child started",
		EntryType: "tool_call",
		Metadata: map[string]any{
			"item_index":  1,
			"sub_node_id": "step_child",
			"step_index":  1,
		},
	})
	s.handleWorkflowEvent(chatID, workflow.WorkflowEvent{
		Type:      workflow.EventNodeStatusUpdate,
		NodeID:    "fanout_node",
		NodeType:  workflow.NodeTypeWorkflow,
		Status:    workflow.StatusRunning,
		Message:   "Item 2 child started",
		EntryType: "tool_call",
		Metadata: map[string]any{
			"item_index":  2,
			"sub_node_id": "step_child",
			"step_index":  1,
		},
	})
	// High-frequency fanout_progress should NOT be appended to DB session messages
	s.handleWorkflowEvent(chatID, workflow.WorkflowEvent{
		Type:      workflow.EventNodeStatusUpdate,
		NodeID:    "fanout_node",
		NodeType:  workflow.NodeTypeWorkflow,
		Status:    workflow.StatusRunning,
		Message:   "Item 1 child progress ticker",
		EntryType: "fanout_progress",
		Metadata: map[string]any{
			"item_index":  1,
			"sub_node_id": "step_child",
			"step_index":  1,
		},
	})

	session, err := repo.GetSession(chatID)
	require.NoError(t, err)

	// Check messages in session
	msgIDs := make(map[string]int)
	for _, m := range session.Messages {
		msgIDs[m.ID]++
		assert.NotEqual(t, "fanout_progress", m.Role, "fanout_progress events must not be persisted into DB messages")
	}

	assert.Contains(t, msgIDs, "wf-step-fanout_node-1-step_child-1")
	assert.Contains(t, msgIDs, "wf-step-fanout_node-2-step_child-1")
	assert.Equal(t, 1, msgIDs["wf-step-fanout_node-1-step_child-1"], "no collision/overwriting for item 1")
	assert.Equal(t, 1, msgIDs["wf-step-fanout_node-2-step_child-1"], "no collision/overwriting for item 2")
}
