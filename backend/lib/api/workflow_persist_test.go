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
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
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

	// Isolate HOME so engine session-dir fallbacks land in a test-owned directory
	t.Setenv("HOME", t.TempDir())

	testDB := db.NewDBForTest(t)
	// Pin the in-memory sqlite DB to a single pooled connection so the
	// background resume goroutine always sees the migrated tables.
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
		NodeType:  workflowspec.NodeTypeAgent,
		AgentName: "Intent Analyst",
		Status:    workflowspec.StatusRunning,
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
		NodeType:  workflowspec.NodeTypeAgent,
		AgentName: "Intent Analyst",
		Status:    workflowspec.StatusRunning,
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
		NodeType:  workflowspec.NodeTypeHuman,
		AgentName: "Dev Workflow",
		Status:    workflowspec.NodeStatus(workflow.RunStatusWaitingHuman),
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
	// Isolate HOME so engine session-dir fallbacks land in a test-owned directory
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

	childYAML := `
name: fanout-child-persist
nodes:
  - id: step_child
    type: command
    sandbox: false
    command: "echo result-${input}"
`
	childDefn, err := workflowspec.ParseDefinition([]byte(childYAML))
	require.NoError(t, err)

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	wfRunner := workflow.NewSubWorkflowRunner(func(name string) (*workflowspec.WorkflowDefinition, error) {
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

	parentDefn, err := workflowspec.ParseDefinition([]byte(parentYAML))
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
		NodeType:  workflowspec.NodeTypeWorkflow,
		Status:    workflowspec.StatusRunning,
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
		NodeType:  workflowspec.NodeTypeWorkflow,
		Status:    workflowspec.StatusRunning,
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
		NodeType:  workflowspec.NodeTypeWorkflow,
		Status:    workflowspec.StatusRunning,
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

func TestSuspendedNodeInfoTagsMatchAcrossPackages(t *testing.T) {
	dbRaw, err := json.Marshal(map[string]dbmodels.SuspendedNodeInfo{
		"node_a": {MessageID: "wf-r1-node_a", Iteration: 2},
	})
	require.NoError(t, err)

	wfRaw, err := json.Marshal(map[string]workflow.SuspendedNodeInfo{
		"node_a": {MessageID: "wf-r1-node_a", Iteration: 2},
	})
	require.NoError(t, err)

	assert.JSONEq(t, string(dbRaw), string(wfRaw))
}

func TestAskUserReply_MultipleConcurrentWaitingRunsInSession(t *testing.T) {
	s, store, runDir := newAskReplyTestServer(t)
	chatID := "chat-multi-runs"

	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))
	msg1 := workflow.HumanMessageID("run1", "plan_approval")
	msg2 := workflow.HumanMessageID("run2", "plan_approval")

	require.NoError(t, s.repo.AppendMessage(chatID, dbmodels.ChatMessage{
		ID: msg1, Role: "ask_user", Content: "please approve plan 1",
	}))
	require.NoError(t, s.repo.AppendMessage(chatID, dbmodels.ChatMessage{
		ID: msg2, Role: "ask_user", Content: "please approve plan 2",
	}))

	seedWaitingRun(t, store, chatID, "run1", runDir)
	seedWaitingRun(t, store, chatID, "run2", runDir)

	// 1. Reply to run1
	rec1 := postAskUserReply(t, s, chatID, msg1, "Approve 1")
	assert.Equal(t, http.StatusOK, rec1.Code)

	// Wait for run1 to complete
	deadline := time.Now().Add(5 * time.Second)
	for {
		r1, err := store.GetRun("run1")
		require.NoError(t, err)
		if r1 != nil && r1.Status == workflow.PersistStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run1 did not complete; status=%v", r1)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify run2 is still WAITING_HUMAN
	r2, err := store.GetRun("run2")
	require.NoError(t, err)
	require.NotNil(t, r2)
	assert.Equal(t, workflow.PersistStatusWaitingHuman, r2.Status)

	// 2. Reply to run2
	rec2 := postAskUserReply(t, s, chatID, msg2, "Approve 2")
	assert.Equal(t, http.StatusOK, rec2.Code)

	deadline = time.Now().Add(5 * time.Second)
	for {
		r2, err = store.GetRun("run2")
		require.NoError(t, err)
		if r2 != nil && r2.Status == workflow.PersistStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run2 did not complete; status=%v", r2)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAskUserReply_CrossChatSessionHijackDefense(t *testing.T) {
	s, store, runDir := newAskReplyTestServer(t)
	chatA := "chat-A"
	chatB := "chat-B"

	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatA, CurrentAgent: "wf-agent"}))
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatB, CurrentAgent: "wf-agent"}))

	msgA := workflow.HumanMessageID("run-A", "plan_approval")
	require.NoError(t, s.repo.AppendMessage(chatA, dbmodels.ChatMessage{
		ID: msgA, Role: "ask_user", Content: "approve A",
	}))

	seedWaitingRun(t, store, chatA, "run-A", runDir)

	// Attempt to resume run-A from chatB
	rec := postAskUserReply(t, s, chatB, msgA, "Malicious Reply")
	assert.Equal(t, http.StatusOK, rec.Code)

	time.Sleep(200 * time.Millisecond)

	// Verify run-A is still waiting and was not hijacked
	rA, err := store.GetRun("run-A")
	require.NoError(t, err)
	require.NotNil(t, rA)
	assert.Equal(t, workflow.PersistStatusWaitingHuman, rA.Status)

	// Check that chatB received no resume events or artifact modifications
	_, err = os.Stat(filepath.Join(runDir, "tmp", chatA, "user_feedback.md"))
	assert.True(t, os.IsNotExist(err))

	sessB, err := s.repo.GetSession(chatB)
	require.NoError(t, err)
	require.NotNil(t, sessB)
	assert.Empty(t, sessB.Messages)
}

