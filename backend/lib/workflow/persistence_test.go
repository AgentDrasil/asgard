package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

func (m *memStore) FindWaitingHumans(sessionID string) ([]*RunSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*RunSnapshot
	for _, run := range m.runs {
		if run.SessionID == sessionID && run.Status == PersistStatusWaitingHuman {
			copy := *run
			out = append(out, &copy)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (m *memStore) FindWaitingHumanByMessageID(messageID string) (*RunSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, run := range m.runs {
		if run.Status != PersistStatusWaitingHuman {
			continue
		}
		if run.SuspendedMessageID == messageID {
			copy := *run
			return &copy, nil
		}
		for _, info := range run.SuspendedNodes {
			if info.MessageID == messageID {
				copy := *run
				return &copy, nil
			}
		}
	}
	return nil, nil
}

func (m *memStore) RefreshSuspension(runID string, states map[string]PersistedNodeState, loopIterations, executionCounts map[string]int, suspendedNodes map[string]SuspendedNodeInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[runID]
	if run == nil {
		return fmt.Errorf("run %s not found", runID)
	}
	run.NodeStates = states
	run.LoopIterations = loopIterations
	run.ExecutionCounts = executionCounts
	run.SuspendedNodes = suspendedNodes
	suspendedNodeID, suspendedMessageID := compatSuspendedColumns(run.SuspendedNodeID, suspendedNodes)
	run.SuspendedNodeID = suspendedNodeID
	run.SuspendedMessageID = suspendedMessageID
	m.order = append(m.order, "refresh:"+run.RunID)
	return nil
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

// fallbackResumeYAML drives fix_loop to exhaustion, suspends at the human
// on_exhausted orphan, and re-enters the fixer via a resets_loop edge when
// the user replies "Retry (reset counter)".
const fallbackResumeYAML = `
name: fallback-resume
loops:
  - id: fix_loop
    nodes: [review, verdict, fixer]
    max_iterations: 2
    on_exhausted: fix_fallback
nodes:
  - id: coding
    type: command
    command: "echo code"
  - id: review
    type: command
    command: "echo review"
    depends:
      - node: coding
      - node: fixer
    join: always
  - id: verdict
    type: command
    command: "echo verdict"
    allowed_exit_codes: [0, 1]
    depends:
      - node: review
  - id: fixer
    type: command
    command: "echo fix"
    depends:
      - node: verdict
        when: "nodes.verdict.exit_code == 0"
        counts_loop: fix_loop
      - node: fix_fallback
        when: "nodes.fix_fallback.output == 'Retry (reset counter)'"
        resets_loop: fix_loop
    join: always
  - id: fix_fallback
    type: human
    prompt: "Auto-fix exhausted. Retry, skip or abort?"
    options: ["Retry (reset counter)", "Skip This Step", "Abort Workflow"]
`

// newLoopPersistenceEngine builds an engine whose command nodes are served by
// runner and whose snapshots land in the given store.
func newLoopPersistenceEngine(t *testing.T, runner NodeRunner, store RunStore) (*Engine, *suspendRecorder) {
	t.Helper()
	registry := NewNodeRunnerRegistry()
	registry.Register(runner)
	rec := &suspendRecorder{}
	engine := NewEngine(registry)
	engine.SetRunStore(store)
	engine.SetHumanSuspender(func(req SuspendRequest) error {
		rec.record(req)
		return nil
	})
	return engine, rec
}

// runUntilFallbackSuspended executes the fallback-resume workflow on engine
// until fix_fallback suspends, then simulates a process crash (cancel + settle
// CANCELLED) and restores the WAITING_HUMAN snapshot, mimicking crash recovery.
func runUntilFallbackSuspended(t *testing.T, engine *Engine, store *memStore, defn *WorkflowDefinition, runID, sessionID, runDir string) *RunSnapshot {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = engine.Execute(ctx, defn, RunContext{
			SessionID: sessionID,
			RunID:     runID,
			RunDir:    runDir,
		})
	}()

	waitFor(t, func() bool {
		return store.get(runID) != nil && store.get(runID).Status == PersistStatusWaitingHuman
	}, "run should suspend at fix_fallback")
	suspendedSnap := store.get(runID)
	require.Equal(t, "fix_fallback", suspendedSnap.SuspendedNodeID)

	cancel()
	waitFor(t, func() bool {
		return store.get(runID).Status == PersistStatusCancelled
	}, "cancelled run settles")
	require.NoError(t, store.MarkWaitingHuman(suspendedSnap))
	return suspendedSnap
}

// passAfterVerdictRuns makes verdict demand a fix (exit 0) for its first
// three invocations and pass (exit 1) afterwards.
func passAfterVerdictRuns() func(nodeID string, n int) int {
	return func(nodeID string, n int) int {
		if nodeID == "verdict" && n > 3 {
			return 1
		}
		return 0
	}
}

func TestFixFallbackResumeAfterRestart(t *testing.T) {
	defn, err := ParseDefinition([]byte(fallbackResumeYAML))
	require.NoError(t, err)

	runner := newLoopCountingRunner(passAfterVerdictRuns())
	store := newMemStore()
	engine1, _ := newLoopPersistenceEngine(t, runner, store)
	suspendedSnap := runUntilFallbackSuspended(t, engine1, store, defn, "runfb", "fb-chat", t.TempDir())

	assert.Equal(t, "wf-runfb-fix_fallback", suspendedSnap.SuspendedMessageID)
	// Circuit-breaker counters persisted at suspension time.
	assert.Equal(t, 2, suspendedSnap.LoopIterations["fix_loop"], "loop counter must persist at the quota")
	assert.Equal(t, 1, suspendedSnap.ExecutionCounts["coding"])
	assert.Equal(t, 3, suspendedSnap.ExecutionCounts["review"])
	assert.Equal(t, 3, suspendedSnap.ExecutionCounts["verdict"])
	assert.Equal(t, 2, suspendedSnap.ExecutionCounts["fixer"])
	assert.Equal(t, 1, suspendedSnap.ExecutionCounts["fix_fallback"])

	// A brand-new engine (fresh process) sharing only the store resumes the
	// orphan human node from the snapshot reply.
	engine2, _ := newLoopPersistenceEngine(t, runner, store)
	result, err := engine2.Resume(context.Background(), "runfb", "Retry (reset counter)")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, RunStatusCompleted, result.Status)
	require.NotNil(t, result.Nodes["fix_fallback"])
	assert.Equal(t, "Retry (reset counter)", result.Nodes["fix_fallback"].Output)

	// Seeded nodes never re-executed; the retry added exactly one fix round.
	assert.Equal(t, 1, runner.count("coding"), "seeded nodes must not re-execute after restart")
	assert.Equal(t, 4, runner.count("review"))
	assert.Equal(t, 4, runner.count("verdict"))
	assert.Equal(t, 3, runner.count("fixer"), "resets_loop edge re-admits fixer exactly once after resume")
	assert.Equal(t, PersistStatusCompleted, store.get("runfb").Status)
}

func TestResumeRestoresCountersAndStableMessageIDs(t *testing.T) {
	defn, err := ParseDefinition([]byte(fallbackResumeYAML))
	require.NoError(t, err)

	runner := newLoopCountingRunner(passAfterVerdictRuns())
	store := newMemStore()
	engine1, _ := newLoopPersistenceEngine(t, runner, store)
	suspendedSnap := runUntilFallbackSuspended(t, engine1, store, defn, "runfb2", "fb2-chat", t.TempDir())

	// Re-drive WITHOUT a pre-supplied reply: the orphan human node has no
	// static in-edges, so only the snapshot's ActivateNodes can wake it. It
	// suspends again — with a fresh, collision-free MessageID derived from
	// the restored execution counts.
	engine2, _ := newLoopPersistenceEngine(t, runner, store)
	rc := engine2.buildResumeContext(suspendedSnap, "", nil)
	type outcome struct {
		result *WorkflowRunResult
		err    error
	}
	outCh := make(chan outcome, 1)
	go func() {
		result, err := engine2.Execute(context.Background(), defn, rc)
		outCh <- outcome{result: result, err: err}
	}()

	waitFor(t, func() bool {
		snap := store.get("runfb2")
		return snap != nil && snap.Status == PersistStatusWaitingHuman && snap.SuspendedMessageID == "wf-runfb2-fix_fallback-2"
	}, "re-driven run should re-suspend with an iteration-2 message id")

	resumedSnap := store.get("runfb2")
	assert.Equal(t, "fix_fallback", resumedSnap.SuspendedNodeID)
	assert.Equal(t, 2, resumedSnap.LoopIterations["fix_loop"], "seed replay must not inflate the restored loop counter")
	assert.Equal(t, 3, resumedSnap.ExecutionCounts["verdict"])
	assert.Equal(t, 2, resumedSnap.ExecutionCounts["fixer"])
	assert.Equal(t, 2, resumedSnap.ExecutionCounts["fix_fallback"])

	// Nothing re-executed while re-suspending.
	assert.Equal(t, 1, runner.count("coding"))
	assert.Equal(t, 3, runner.count("review"))
	assert.Equal(t, 3, runner.count("verdict"))
	assert.Equal(t, 2, runner.count("fixer"))

	// Delivering the reply to the live re-driven run completes it.
	_, err = engine2.Resume(context.Background(), "runfb2", "Retry (reset counter)")
	require.NoError(t, err)

	out := <-outCh
	require.NoError(t, out.err)
	require.NotNil(t, out.result)
	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Equal(t, 3, runner.count("fixer"))
	assert.Equal(t, 4, runner.count("verdict"))
	assert.Equal(t, PersistStatusCompleted, store.get("runfb2").Status)
}

func TestMemStore_FindWaitingHumans(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.StartRun(&RunSnapshot{RunID: "run-a", SessionID: "chat-1", Status: PersistStatusRunning}))
	require.NoError(t, store.MarkWaitingHuman(&RunSnapshot{
		RunID:     "run-a",
		SessionID: "chat-1",
		Status:    PersistStatusWaitingHuman,
		SuspendedNodes: map[string]SuspendedNodeInfo{
			"review": {MessageID: "wf-run-a-review", Iteration: 1},
		},
	}))
	require.NoError(t, store.MarkWaitingHuman(&RunSnapshot{
		RunID:     "run-b",
		SessionID: "chat-1",
		Status:    PersistStatusWaitingHuman,
		SuspendedNodes: map[string]SuspendedNodeInfo{
			"approve": {MessageID: "wf-run-b-approve", Iteration: 1},
		},
	}))
	require.NoError(t, store.SettleRun("run-b", PersistStatusCompleted, nil))

	runs, err := store.FindWaitingHumans("chat-1")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "run-a", runs[0].RunID)

	runs, err = store.FindWaitingHumans("chat-other")
	require.NoError(t, err)
	assert.Empty(t, runs)
}

