package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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

// blockingNodeRunner blocks a command node until its context is cancelled,
// letting tests deterministically observe a workflow being stopped mid-flight.
type blockingNodeRunner struct {
	started chan struct{}
	once    sync.Once
}

func (r *blockingNodeRunner) Supports(t workflowspec.NodeType) bool {
	return t == workflowspec.NodeTypeCommand
}

func (r *blockingNodeRunner) Run(ctx context.Context, nctx *workflow.NodeContext) (*workflowspec.NodeResult, error) {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestStopSession_InvalidChatID(t *testing.T) {
	server, _, _ := setupQueueTestServer(t)

	code, _ := server.stopSessionExecution("bad id!")
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestStopSession_NotRunning(t *testing.T) {
	server, repo, _ := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-0000000000a1"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+chatID+"/stop", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestStopSession_SingleAgentRunning(t *testing.T) {
	server, repo, hub := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-0000000000a2"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	started := make(chan struct{})
	runErr := make(chan error, 1)
	server.runSingleAgentFn = func(ctx context.Context, agent *agentspec.Agent, cid string, req TriggerMessageRequest) (string, string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		runErr <- ctx.Err()
		return "failed", "", ctx.Err()
	}

	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	payload, err := json.Marshal(TriggerMessageRequest{Prompt: "run until stopped", ChatID: chatID})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("agent execution did not start")
	}

	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+chatID+"/stop", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "stopped", body["status"])
	assert.Equal(t, chatID, body["chatId"])

	select {
	case err := <-runErr:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("agent context was not cancelled")
	}

	assert.Eventually(t, func() bool {
		_, running := server.activeExecutions.Load(chatID)
		return !running
	}, 3*time.Second, 10*time.Millisecond, "execution guard must be released after stop")

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	found := false
	for _, m := range sess.Messages {
		if m.ActivityType == "CANCELLED" {
			found = true
			assert.Equal(t, "执行已由用户终止", m.Content)
		}
	}
	assert.True(t, found, "expected a CANCELLED activity message")

	var sawQueue, sawMessage, sawStatus, sawDone bool
	deadline := time.After(3 * time.Second)
	for !sawQueue || !sawMessage || !sawStatus || !sawDone {
		select {
		case ev := <-subCh:
			switch ev.Type {
			case EventTypeQueue:
				sawQueue = true
			case EventTypeMessage:
				if ev.Message != nil && ev.Message.ActivityType == "CANCELLED" {
					sawMessage = true
				}
			case EventTypeStatus:
				if v, ok := ev.Payload["isRunning"].(bool); ok && !v {
					sawStatus = true
				}
			case EventTypeDone:
				sawDone = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for stop events (queue=%v message=%v status=%v done=%v)", sawQueue, sawMessage, sawStatus, sawDone)
		}
	}
}

func TestStopSession_ClearsQueuedMessages(t *testing.T) {
	server, repo, _ := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-0000000000a4"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	started := make(chan struct{})
	server.runSingleAgentFn = func(ctx context.Context, agent *agentspec.Agent, cid string, req TriggerMessageRequest) (string, string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return "failed", "", ctx.Err()
	}

	first, err := json.Marshal(TriggerMessageRequest{Prompt: "first", ChatID: chatID})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(first))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("agent execution did not start")
	}

	second, err := json.Marshal(TriggerMessageRequest{Prompt: "queued while running", ChatID: chatID})
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(second))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)

	queued, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	require.Len(t, queued, 1)

	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+chatID+"/stop", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	queued, err = repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Empty(t, queued, "stop must clear queued messages")
}

func TestStopSession_WorkflowRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)
	hub := NewSessionEventHubWithCapacity(20)
	t.Cleanup(hub.Close)

	tempDir := t.TempDir()
	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte(fmt.Sprintf(`
name: stop-workflow
tmp_dir: "%s/tmp/${session_id}"
nodes:
  - id: slow
    type: command
    command: "sleep 100"
`, tempDir)), 0644))

	runner := &blockingNodeRunner{started: make(chan struct{})}
	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(runner)
	engine := workflow.NewEngine(registry)
	engine.SetRunStore(newWorkflowRunStore(wfRepo))

	wfAgent := &agentspec.Agent{
		Config:       agentspec.AgentConfig{ID: "wf-stop", Name: "WF Stop", Type: "workflow"},
		WorkflowPath: wfFile,
	}

	server := &Server{
		conf:            &config.Config{},
		repo:            repo,
		workflowRunRepo: wfRepo,
		eventHub:        hub,
		workflowEngine:  engine,
		agents:          []*agentspec.Agent{wfAgent},
	}
	server.mux = server.buildMuxLocked()

	chatID := "018f3a5b-0000-7000-8000-0000000000a5"
	require.NoError(t, repo.UpdateAgentSession(chatID, "wf-stop", "", "", nil))

	payload, err := json.Marshal(TriggerMessageRequest{Prompt: "start workflow", ChatID: chatID})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/wf-stop/message", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)

	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("workflow node did not start")
	}

	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+chatID+"/stop", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	assert.Eventually(t, func() bool {
		runs, err := wfRepo.ListRunsBySession(chatID)
		if err != nil || len(runs) == 0 {
			return false
		}
		return runs[0].Status == dbmodels.WorkflowStatusCancelled
	}, 5*time.Second, 20*time.Millisecond, "workflow run must settle CANCELLED")

	assert.Eventually(t, func() bool {
		_, running := server.activeExecutions.Load(chatID)
		return !running
	}, 3*time.Second, 10*time.Millisecond, "execution guard must be released after workflow stop")

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	sawCancel := false
	for _, m := range sess.Messages {
		if m.ActivityType == "CANCELLED" {
			sawCancel = true
		}
		assert.NotEqual(t, "error", m.Role, "cancelling a workflow must not persist node failure errors")
	}
	assert.True(t, sawCancel, "expected a CANCELLED activity message")
}

func TestStopWorkflowRun_NotFound(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)

	server := &Server{conf: &config.Config{}, workflowRunRepo: wfRepo}
	server.mux = server.buildMuxLocked()

	req := httptest.NewRequest(http.MethodPost, "/api/workflows/missing-run/stop", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestStopWorkflowRun_Valid(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)

	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)

	agent := &agentspec.Agent{Config: agentspec.AgentConfig{ID: "test-agent", Name: "Test Agent", Type: "agent"}}
	server := &Server{
		conf:            &config.Config{},
		repo:            repo,
		workflowRunRepo: wfRepo,
		eventHub:        hub,
		agents:          []*agentspec.Agent{agent},
	}
	server.mux = server.buildMuxLocked()

	chatID := "018f3a5b-0000-7000-8000-0000000000a6"
	runID := "run-stop-valid"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))
	require.NoError(t, wfRepo.SaveRun(&dbmodels.WorkflowRun{
		RunID:      runID,
		SessionID:  chatID,
		Status:     dbmodels.WorkflowStatusRunning,
		NodeStates: "{}",
		DAGSpec:    "{}",
	}))
	server.activeExecutions.Store(chatID, &executionHandle{isWorkflow: true, startTime: time.Now()})
	t.Cleanup(func() { server.activeExecutions.Delete(chatID) })

	req := httptest.NewRequest(http.MethodPost, "/api/workflows/"+runID+"/stop", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "stopped", body["status"])
	assert.Equal(t, runID, body["runId"])
	assert.Equal(t, chatID, body["chatId"])
}