func TestAskUserReply_EmptyMessageID_SingleRunFallback(t *testing.T) {
	s, store, runDir := newAskReplyTestServer(t)
	chatID := "chat-single-fallback"

	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))
	seedWaitingRun(t, store, chatID, "run-single", runDir)

	// Send reply with empty message_id
	rec := postAskUserReply(t, s, chatID, "", "Approved Single")
	assert.Equal(t, http.StatusOK, rec.Code)

	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := store.GetRun("run-single")
		require.NoError(t, err)
		if run != nil && run.Status == workflow.PersistStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run-single did not complete; status=%v", run)
		}
		time.Sleep(10 * time.Millisecond)
	}

	feedback, err := os.ReadFile(filepath.Join(runDir, "tmp", chatID, "user_feedback.md"))
	require.NoError(t, err)
	assert.Equal(t, "Approved Single", string(feedback))
}

func TestAskUserReply_EmptyMessageID_SingleRunMultipleNodes_GracefulNoop(t *testing.T) {
	s, store, runDir := newAskReplyTestServer(t)
	chatID := "chat-single-run-multi-nodes"

	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID}))

	// Seed run with 2 suspended nodes
	require.NoError(t, store.MarkWaitingHuman(&workflow.RunSnapshot{
		RunID:     "run-multi-nodes",
		SessionID: chatID,
		Status:    workflow.PersistStatusWaitingHuman,
		DAGSpec:   askUserReplyTestYAML,
		RunDir:    runDir,
		SuspendedNodes: map[string]workflow.SuspendedNodeInfo{
			"node1": {MessageID: "wf-run-multi-nodes-node1"},
			"node2": {MessageID: "wf-run-multi-nodes-node2"},
		},
	}))

	// Send reply with empty message_id -> totalWaiting == 2 -> graceful noop
	rec := postAskUserReply(t, s, chatID, "", "Should Noop")
	assert.Equal(t, http.StatusOK, rec.Code)

	time.Sleep(200 * time.Millisecond)

	run, err := store.GetRun("run-multi-nodes")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, workflow.PersistStatusWaitingHuman, run.Status)
}

