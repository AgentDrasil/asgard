package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	_ "github.com/AgentDrasil/asgard/plugins/notebook"
)

type fakeAgentSource struct {
	mu        sync.Mutex
	agents    []*agentspec.Agent
	listeners []func()
}

func newFakeAgentSource(agents []*agentspec.Agent) *fakeAgentSource {
	return &fakeAgentSource{agents: agents}
}

func (s *fakeAgentSource) Agents() ([]*agentspec.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*agentspec.Agent, len(s.agents))
	copy(out, s.agents)
	return out, nil
}

func (s *fakeAgentSource) OnAgentsChange(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
}

func (s *fakeAgentSource) setAgents(agents []*agentspec.Agent) {
	s.mu.Lock()
	s.agents = agents
	listeners := append([]func(){}, s.listeners...)
	s.mu.Unlock()
	for _, fn := range listeners {
		fn()
	}
}

type fakeCallbacks struct {
	mu             sync.Mutex
	initSessions   []*runnerpb.InitWorkflowSessionRequest
	events         []*runnerpb.ReportWorkflowEventRequest
	runStarts      []*runnerpb.WorkflowRunStartRequest
	runSettles     []*runnerpb.WorkflowRunSettleRequest
	initSessionErr error
	runStartErr    error
	runSettleErr   error
	reportEventErr error
}

func (f *fakeCallbacks) AskUser(ctx context.Context, in *runnerpb.AskUserRequest, opts ...grpc.CallOption) (*runnerpb.AskUserResponse, error) {
	return &runnerpb.AskUserResponse{Reply: "ok"}, nil
}

func (f *fakeCallbacks) CallPeer(ctx context.Context, in *runnerpb.CallPeerRequest, opts ...grpc.CallOption) (*runnerpb.CallPeerResponse, error) {
	return &runnerpb.CallPeerResponse{}, nil
}

func (f *fakeCallbacks) FindPeer(ctx context.Context, in *runnerpb.FindPeerRequest, opts ...grpc.CallOption) (*runnerpb.FindPeerResponse, error) {
	return &runnerpb.FindPeerResponse{}, nil
}

func (f *fakeCallbacks) RecordMetrics(ctx context.Context, in *runnerpb.RecordMetricsRequest, opts ...grpc.CallOption) (*runnerpb.RecordMetricsResponse, error) {
	return &runnerpb.RecordMetricsResponse{}, nil
}

func (f *fakeCallbacks) GetUserProxyKeys(ctx context.Context, in *runnerpb.GetUserProxyKeysRequest, opts ...grpc.CallOption) (*runnerpb.GetUserProxyKeysResponse, error) {
	return &runnerpb.GetUserProxyKeysResponse{}, nil
}

func (f *fakeCallbacks) InitWorkflowSession(ctx context.Context, in *runnerpb.InitWorkflowSessionRequest, opts ...grpc.CallOption) (*runnerpb.InitWorkflowSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initSessions = append(f.initSessions, in)
	return &runnerpb.InitWorkflowSessionResponse{}, f.initSessionErr
}

func (f *fakeCallbacks) ReportWorkflowEvent(ctx context.Context, in *runnerpb.ReportWorkflowEventRequest, opts ...grpc.CallOption) (*runnerpb.ReportWorkflowEventResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, in)
	return &runnerpb.ReportWorkflowEventResponse{}, f.reportEventErr
}

func (f *fakeCallbacks) WorkflowRunStart(ctx context.Context, in *runnerpb.WorkflowRunStartRequest, opts ...grpc.CallOption) (*runnerpb.WorkflowRunStartResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runStarts = append(f.runStarts, in)
	return &runnerpb.WorkflowRunStartResponse{}, f.runStartErr
}

func (f *fakeCallbacks) WorkflowRunSettle(ctx context.Context, in *runnerpb.WorkflowRunSettleRequest, opts ...grpc.CallOption) (*runnerpb.WorkflowRunSettleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runSettles = append(f.runSettles, in)
	return &runnerpb.WorkflowRunSettleResponse{}, f.runSettleErr
}

