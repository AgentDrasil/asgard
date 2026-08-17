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

	"github.com/AgentDrasil/asgard/lib/db"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
	"github.com/AgentDrasil/asgard/lib/workflow"
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
	assert.Equal(t, "tool_call", msg.Role)
	assert.Equal(t, "Intent Analyst", msg.AgentName)
	assert.Equal(t, []string{"/tmp/intend.md"}, msg.ArtifactFiles)
	assert.Equal(t, "Writing requirements to /tmp/intend.md", msg.Content)
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