func TestAskUserReply_EmptyMessageID_MultipleRuns_GracefulNoop(t *testing.T) {
	s, store, runDir := newAskReplyTestServer(t)
	chatID := "chat-multi-runs-noop"

	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID}))

	seedWaitingRun(t, store, chatID, "run-a", runDir)
	seedWaitingRun(t, store, chatID, "run-b", runDir)

	// Send reply with empty message_id -> totalWaiting == 2 -> graceful noop
	rec := postAskUserReply(t, s, chatID, "", "Should Noop")
	assert.Equal(t, http.StatusOK, rec.Code)

	time.Sleep(200 * time.Millisecond)

	ra, err := store.GetRun("run-a")
	require.NoError(t, err)
	assert.Equal(t, workflow.PersistStatusWaitingHuman, ra.Status)

	rb, err := store.GetRun("run-b")
	require.NoError(t, err)
	assert.Equal(t, workflow.PersistStatusWaitingHuman, rb.Status)
}

func TestAskUserReply_UnknownMessageID_SafeNoop(t *testing.T) {
	s, store, runDir := newAskReplyTestServer(t)
	chatID := "chat-unknown-msg"

	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID}))
	seedWaitingRun(t, store, chatID, "run-active", runDir)

	// Send reply with non-existent message_id
	rec := postAskUserReply(t, s, chatID, "wf-nonexistent-message", "Should Safe Noop")
	assert.Equal(t, http.StatusOK, rec.Code)

	time.Sleep(200 * time.Millisecond)

	run, err := store.GetRun("run-active")
	require.NoError(t, err)
	assert.Equal(t, workflow.PersistStatusWaitingHuman, run.Status)
}

func TestAskUserReply_StaleRunPollutionDefense(t *testing.T) {
	s, store, runDir := newAskReplyTestServer(t)
	chatID := "chat-stale-defense"

	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID}))

	// Seed one COMPLETED run (stale record) and one WAITING_HUMAN run
	require.NoError(t, store.StartRun(&workflow.RunSnapshot{
		RunID:     "run-completed",
		SessionID: chatID,
	}))
	require.NoError(t, store.SettleRun("run-completed", workflow.PersistStatusCompleted, map[string]workflow.PersistedNodeState{}))

	seedWaitingRun(t, store, chatID, "run-live", runDir)

	// Empty message_id reply should ignore completed run and fall back to run-live (totalWaiting == 1)
	rec := postAskUserReply(t, s, chatID, "", "Approve Live")
	assert.Equal(t, http.StatusOK, rec.Code)

	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := store.GetRun("run-live")
		require.NoError(t, err)
		if run != nil && run.Status == workflow.PersistStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run-live did not complete; status=%v", run)
		}
		time.Sleep(10 * time.Millisecond)
	}

	feedback, err := os.ReadFile(filepath.Join(runDir, "tmp", chatID, "user_feedback.md"))
	require.NoError(t, err)
	assert.Equal(t, "Approve Live", string(feedback))
}