func createTestWorkflowAgent(t *testing.T, id, nodeID, runnerID, schedule, agentType, extraNodes string) *agentspec.Agent {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")

	if agentType == "" {
		agentType = "workflow"
	}
	runnerField := ""
	if runnerID != "" {
		runnerField = fmt.Sprintf("runner_id: %s\n", runnerID)
	}

	nodes := `
  - id: step1
    type: command
    command: "echo hello"
`
	if extraNodes != "" {
		nodes = extraNodes
	}

	spec := fmt.Sprintf(`name: %s
no_human: true
schedule: %q
%snodes:%s`, id, schedule, runnerField, nodes)

	require.NoError(t, os.WriteFile(path, []byte(spec), 0o644))

	return &agentspec.Agent{
		NodeID: nodeID,
		Config: agentspec.AgentConfig{
			ID:   id,
			Name: id,
			Type: agentType,
		},
		Path:         dir,
		WorkflowPath: path,
	}
}

func TestRunnerCronManager_Reload_TableDriven(t *testing.T) {
	t.Parallel()

	nodeID := "runner-test"

	tests := []struct {
		name          string
		agentNodeID   string
		defnRunnerID  string
		schedule      string
		agentType     string
		expectJobKey  string
		expectPresent bool
	}{
		{
			name:          "local runner-owned workflow with schedule",
			agentNodeID:   nodeID,
			defnRunnerID:  "",
			schedule:      "0 2 * * *",
			agentType:     "workflow",
			expectJobKey:  nodeID + "/wf-local",
			expectPresent: true,
		},
		{
			name:          "global workflow targeting this runner with schedule",
			agentNodeID:   "",
			defnRunnerID:  nodeID,
			schedule:      "0 3 * * *",
			agentType:     "workflow",
			expectJobKey:  "/wf-global-targeted",
			expectPresent: false,
		},
		{
			name:          "local runner-owned workflow without schedule",
			agentNodeID:   nodeID,
			defnRunnerID:  "",
			schedule:      "",
			agentType:     "workflow",
			expectJobKey:  nodeID + "/wf-no-sched",
			expectPresent: false,
		},
		{
			name:          "workflow owned by another runner",
			agentNodeID:   "other-runner",
			defnRunnerID:  "",
			schedule:      "0 4 * * *",
			agentType:     "workflow",
			expectJobKey:  "other-runner/wf-other",
			expectPresent: false,
		},
		{
			name:          "global workflow targeting another runner",
			agentNodeID:   "",
			defnRunnerID:  "other-runner",
			schedule:      "0 5 * * *",
			agentType:     "workflow",
			expectJobKey:  "/wf-other-targeted",
			expectPresent: false,
		},
		{
			name:          "global workflow targeting no runner",
			agentNodeID:   "",
			defnRunnerID:  "",
			schedule:      "0 6 * * *",
			agentType:     "workflow",
			expectJobKey:  "/wf-global-no-runner",
			expectPresent: false,
		},
		{
			name:          "non-workflow agent",
			agentNodeID:   nodeID,
			defnRunnerID:  "",
			schedule:      "0 7 * * *",
			agentType:     "chat",
			expectJobKey:  nodeID + "/chat-agent",
			expectPresent: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			id := filepath.Base(tt.expectJobKey)
			agent := createTestWorkflowAgent(t, id, tt.agentNodeID, tt.defnRunnerID, tt.schedule, tt.agentType, "")

			src := newFakeAgentSource([]*agentspec.Agent{agent})
			mgr, err := New(Options{
				NodeID: nodeID,
				Remote: src,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = mgr.Shutdown() })

			jobs := mgr.Jobs()
			if tt.expectPresent {
				assert.Contains(t, jobs, tt.expectJobKey)
			} else {
				assert.NotContains(t, jobs, tt.expectJobKey)
			}
		})
	}
}

