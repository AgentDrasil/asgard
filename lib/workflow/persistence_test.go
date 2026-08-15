package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memStore is an in-memory workflow.RunStore fake.
type memStore struct {
	mu    sync.Mutex
	runs  map[string]*RunSnapshot
	order []string
}

func newMemStore() *memStore {
	return &memStore{runs: make(map[string]*RunSnapshot)}
}

func (m *memStore) StartRun(run *RunSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := *run
	m.runs[run.RunID] = &copy
	m.order = append(m.order, "start:"+run.RunID)
	return nil
}

func (m *memStore) MarkWaitingHuman(run *RunSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := *run
	m.runs[run.RunID] = &copy
	m.order = append(m.order, "wait:"+run.RunID)
	return nil
}

func (m *memStore) SettleRun(runID string, status string, states map[string]PersistedNodeState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[runID]
	if run == nil {
		return fmt.Errorf("run %s not found", runID)
	}
	run.Status = status
	run.NodeStates = states
	run.SuspendedNodeID = ""
	run.SuspendedMessageID = ""
	m.order = append(m.order, "settle:"+run.RunID+":"+status)
	return nil
}

func (m *memStore) GetRun(runID string) (*RunSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.runs[runID]; run != nil {
		copy := *run
		return &copy, nil
	}
	return nil, nil
}

func (m *memStore) FindWaitingHuman(sessionID string) (*RunSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, run := range m.runs {
		if run.SessionID == sessionID && run.Status == PersistStatusWaitingHuman {
			copy := *run
			return &copy, nil
		}
	}
	return nil, nil
}

func (m *memStore) get(runID string) *RunSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.runs[runID]; run != nil {
		copy := *run
		return &copy
	}
	return nil
}

const humanLoopYAML = `
name: human-loop
tmp_dir: "tmp/${session_id}"
nodes:
  - id: prep
    type: command
    command: "echo prepared > ${tmp_dir}/prep.txt"
    output_file: "prep.txt"
  - id: plan_approval
    type: human
    depends:
      - node: prep
    prompt: "review the plan at ${tmp_dir}/prep.txt"
    options: ["Approve", "Reject"]
    output_file: "user_feedback.md"
  - id: final
    type: command
    depends:
      - node: plan_approval
    command: "cat ${tmp_dir}/user_feedback.md > ${tmp_dir}/final.txt"
    output_file: "final.txt"
`

func newTestEngine(t *testing.T) (*Engine, *memStore, *suspendRecorder) {
	t.Helper()
	registry := NewNodeRunnerRegistry()
	registry.Register(NewCommandRunner(false))
	store := newMemStore()
	rec := &suspendRecorder{}
	engine := NewEngine(registry)
	engine.SetRunStore(store)
	engine.SetHumanSuspender(func(req SuspendRequest) error {
		rec.record(req)
		return nil
	})
	return engine, store, rec
}

type suspendRecorder struct {
	mu       sync.Mutex
	requests []SuspendRequest
}

func (r *suspendRecorder) record(req SuspendRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
}

func (r *suspendRecorder) all() []SuspendRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]SuspendRequest{}, r.requests...)
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for condition: %s", msg)
}

func TestHumanMessageIDDeterministic(t *testing.T) {
	assert.Equal(t, "wf-run789-plan_approval", HumanMessageID("run789", "plan_approval"))
}

func TestHumanNodeSuspendAndResumeInProcess(t *testing.T) {
	engine, store, rec := newTestEngine(t)
	defn, err := ParseDefinition([]byte(humanLoopYAML))
	require.NoError(t, err)

	runDir := t.TempDir()
	type outcome struct {
		result *WorkflowRunResult
		err    error
	}
	outCh := make(chan outcome, 1)
	go func() {
		result, err := engine.Execute(context.Background(), defn, RunContext{
			SessionID: "chat-1",
			RunID:     "run789",
			RunDir:    runDir,
			Input:     "build the feature",
		})
		outCh <- outcome{result: result, err: err}
	}()

	// The run suspends at the human node with a deterministic MessageID.
	waitFor(t, func() bool {
		return store.get("run789") != nil && store.get("run789").Status == PersistStatusWaitingHuman
	}, "run should reach WAITING_HUMAN")

	snap := store.get("run789")
	assert.Equal(t, "plan_approval", snap.SuspendedNodeID)
	assert.Equal(t, "wf-run789-plan_approval", snap.SuspendedMessageID)
	assert.Equal(t, PersistStatusWaitingHuman, snap.Status)
	// The prep node settled before the snapshot.
	assert.Equal(t, string(StatusSucceeded), snap.NodeStates["prep"].Status)

	require.Len(t, rec.all(), 1)
	req := rec.all()[0]
	assert.Equal(t, "wf-run789-plan_approval", req.MessageID)
	assert.Contains(t, req.Prompt, "review the plan at")
	assert.Contains(t, req.Prompt, "Approve / Reject")
	assert.Equal(t, []string{"Approve", "Reject"}, req.Options)

	// Resume via the engine (in-process waiter delivery).
	_, err = engine.Resume(context.Background(), "run789", "Approved")
	require.NoError(t, err)

	out := <-outCh
	require.NoError(t, out.err)
	require.NotNil(t, out.result)
	assert.Equal(t, RunStatusCompleted, out.result.Status)

	artifactsDir := filepath.Join(runDir, "tmp", "chat-1")
	feedback, err := os.ReadFile(filepath.Join(artifactsDir, "user_feedback.md"))
	require.NoError(t, err)
	assert.Equal(t, "Approved", string(feedback))
	finalOut, err := os.ReadFile(filepath.Join(artifactsDir, "final.txt"))
	require.NoError(t, err)
	assert.Equal(t, "Approved", strings.TrimSpace(string(finalOut)))

	// The settled snapshot is terminal.
	settled := store.get("run789")
	assert.Equal(t, PersistStatusCompleted, settled.Status)
}