func TestWorkflowPersist_LiveWaiterResume_NoPrematureDone(t *testing.T) {
	// Isolate HOME so engine session-dir fallbacks land in a test-owned directory
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

	hub := NewSessionEventHubWithCapacity(50)
	t.Cleanup(hub.Close)

	wfFile := filepath.Join(tempDir, "workflow.yaml")
	slowWorkflowYAML := fmt.Sprintf(`
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
    command: "sleep 0.2 && echo done > ${tmp_dir}/final.txt"
`, tempDir)
	require.NoError(t, os.WriteFile(wfFile, []byte(slowWorkflowYAML), 0644))

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "wf-live-agent",
			Name: "Workflow Live Agent",
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
	engine.SetHumanSuspender(s.suspendWorkflowHuman)

	chatID := "chat-wf-live-waiter-done-test"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-live-agent"}))

	subCh, doneCh, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	triggerPayload := map[string]any{
		"prompt": "start flow",
		"chatId": chatID,
	}
	raw, err := json.Marshal(triggerPayload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/wf-live-agent/message", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusAccepted, rec.Code)

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

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.False(t, s.isSessionRunning(sess), "Session must not be running while waiting for human")

	// Drain any events from the pre-resume/suspension phase before starting resume collection
	drain := true
	for drain {
		select {
		case <-subCh:
		default:
			drain = false
		}
	}

	var statusEvents []SessionEvent
	var doneEvents []SessionEvent
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for {
			select {
			case ev := <-subCh:
				switch ev.Type {
				case "status":
					statusEvents = append(statusEvents, ev)
				case "done":
					doneEvents = append(doneEvents, ev)
				}
			case <-doneCh:
				for {
					select {
					case ev := <-subCh:
						switch ev.Type {
						case "status":
							statusEvents = append(statusEvents, ev)
						case "done":
							doneEvents = append(doneEvents, ev)
						}
					default:
						return
					}
				}
			}
		}
	}()

	replyRec := postAskUserReply(t, s, chatID, askMessageID, "Approved")
	assert.Equal(t, http.StatusOK, replyRec.Code)

	// Sample running status while workflow is running the final node
	time.Sleep(50 * time.Millisecond)
	sessRunning, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.True(t, s.isSessionRunning(sessRunning), "Session must be running during resumed execution")

	// Wait until workflow run completes
	waitForRunStatus(t, testDB, chatID, workflow.PersistStatusCompleted)

	// Give a short grace period for all events to drain
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-eventsDone

	// Assert exactly 1 done event was received overall
	assert.Len(t, doneEvents, 1, "Must receive exactly one done event upon full completion")

	// Verify status event sequence after resume: first isRunning: true, last isRunning: false
	require.NotEmpty(t, statusEvents)
	assert.Equal(t, true, statusEvents[0].Payload["isRunning"], "First status event after resume must be running: true")
	assert.Equal(t, false, statusEvents[len(statusEvents)-1].Payload["isRunning"], "Last status event after resume must be running: false")

	sessAfter, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.False(t, s.isSessionRunning(sessAfter))
}

func TestWorkflowPersist_RedriveResume_StatusSync(t *testing.T) {
	// Isolate HOME so engine session-dir fallbacks land in a test-owned directory
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

	hub := NewSessionEventHubWithCapacity(50)
	t.Cleanup(hub.Close)

	runDir := t.TempDir()

	s := &Server{
		conf:            &config.Config{},
		repo:            repo,
		eventHub:        hub,
		workflowEngine:  engine,
		workflowRunRepo: wfRepo,
	}
	s.mux = s.buildMuxLocked()
	engine.SetHumanSuspender(s.suspendWorkflowHuman)

	chatID := "chat-wf-redrive-sync"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))
	require.NoError(t, repo.AppendMessage(chatID, dbmodels.ChatMessage{
		ID: "wf-run-redrive-plan_approval", Role: "ask_user", Content: "please approve the plan",
	}))

	slowRedriveYAML := fmt.Sprintf(`
name: ask-reply-loop
tmp_dir: "%s/tmp/${session_id}"
nodes:
  - id: plan_approval
    type: human
    prompt: "please approve the plan"
    output_file: "user_feedback.md"
  - id: final
    type: command
    depends:
      - node: plan_approval
    command: "sleep 0.2 && cat ${tmp_dir}/user_feedback.md > ${tmp_dir}/final.txt"
`, runDir)

	require.NoError(t, store.MarkWaitingHuman(&workflow.RunSnapshot{
		RunID:              "run-redrive",
		SessionID:          chatID,
		Status:             workflow.PersistStatusWaitingHuman,
		DAGSpec:            slowRedriveYAML,
		RunDir:             runDir,
		NodeStates:         map[string]workflow.PersistedNodeState{},
		SuspendedNodeID:    "plan_approval",
		SuspendedMessageID: "wf-run-redrive-plan_approval",
	}))

	subCh, doneCh, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	statusEvents := make([]SessionEvent, 0)
	doneSub := make(chan struct{})
	go func() {
		defer close(doneSub)
		for {
			select {
			case ev := <-subCh:
				if ev.Type == "status" {
					statusEvents = append(statusEvents, ev)
				}
			case <-doneCh:
				for {
					select {
					case ev := <-subCh:
						if ev.Type == "status" {
							statusEvents = append(statusEvents, ev)
						}
					default:
						return
					}
				}
			}
		}
	}()

	rec := postAskUserReply(t, s, chatID, "wf-run-redrive-plan_approval", "Approved Redrive")
	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify GET /api/sessions/:id during redrive returns isRunning: true
	time.Sleep(50 * time.Millisecond)
	reqGet := httptest.NewRequest(http.MethodGet, "/api/sessions/"+chatID, nil)
	recGetRunning := httptest.NewRecorder()
	s.ServeHTTP(recGetRunning, reqGet)
	assert.Equal(t, http.StatusOK, recGetRunning.Code)
	var sessRespRunning ChatSession
	require.NoError(t, json.Unmarshal(recGetRunning.Body.Bytes(), &sessRespRunning))
	assert.True(t, sessRespRunning.IsRunning, "GET /api/sessions/:id must return isRunning: true during redrive")

	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := store.GetRun("run-redrive")
		require.NoError(t, err)
		if run != nil && run.Status == workflow.PersistStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run-redrive did not complete; status=%v", run)
		}
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-doneSub

	hasRunningStatus := false
	hasCompletedStatus := false
	for _, ev := range statusEvents {
		if isRunning, ok := ev.Payload["isRunning"].(bool); ok {
			if isRunning {
				hasRunningStatus = true
			} else {
				hasCompletedStatus = true
			}
		}
	}
	assert.True(t, hasRunningStatus, "Must broadcast isRunning: true status event during redrive")
	assert.True(t, hasCompletedStatus, "Must broadcast isRunning: false status event after redrive")

	feedback, err := os.ReadFile(filepath.Join(runDir, "tmp", chatID, "user_feedback.md"))
	require.NoError(t, err)
	assert.Equal(t, "Approved Redrive", string(feedback))

	// Verify GET /api/sessions/:id after redrive returns isRunning: false
	recGetCompleted := httptest.NewRecorder()
	s.ServeHTTP(recGetCompleted, reqGet)
	assert.Equal(t, http.StatusOK, recGetCompleted.Code)
	var sessRespCompleted ChatSession
	require.NoError(t, json.Unmarshal(recGetCompleted.Body.Bytes(), &sessRespCompleted))
	assert.False(t, sessRespCompleted.IsRunning, "GET /api/sessions/:id must return isRunning: false after redrive completion")
}