func TestRunnerCronManager_DynamicReload(t *testing.T) {
	t.Parallel()

	nodeID := "runner-dynamic"
	a1 := createTestWorkflowAgent(t, "wf1", nodeID, "", "0 1 * * *", "workflow", "")
	a2 := createTestWorkflowAgent(t, "wf2", nodeID, "", "0 2 * * *", "workflow", "")

	src := newFakeAgentSource([]*agentspec.Agent{a1, a2})
	mgr, err := New(Options{
		NodeID: nodeID,
		Remote: src,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Shutdown() })

	assert.ElementsMatch(t, []string{nodeID + "/wf1", nodeID + "/wf2"}, mgr.Jobs())

	// Remove a1, add a3
	a3 := createTestWorkflowAgent(t, "wf3", nodeID, "", "0 3 * * *", "workflow", "")
	src.setAgents([]*agentspec.Agent{a2, a3})

	assert.ElementsMatch(t, []string{nodeID + "/wf2", nodeID + "/wf3"}, mgr.Jobs())
}

func TestRunnerCronManager_RunScheduledWorkflow(t *testing.T) {
	t.Parallel()

	nodeID := "runner-exec"
	cbs := &fakeCallbacks{}

	agent := createTestWorkflowAgent(t, "wf-cmd", nodeID, "", "0 2 * * *", "workflow", `
  - id: cmd1
    type: command
    sandbox: false
    command: "echo run_cmd_test"
`)

	src := newFakeAgentSource([]*agentspec.Agent{agent})
	mgr, err := New(Options{
		NodeID:           nodeID,
		Remote:           src,
		Callbacks:        func() (runnerpb.AgentCallbackServiceClient, error) { return cbs, nil },
		AllowUnsandboxed: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Shutdown() })

	mgr.RunScheduledWorkflow(agent)

	cbs.mu.Lock()
	defer cbs.mu.Unlock()

	// Verify session initialized
	require.Len(t, cbs.initSessions, 1)
	assert.Equal(t, nodeID, cbs.initSessions[0].RunnerId)
	assert.Equal(t, "wf-cmd", cbs.initSessions[0].AgentId)
	assert.Equal(t, "wf-cmd", cbs.initSessions[0].WorkflowName)
	assert.Equal(t, "wf-cron-runner-exec-wf-cmd", cbs.initSessions[0].SessionId)

	// Verify run started and settled
	require.Len(t, cbs.runStarts, 1)
	assert.Equal(t, cbs.initSessions[0].SessionId, cbs.runStarts[0].SessionId)
	assert.NotEmpty(t, cbs.runStarts[0].RunId)

	require.Len(t, cbs.runSettles, 1)
	assert.Equal(t, cbs.runStarts[0].RunId, cbs.runSettles[0].RunId)
	assert.Equal(t, "COMPLETED", cbs.runSettles[0].Status)

	// Verify workflow events streamed
	var eventTypes []string
	for _, ev := range cbs.events {
		eventTypes = append(eventTypes, ev.EventType)
	}
	assert.Contains(t, eventTypes, string(workflow.EventWorkflowStarted))
	assert.Contains(t, eventTypes, string(workflow.EventNodeStarted))
	assert.Contains(t, eventTypes, string(workflow.EventNodeFinished))
	assert.Contains(t, eventTypes, string(workflow.EventWorkflowFinished))
}