func TestHumanNodeResumeAfterRestart(t *testing.T) {
	engine1, store, _ := newTestEngine(t)
	defn, err := ParseDefinition([]byte(humanLoopYAML))
	require.NoError(t, err)

	runDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = engine1.Execute(ctx, defn, RunContext{
			SessionID: "chat-1",
			RunID:     "runrestart",
			RunDir:    runDir,
		})
	}()

	waitFor(t, func() bool {
		return store.get("runrestart") != nil && store.get("runrestart").Status == PersistStatusWaitingHuman
	}, "run should reach WAITING_HUMAN")
	suspendedSnap := store.get("runrestart")

	// Simulate process death: cancel the original engine and restore the
	// WAITING_HUMAN snapshot (a crash never settles the run).
	cancel()
	waitFor(t, func() bool {
		return store.get("runrestart").Status == PersistStatusCancelled
	}, "cancelled run settles")
	require.NoError(t, store.MarkWaitingHuman(suspendedSnap))

	// A brand-new engine (fresh process) sharing only the store resumes the
	// run from the DB snapshot.
	registry := NewNodeRunnerRegistry()
	registry.Register(NewCommandRunner(false))
	engine2 := NewEngine(registry)
	engine2.SetRunStore(store)

	result, err := engine2.Resume(context.Background(), "runrestart", "looks good, ship it")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, RunStatusCompleted, result.Status)
	assert.Equal(t, StatusSucceeded, result.Nodes["plan_approval"].Status)
	assert.Equal(t, "looks good, ship it", result.Nodes["plan_approval"].Output)

	artifactsDir := filepath.Join(runDir, "tmp", "chat-1")
	feedback, err := os.ReadFile(filepath.Join(artifactsDir, "user_feedback.md"))
	require.NoError(t, err)
	assert.Equal(t, "looks good, ship it", string(feedback))
	finalOut, err := os.ReadFile(filepath.Join(artifactsDir, "final.txt"))
	require.NoError(t, err)
	assert.Equal(t, "looks good, ship it", strings.TrimSpace(string(finalOut)))
	assert.Equal(t, PersistStatusCompleted, store.get("runrestart").Status)
}

func TestResumeRejectsNonWaitingRun(t *testing.T) {
	engine, store, _ := newTestEngine(t)
	require.NoError(t, store.StartRun(&RunSnapshot{
		RunID: "run1", SessionID: "s", Status: PersistStatusCompleted,
		NodeStates: map[string]PersistedNodeState{},
	}))
	_, err := engine.Resume(context.Background(), "run1", "reply")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not waiting for human input")
}

func TestParallelHumanNodesRejected(t *testing.T) {
	yaml := `
name: parallel-human
nodes:
  - id: start
    type: command
    command: "echo hi"
  - id: review_a
    type: human
    depends:
      - node: start
    prompt: "review a"
  - id: review_b
    type: human
    depends:
      - node: start
    prompt: "review b"
  - id: join
    type: command
    depends:
      - node: review_a
      - node: review_b
    command: "echo done"
`
	_, err := ParseDefinition([]byte(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parallel human nodes are not supported in Phase 3")
}

func TestSequentialHumanNodesAllowed(t *testing.T) {
	yaml := `
name: sequential-human
nodes:
  - id: approval_1
    type: human
    prompt: "first approval"
  - id: approval_2
    type: human
    depends:
      - node: approval_1
    prompt: "second approval"
`
	_, err := ParseDefinition([]byte(yaml))
	require.NoError(t, err)
}

func TestHumanNodeRequiresPrompt(t *testing.T) {
	yaml := `
name: missing-prompt
nodes:
  - id: approval
    type: human
`
	_, err := ParseDefinition([]byte(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt is required for human nodes")
}