func TestWorkflowPersist_ResumeDuplicateReply_GuardSafety(t *testing.T) {
	// Isolate HOME so engine session-dir fallbacks land in a test-owned directory
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

	hub := NewSessionEventHubWithCapacity(50)
	t.Cleanup(hub.Close)

	wfFile := filepath.Join(tempDir, "workflow.yaml")
	// Command sleeps a bit to stay actively executing
	sleepYAML := fmt.Sprintf(`
name: sleep-flow
tmp_dir: "%s/tmp/${session_id}"
nodes:
  - id: entry
    type: human
    prompt: "please approve"
  - id: slow_node
    type: command
    depends:
      - node: entry
    command: "sleep 0.3 && echo done > ${tmp_dir}/final.txt"
`, tempDir)
	require.NoError(t, os.WriteFile(wfFile, []byte(sleepYAML), 0644))

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "wf-guard-agent",
			Name: "Workflow Guard Agent",
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
	engine.SetHumanSuspender(s.suspendWorkflowHuman)

	chatID := "chat-wf-dup-guard"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-guard-agent"}))

	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	triggerPayload := map[string]any{
		"prompt": "start flow",
		"chatId": chatID,
	}
	raw, err := json.Marshal(triggerPayload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/wf-guard-agent/message", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusAccepted, rec.Code)

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

	// Post the first valid reply
	replyRec1 := postAskUserReply(t, s, chatID, askMessageID, "First Reply")
	assert.Equal(t, http.StatusOK, replyRec1.Code)

	// Immediately post a duplicate reply while engine is executing slow_node
	replyRec2 := postAskUserReply(t, s, chatID, askMessageID, "Duplicate Reply")
	assert.Equal(t, http.StatusOK, replyRec2.Code)

	// Wait for completion
	waitForRunStatus(t, testDB, chatID, workflow.PersistStatusCompleted)

	// Ensure engine finishes cleanly
	time.Sleep(100 * time.Millisecond)
	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.False(t, s.isSessionRunning(sess))
}

func TestWorkflowPersist_ResumeError_RollbackSafety(t *testing.T) {
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

	hub := NewSessionEventHubWithCapacity(50)
	t.Cleanup(hub.Close)

	runDir := t.TempDir()

	s := &Server{
		conf:            &config.Config{},
		repo:            repo,
		eventHub:        hub,
		workflowEngine:  engine,
		workflowRunRepo: wfRepo,
	}
	s.mux = s.buildMuxLocked()
	engine.SetHumanSuspender(s.suspendWorkflowHuman)

	chatID := "chat-rollback-test"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))
	require.NoError(t, repo.AppendMessage(chatID, dbmodels.ChatMessage{
		ID: "wf-corrupted-msg-id", Role: "ask_user", Content: "please approve",
	}))

	// Seed a waiting run with invalid/corrupted DAGSpec so ResumeByMessageID will match the snapshot but fail during ParseDefinition
	require.NoError(t, store.MarkWaitingHuman(&workflow.RunSnapshot{
		RunID:              "run-corrupted",
		SessionID:          chatID,
		Status:             workflow.PersistStatusWaitingHuman,
		DAGSpec:            "invalid: yaml: : : [",
		RunDir:             runDir,
		NodeStates:         map[string]workflow.PersistedNodeState{},
		SuspendedNodeID:    "plan_approval",
		SuspendedMessageID: "wf-corrupted-msg-id",
	}))

	subCh, doneCh, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	statusEvents := make([]SessionEvent, 0)
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for {
			select {
			case ev := <-subCh:
				if ev.Type == "status" {
					statusEvents = append(statusEvents, ev)
				}
			case <-doneCh:
				for {
					select {
					case ev := <-subCh:
						if ev.Type == "status" {
							statusEvents = append(statusEvents, ev)
						}
					default:
						return
					}
				}
			}
		}
	}()

	rec := postAskUserReply(t, s, chatID, "wf-corrupted-msg-id", "Some Reply")
	assert.Equal(t, http.StatusOK, rec.Code)

	time.Sleep(150 * time.Millisecond)
	cancel()
	<-eventsDone

	// activeExecutions should be clean and session should not be stuck running
	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.False(t, s.isSessionRunning(sess))
	_, inActive := s.activeExecutions.Load(chatID)
	assert.False(t, inActive, "activeExecutions must be deleted on error/ignored resume")
	assert.False(t, sess.IsRunning(), "session must be in completed state after rollback")

	// Verify status transition: status:true followed by status:false on rollback
	hasStatusTrue := false
	hasStatusFalse := false
	for _, ev := range statusEvents {
		if isRunning, ok := ev.Payload["isRunning"].(bool); ok {
			if isRunning {
				hasStatusTrue = true
			} else {
				hasStatusFalse = true
			}
		}
	}
	assert.True(t, hasStatusTrue, "Must broadcast isRunning: true when starting resume")
	assert.True(t, hasStatusFalse, "Must broadcast isRunning: false when rolling back on resume error")
}