func TestRunnerCronManager_NotebookWorkflow_LocalExecution(t *testing.T) {
	t.Parallel()

	nodeID := "runner-notebook"
	cbs := &fakeCallbacks{}

	vaultDir := t.TempDir()
	inboxDir := filepath.Join(vaultDir, "inbox")
	require.NoError(t, os.MkdirAll(inboxDir, 0o755))
	notePath := filepath.Join(inboxDir, "test_article.md")
	require.NoError(t, os.WriteFile(notePath, []byte("# Test Note Content\n"), 0o644))

	nodes := `
  - id: scan
    type: function
    function: "notebook.scan_ingest_pending"
    args:
      vault_path: "${run_dir}"
  - id: mock_fanout
    type: command
    sandbox: false
    depends:
      - node: scan
    command: "echo '{\"item\": \"inbox/test_article.md\", \"status\": \"SUCCEEDED\"}' > ${session_dir}/ingest_results.jsonl"
  - id: record
    type: function
    function: "notebook.record_ingest_success"
    depends:
      - node: mock_fanout
    args:
      vault_path: "${run_dir}"
`
	agent := createTestWorkflowAgent(t, "notebook-workflow", nodeID, "", "0 2 * * *", "workflow", nodes)
	agent.Config.RunDirs = []string{vaultDir}

	src := newFakeAgentSource([]*agentspec.Agent{agent})
	mgr, err := New(Options{
		NodeID:           nodeID,
		Remote:           src,
		Callbacks:        func() (runnerpb.AgentCallbackServiceClient, error) { return cbs, nil },
		AllowUnsandboxed: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Shutdown() })

	mgr.RunScheduledWorkflow(agent)

	cbs.mu.Lock()
	defer cbs.mu.Unlock()

	require.Len(t, cbs.runSettles, 1)
	assert.Equal(t, "COMPLETED", cbs.runSettles[0].Status)

	// Verify ingest_state.yaml exists in vaultDir/.state/
	stateFile := filepath.Join(vaultDir, ".state", "ingest_state.yaml")
	require.FileExists(t, stateFile)
	data, err := os.ReadFile(stateFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "test_article.md")
}

func TestRemoteRunStore(t *testing.T) {
	t.Parallel()

	cbs := &fakeCallbacks{}
	store := newRemoteRunStore(func() (runnerpb.AgentCallbackServiceClient, error) {
		return cbs, nil
	})

	snap := &workflow.RunSnapshot{
		RunID:             "r-123",
		SessionID:         "s-456",
		Status:            workflow.PersistStatusRunning,
		DAGSpec:           "name: test",
		RunDir:            "/tmp/run",
		Input:             "prompt",
		ParentRunID:       "p-789",
		AllowCrossSession: true,
	}

	require.NoError(t, store.StartRun(snap))

	cbs.mu.Lock()
	require.Len(t, cbs.runStarts, 1)
	assert.Equal(t, "r-123", cbs.runStarts[0].RunId)
	assert.Equal(t, "s-456", cbs.runStarts[0].SessionId)
	assert.Equal(t, "name: test", cbs.runStarts[0].DagSpec)
	assert.Equal(t, "/tmp/run", cbs.runStarts[0].RunDir)
	assert.Equal(t, "p-789", cbs.runStarts[0].ParentRunId)
	assert.True(t, cbs.runStarts[0].AllowCrossSession)
	cbs.mu.Unlock()

	states := map[string]workflow.PersistedNodeState{
		"node1": {
			Status: "completed",
			Output: "out",
		},
	}
	require.NoError(t, store.SettleRun("r-123", "completed", states))

	cbs.mu.Lock()
	require.Len(t, cbs.runSettles, 1)
	assert.Equal(t, "r-123", cbs.runSettles[0].RunId)
	assert.Equal(t, "completed", cbs.runSettles[0].Status)
	var decoded map[string]workflow.PersistedNodeState
	require.NoError(t, json.Unmarshal(cbs.runSettles[0].NodeStatesJson, &decoded))
	assert.Equal(t, "completed", decoded["node1"].Status)
	cbs.mu.Unlock()

	// Verify unhandled methods return safe defaults or nil
	assert.NoError(t, store.MarkWaitingHuman(&workflow.RunSnapshot{}))
	assert.NoError(t, store.MarkRunning("r-123"))
	assert.NoError(t, store.RefreshSuspension("r-123", nil, nil, nil, nil))
	run, err := store.GetRun("r-123")
	assert.NoError(t, err)
	assert.Nil(t, run)
	allWaiting, err := store.FindWaitingHumans("s-456")
	assert.NoError(t, err)
	assert.Nil(t, allWaiting)
	msgWaiting, err := store.FindWaitingHumanByMessageID("m-1")
	assert.NoError(t, err)
	assert.Nil(t, msgWaiting)
}

func (f *fakeCallbacks) HoldMergeLock(context.Context, *runnerpb.HoldMergeLockRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[runnerpb.HoldMergeLockResponse], error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}