func TestMemStore_FindWaitingHumanByMessageID(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.MarkWaitingHuman(&RunSnapshot{
		RunID:              "run-1",
		SessionID:          "chat-1",
		Status:             PersistStatusWaitingHuman,
		SuspendedNodeID:    "approval",
		SuspendedMessageID: "wf-run-1-approval",
		SuspendedNodes: map[string]SuspendedNodeInfo{
			"approval": {MessageID: "wf-run-1-approval", Iteration: 1},
			"review":   {MessageID: "wf-run-1-review", Iteration: 1},
		},
	}))

	run, err := store.FindWaitingHumanByMessageID("wf-run-1-approval")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, "run-1", run.RunID)

	run, err = store.FindWaitingHumanByMessageID("wf-run-1-review")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, "run-1", run.RunID)

	run, err = store.FindWaitingHumanByMessageID("wf-missing")
	require.NoError(t, err)
	assert.Nil(t, run)
}

func TestMemStore_RefreshSuspension(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.MarkWaitingHuman(&RunSnapshot{
		RunID:              "run-1",
		SessionID:          "chat-1",
		Status:             PersistStatusWaitingHuman,
		SuspendedNodeID:    "plan_approval",
		SuspendedMessageID: "wf-run-1-plan_approval",
		SuspendedNodes: map[string]SuspendedNodeInfo{
			"plan_approval": {MessageID: "wf-run-1-plan_approval", Iteration: 1},
			"code_review":   {MessageID: "wf-run-1-code_review", Iteration: 1},
		},
	}))

	err := store.RefreshSuspension(
		"run-1",
		map[string]PersistedNodeState{
			"prep":          {Status: string(StatusSucceeded)},
			"plan_approval": {Status: string(StatusSucceeded)},
		},
		map[string]int{"fix_loop": 2},
		map[string]int{"fixer": 2},
		map[string]SuspendedNodeInfo{
			"code_review": {MessageID: "wf-run-1-code_review", Iteration: 1},
		},
	)
	require.NoError(t, err)

	snap := store.get("run-1")
	require.NotNil(t, snap)
	assert.Equal(t, PersistStatusWaitingHuman, snap.Status)
	assert.Len(t, snap.NodeStates, 2)
	assert.Equal(t, 2, snap.LoopIterations["fix_loop"])
	assert.Equal(t, 2, snap.ExecutionCounts["fixer"])
	require.Len(t, snap.SuspendedNodes, 1)
	assert.Equal(t, "wf-run-1-code_review", snap.SuspendedNodes["code_review"].MessageID)
	assert.Equal(t, "code_review", snap.SuspendedNodeID)
	assert.Equal(t, "wf-run-1-code_review", snap.SuspendedMessageID)

	// Unknown runs are rejected.
	err = store.RefreshSuspension("missing", nil, nil, nil, nil)
	assert.Error(t, err)
}