func TestWorkflowRunPersistence_E2E(t *testing.T) {
	// Isolate HOME so engine session-dir fallbacks land in a test-owned directory
	t.Setenv("HOME", t.TempDir())

	testDB := db.NewDBForTest(t)
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	tempDir := t.TempDir()
	sessionRepo := dbmodels.NewSessionRepository(testDB)
	sessionRepo.SetSessionDirFunc(func(chatID string) string {
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

	s := &Server{repo: sessionRepo, workflowEngine: engine}
	engine.SetHumanSuspender(s.suspendWorkflowHuman)

	chatID := "chat-wf-e2e-persistence"
	runID := "run-wf-e2e-1"
	runDir := t.TempDir()

	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))
	msgID := workflow.HumanMessageID(runID, "plan_approval")
	require.NoError(t, s.repo.AppendMessage(chatID, dbmodels.ChatMessage{
		ID: msgID, Role: "ask_user", Content: "please approve the plan",
	}))

	originalYAML := askUserReplyTestYAML

	// 1. StartRun & MarkWaitingHuman with node states
	require.NoError(t, store.StartRun(&workflow.RunSnapshot{
		RunID:     runID,
		SessionID: chatID,
		Status:    workflow.PersistStatusRunning,
		DAGSpec:   originalYAML,
		RunDir:    runDir,
	}))

	initialStates := map[string]workflow.PersistedNodeState{
		"plan_approval": {
			Status: "SUSPENDED",
			Output: "Need manager review for spec v2",
		},
	}
	require.NoError(t, store.MarkWaitingHuman(&workflow.RunSnapshot{
		RunID:              runID,
		SessionID:          chatID,
		Status:             workflow.PersistStatusWaitingHuman,
		DAGSpec:            originalYAML,
		RunDir:             runDir,
		NodeStates:         initialStates,
		SuspendedNodeID:    "plan_approval",
		SuspendedMessageID: msgID,
		SuspendedNodes: map[string]workflow.SuspendedNodeInfo{
			"plan_approval": {MessageID: msgID},
		},
	}))

	// 2. Direct raw DB inspection to assert DB records pruned content and records paths
	var rawRow struct {
		RunID       string `gorm:"column:run_id"`
		DAGSpecPath string `gorm:"column:dag_spec_path"`
		InputPath   string `gorm:"column:input_path"`
		NodeStates  string `gorm:"column:node_states"`
	}
	err = testDB.Table("workflow_runs").Where("run_id = ?", runID).Scan(&rawRow).Error
	require.NoError(t, err)
	assert.NotEmpty(t, rawRow.DAGSpecPath, "DAGSpecPath must be recorded in DB")
	assert.FileExists(t, rawRow.DAGSpecPath, "DAGSpec offloaded file must exist on disk")

	decodedStates, err := dbmodels.DecodeNodeStates(rawRow.NodeStates)
	require.NoError(t, err)
	assert.Empty(t, decodedStates["plan_approval"].Output, "DB JSON NodeState Output must be pruned")
	assert.NotEmpty(t, decodedStates["plan_approval"].OutputPath, "OutputPath must be saved in DB JSON")
	assert.FileExists(t, decodedStates["plan_approval"].OutputPath, "Node Output log file must exist on disk")

	// 3. Transparent hydration via store.GetRun
	hydratedSnap, err := store.GetRun(runID)
	require.NoError(t, err)
	require.NotNil(t, hydratedSnap)
	assert.Equal(t, originalYAML, hydratedSnap.DAGSpec, "DAGSpec must be hydrated seamlessly")
	assert.Equal(t, "Need manager review for spec v2", hydratedSnap.NodeStates["plan_approval"].Output, "Node Output must be hydrated seamlessly")

	// 4. Resume via handleAskUserReply
	rec := postAskUserReply(t, s, chatID, msgID, "Approved by manager")
	assert.Equal(t, http.StatusOK, rec.Code)

	// Wait for terminal completion
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, err := store.GetRun(runID)
		require.NoError(t, err)
		if snap != nil && snap.Status == workflow.PersistStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("workflow run did not reach COMPLETED in time; snap=%+v", snap)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 5. Post-settle verification
	settledSnap, err := store.GetRun(runID)
	require.NoError(t, err)
	require.NotNil(t, settledSnap)
	assert.Equal(t, workflow.PersistStatusCompleted, settledSnap.Status)
	assert.Equal(t, originalYAML, settledSnap.DAGSpec)

	// Direct DB check on settled state: raw DB node states remain pruned
	err = testDB.Table("workflow_runs").Where("run_id = ?", runID).Scan(&rawRow).Error
	require.NoError(t, err)
	assert.NotEmpty(t, rawRow.DAGSpecPath)
	assert.FileExists(t, rawRow.DAGSpecPath)
	decodedSettledStates, err := dbmodels.DecodeNodeStates(rawRow.NodeStates)
	require.NoError(t, err)
	for nodeID, ns := range decodedSettledStates {
		assert.Empty(t, ns.Output, "Node %s output in DB JSON must be empty", nodeID)
		if ns.OutputPath != "" {
			assert.FileExists(t, ns.OutputPath, "Node %s log file must exist on disk", nodeID)
		}
	}
}
