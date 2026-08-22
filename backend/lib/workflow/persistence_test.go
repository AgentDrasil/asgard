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

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
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
	defn, err := workflowspec.ParseDefinition([]byte(humanLoopYAML))
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
	assert.Equal(t, string(workflowspec.StatusSucceeded), snap.NodeStates["prep"].Status)

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
	defn, err := workflowspec.ParseDefinition([]byte(humanLoopYAML))
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
	assert.Equal(t, workflowspec.StatusSucceeded, result.Nodes["plan_approval"].Status)
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

func TestParallelHumanNodesAllowed(t *testing.T) {
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
	_, err := workflowspec.ParseDefinition([]byte(yaml))
	require.NoError(t, err)
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
	_, err := workflowspec.ParseDefinition([]byte(yaml))
	require.NoError(t, err)
}

func TestHumanNodeRequiresPrompt(t *testing.T) {
	yaml := `
name: missing-prompt
nodes:
  - id: approval
    type: human
`
	_, err := workflowspec.ParseDefinition([]byte(yaml))
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
func runUntilFallbackSuspended(t *testing.T, engine *Engine, store *memStore, defn *workflowspec.WorkflowDefinition, runID, sessionID, runDir string) *RunSnapshot {
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
	defn, err := workflowspec.ParseDefinition([]byte(fallbackResumeYAML))
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
	defn, err := workflowspec.ParseDefinition([]byte(fallbackResumeYAML))
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
		return snap != nil && snap.Status == PersistStatusWaitingHuman && snap.SuspendedMessageID == "wf-runfb2-fix_fallback" &&
			snap.ExecutionCounts["fix_fallback"] == 2
	}, "re-driven run should re-suspend with the reused original message id")

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
			"prep":          {Status: string(workflowspec.StatusSucceeded)},
			"plan_approval": {Status: string(workflowspec.StatusSucceeded)},
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

// parallelDualLoopYAML defines a DAG with two parallel loops, each exhausting to an on_exhausted orphan human node.
// This is structurally valid under the current validator and allows concurrent human suspensions.
const parallelDualLoopYAML = `
name: parallel-dual-loop
loops:
  - id: loop_a
    nodes: [review_a, verdict_a, fixer_a]
    max_iterations: 1
    on_exhausted: human_a
  - id: loop_b
    nodes: [review_b, verdict_b, fixer_b]
    max_iterations: 1
    on_exhausted: human_b
nodes:
  - id: root
    type: command
    command: "echo root"
  - id: review_a
    type: command
    command: "echo review_a"
    depends:
      - node: root
      - node: fixer_a
      - node: human_a
        when: "nodes.human_a.output == 'retry'"
        resets_loop: loop_a
    join: always
  - id: verdict_a
    type: command
    command: "echo verdict_a"
    allowed_exit_codes: [0, 1]
    depends:
      - node: review_a
  - id: fixer_a
    type: command
    command: "echo fixer_a"
    depends:
      - node: verdict_a
        when: "nodes.verdict_a.exit_code == 0"
        counts_loop: loop_a
  - id: review_b
    type: command
    command: "echo review_b"
    depends:
      - node: root
      - node: fixer_b
      - node: human_b
        when: "nodes.human_b.output == 'retry'"
        resets_loop: loop_b
    join: always
  - id: verdict_b
    type: command
    command: "echo verdict_b"
    allowed_exit_codes: [0, 1]
    depends:
      - node: review_b
  - id: fixer_b
    type: command
    command: "echo fixer_b"
    depends:
      - node: verdict_b
        when: "nodes.verdict_b.exit_code == 0"
        counts_loop: loop_b
  - id: human_a
    type: human
    prompt: "Human A prompt"
    options: ["ok", "retry"]
  - id: human_b
    type: human
    prompt: "Human B prompt"
    options: ["ok", "retry"]
  - id: final
    type: command
    command: "echo final"
    depends:
      - node: human_a
      - node: human_b
`

// parallelDualLoopWithBrotherCommandYAML defines two parallel human nodes and a slow command node.
const parallelDualLoopWithBrotherCommandYAML = `
name: parallel-dual-loop-brother
loops:
  - id: loop_a
    nodes: [review_a, verdict_a, fixer_a]
    max_iterations: 1
    on_exhausted: human_a
  - id: loop_b
    nodes: [review_b, verdict_b, fixer_b]
    max_iterations: 1
    on_exhausted: human_b
nodes:
  - id: root
    type: command
    command: "echo root"
  - id: review_a
    type: command
    command: "echo review_a"
    depends:
      - node: root
      - node: fixer_a
    join: always
  - id: verdict_a
    type: command
    command: "echo verdict_a"
    allowed_exit_codes: [0, 1]
    depends:
      - node: review_a
  - id: fixer_a
    type: command
    command: "echo fixer_a"
    depends:
      - node: verdict_a
        when: "nodes.verdict_a.exit_code == 0"
        counts_loop: loop_a
  - id: review_b
    type: command
    command: "echo review_b"
    depends:
      - node: root
      - node: fixer_b
    join: always
  - id: verdict_b
    type: command
    command: "echo verdict_b"
    allowed_exit_codes: [0, 1]
    depends:
      - node: review_b
  - id: fixer_b
    type: command
    command: "echo fixer_b"
    depends:
      - node: verdict_b
        when: "nodes.verdict_b.exit_code == 0"
        counts_loop: loop_b
  - id: brother_cmd
    type: command
    command: "echo brother"
    depends:
      - node: root
  - id: human_a
    type: human
    prompt: "Human A prompt"
    options: ["ok"]
  - id: human_b
    type: human
    prompt: "Human B prompt"
    options: ["ok"]
  - id: final
    type: command
    command: "echo final"
    depends:
      - node: human_a
      - node: human_b
      - node: brother_cmd
`

// singleLoopWithSlowDownstreamYAML defines a human node followed by a slow command node.
const singleLoopWithSlowDownstreamYAML = `
name: single-loop-slow-downstream
loops:
  - id: loop_a
    nodes: [review_a, verdict_a, fixer_a]
    max_iterations: 1
    on_exhausted: human_a
nodes:
  - id: root
    type: command
    command: "echo root"
  - id: review_a
    type: command
    command: "echo review_a"
    depends:
      - node: root
      - node: fixer_a
    join: always
  - id: verdict_a
    type: command
    command: "echo verdict_a"
    allowed_exit_codes: [0, 1]
    depends:
      - node: review_a
  - id: fixer_a
    type: command
    command: "echo fixer_a"
    depends:
      - node: verdict_a
        when: "nodes.verdict_a.exit_code == 0"
        counts_loop: loop_a
  - id: human_a
    type: human
    prompt: "Human A prompt"
    options: ["ok"]
  - id: slow_downstream
    type: command
    command: "echo slow"
    depends:
      - node: human_a
`

func TestEngine_ParallelHumanNodes_ConcurrentSuspensionAndResume_InMemory(t *testing.T) {
	defn, err := workflowspec.ParseDefinition([]byte(parallelDualLoopYAML))
	require.NoError(t, err)

	store := newMemStore()
	engine, rec := newLoopPersistenceEngine(t, NewCommandRunner(false), store)

	runDir := t.TempDir()
	type outcome struct {
		result *WorkflowRunResult
		err    error
	}
	outCh := make(chan outcome, 1)
	go func() {
		res, err := engine.Execute(context.Background(), defn, RunContext{
			SessionID: "chat-dual",
			RunID:     "run-dual",
			RunDir:    runDir,
			Input:     "test",
		})
		outCh <- outcome{result: res, err: err}
	}()

	waitFor(t, func() bool {
		snap := store.get("run-dual")
		return snap != nil && snap.Status == PersistStatusWaitingHuman && len(snap.SuspendedNodes) == 2
	}, "both human nodes should be concurrently suspended")

	snap := store.get("run-dual")
	require.Len(t, snap.SuspendedNodes, 2)
	assert.Contains(t, snap.SuspendedNodes, "human_a")
	assert.Contains(t, snap.SuspendedNodes, "human_b")
	msgA := snap.SuspendedNodes["human_a"].MessageID
	msgB := snap.SuspendedNodes["human_b"].MessageID
	assert.NotEmpty(t, msgA)
	assert.NotEmpty(t, msgB)

	// Resume human_a via DeliverResumeByMessageID in memory
	resA, err := engine.ResumeByMessageID(context.Background(), msgA, "ok", nil)
	require.NoError(t, err)
	assert.Nil(t, resA)

	// Resume human_b via DeliverResumeByMessageID in memory
	resB, err := engine.ResumeByMessageID(context.Background(), msgB, "ok", nil)
	require.NoError(t, err)
	assert.Nil(t, resB)

	out := <-outCh
	require.NoError(t, out.err)
	require.NotNil(t, out.result)
	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Equal(t, PersistStatusCompleted, store.get("run-dual").Status)
	assert.Len(t, rec.all(), 2)
}

func TestEngine_ParallelHumanNodes_RestartReplay(t *testing.T) {
	defn, err := workflowspec.ParseDefinition([]byte(parallelDualLoopYAML))
	require.NoError(t, err)

	store := newMemStore()
	engine1, _ := newLoopPersistenceEngine(t, NewCommandRunner(false), store)

	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() {
		_, _ = engine1.Execute(ctx1, defn, RunContext{
			SessionID: "chat-dual-restart",
			RunID:     "run-dual-restart",
			RunDir:    t.TempDir(),
		})
	}()

	waitFor(t, func() bool {
		snap := store.get("run-dual-restart")
		return snap != nil && snap.Status == PersistStatusWaitingHuman && len(snap.SuspendedNodes) == 2
	}, "dual human nodes suspend")

	snap := store.get("run-dual-restart")
	msgA := snap.SuspendedNodes["human_a"].MessageID
	msgB := snap.SuspendedNodes["human_b"].MessageID

	// Cancel engine1 and restore waiting snapshot (simulate server shutdown)
	cancel1()
	waitFor(t, func() bool {
		return store.get("run-dual-restart").Status == PersistStatusCancelled
	}, "cancelled")
	require.NoError(t, store.MarkWaitingHuman(snap))

	// Recreate fresh engine2 (server restarted)
	engine2, rec2 := newLoopPersistenceEngine(t, NewCommandRunner(false), store)

	// Resume human_a by message ID -> re-drives run; human_b should re-suspend reusing original MessageID
	type outcome struct {
		result *WorkflowRunResult
		err    error
	}
	outCh := make(chan outcome, 1)
	go func() {
		res, err := engine2.ResumeByMessageID(context.Background(), msgA, "ok", nil)
		outCh <- outcome{result: res, err: err}
	}()

	// Wait for engine2 to re-suspend at human_b
	waitFor(t, func() bool {
		s := store.get("run-dual-restart")
		return s != nil && s.Status == PersistStatusWaitingHuman && len(s.SuspendedNodes) == 1 && s.SuspendedNodes["human_b"].MessageID == msgB
	}, "human_b re-suspends with original message ID")

	// Resume human_b in memory on engine2
	resB, err := engine2.ResumeByMessageID(context.Background(), msgB, "ok", nil)
	require.NoError(t, err)
	assert.Nil(t, resB)

	out := <-outCh
	require.NoError(t, out.err)
	require.NotNil(t, out.result)
	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Equal(t, PersistStatusCompleted, store.get("run-dual-restart").Status)
	assert.NotEmpty(t, rec2.all())
}

func TestEngine_ParallelHuman_PartialResume_PrunesSuspendedNodes(t *testing.T) {
	defn, err := workflowspec.ParseDefinition([]byte(parallelDualLoopYAML))
	require.NoError(t, err)

	store := newMemStore()
	engine, _ := newLoopPersistenceEngine(t, NewCommandRunner(false), store)

	go func() {
		_, _ = engine.Execute(context.Background(), defn, RunContext{
			SessionID: "chat-prune",
			RunID:     "run-prune",
			RunDir:    t.TempDir(),
		})
	}()

	waitFor(t, func() bool {
		snap := store.get("run-prune")
		return snap != nil && snap.Status == PersistStatusWaitingHuman && len(snap.SuspendedNodes) == 2
	}, "both suspended")

	snap := store.get("run-prune")
	msgA := snap.SuspendedNodes["human_a"].MessageID
	msgB := snap.SuspendedNodes["human_b"].MessageID

	// Resume human_a in memory
	resA, err := engine.ResumeByMessageID(context.Background(), msgA, "ok", nil)
	require.NoError(t, err)
	assert.Nil(t, resA)

	// Post-Settle should have refreshed store and pruned SuspendedNodes down to human_b
	waitFor(t, func() bool {
		s := store.get("run-prune")
		return s != nil && len(s.SuspendedNodes) == 1 && s.SuspendedNodes["human_b"].MessageID == msgB && s.NodeStates["human_a"].Status == string(workflowspec.StatusSucceeded)
	}, "snap pruned to human_b with human_a succeeded")

	// Resume human_b to complete run
	resB, err := engine.ResumeByMessageID(context.Background(), msgB, "ok", nil)
	require.NoError(t, err)
	assert.Nil(t, resB)

	waitFor(t, func() bool {
		return store.get("run-prune").Status == PersistStatusCompleted
	}, "completed")
}

func TestEngine_Resume_DuplicateReply_NoDoubleExecute(t *testing.T) {
	t.Run("dual human node duplicate reply ignored", func(t *testing.T) {
		defn, err := workflowspec.ParseDefinition([]byte(parallelDualLoopYAML))
		require.NoError(t, err)

		store := newMemStore()
		engine, _ := newLoopPersistenceEngine(t, NewCommandRunner(false), store)

		go func() {
			_, _ = engine.Execute(context.Background(), defn, RunContext{
				SessionID: "chat-dup-dual",
				RunID:     "run-dup-dual",
				RunDir:    t.TempDir(),
			})
		}()

		waitFor(t, func() bool {
			snap := store.get("run-dup-dual")
			return snap != nil && len(snap.SuspendedNodes) == 2
		}, "both suspended")

		snap := store.get("run-dup-dual")
		msgA := snap.SuspendedNodes["human_a"].MessageID
		msgB := snap.SuspendedNodes["human_b"].MessageID

		// Resume human_a
		_, err = engine.ResumeByMessageID(context.Background(), msgA, "ok", nil)
		require.NoError(t, err)

		// Send duplicate reply for human_a while human_b is still waiting
		resDup, errDup := engine.ResumeByMessageID(context.Background(), msgA, "duplicate", nil)
		require.NoError(t, errDup)
		assert.Nil(t, resDup, "duplicate reply should be safely ignored and not trigger re-execution")

		// Resume human_b
		_, err = engine.ResumeByMessageID(context.Background(), msgB, "ok", nil)
		require.NoError(t, err)

		waitFor(t, func() bool {
			snap := store.get("run-dup-dual")
			return snap != nil && snap.Status == PersistStatusCompleted
		}, "settles completed")
	})

	t.Run("single human node executing guard ignores late resume", func(t *testing.T) {
		slowRunner := &slowNodeRunner{delay: 100 * time.Millisecond}
		defn, err := workflowspec.ParseDefinition([]byte(singleLoopWithSlowDownstreamYAML))
		require.NoError(t, err)

		store := newMemStore()
		registry := NewNodeRunnerRegistry()
		registry.Register(slowRunner)
		rec := &suspendRecorder{}
		engine := NewEngine(registry)
		engine.SetRunStore(store)
		engine.SetHumanSuspender(func(req SuspendRequest) error {
			rec.record(req)
			return nil
		})

		go func() {
			_, _ = engine.Execute(context.Background(), defn, RunContext{
				SessionID: "chat-dup-slow",
				RunID:     "run-dup-slow",
				RunDir:    t.TempDir(),
			})
		}()

		waitFor(t, func() bool {
			snap := store.get("run-dup-slow")
			return snap != nil && snap.Status == PersistStatusWaitingHuman
		}, "suspended")

		snap := store.get("run-dup-slow")
		msgA := snap.SuspendedMessageID

		// Resume human_a: wakes worker and starts executing slow_downstream
		_, err = engine.ResumeByMessageID(context.Background(), msgA, "ok", nil)
		require.NoError(t, err)

		// While slow_downstream is actively running, attempt duplicate ResumeByMessageID and ResumeWithEmitter
		resDup1, errDup1 := engine.ResumeByMessageID(context.Background(), msgA, "duplicate", nil)
		require.NoError(t, errDup1)
		assert.Nil(t, resDup1)

		resDup2, errDup2 := engine.ResumeWithEmitter(context.Background(), "run-dup-slow", "duplicate", nil)
		assert.Error(t, errDup2)
		assert.Nil(t, resDup2)
		assert.Contains(t, errDup2.Error(), "active or has multiple pending human nodes")

		waitFor(t, func() bool {
			return store.get("run-dup-slow").Status == PersistStatusCompleted
		}, "settles completed")
	})
}

type slowNodeRunner struct {
	delay time.Duration
}

func (s *slowNodeRunner) Supports(t workflowspec.NodeType) bool {
	return t == workflowspec.NodeTypeCommand
}

func (s *slowNodeRunner) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	time.Sleep(s.delay)
	return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded, Output: "slow done"}, nil
}

func TestEngine_HumanNode_FastReplyRace(t *testing.T) {
	defn, err := workflowspec.ParseDefinition([]byte(fallbackResumeYAML))
	require.NoError(t, err)

	store := newMemStore()
	engine, _ := newLoopPersistenceEngine(t, newLoopCountingRunner(passAfterVerdictRuns()), store)

	// Pre-fill a fast reply or deliver synchronously inside suspend callback
	var once sync.Once
	engine.SetHumanSuspender(func(req SuspendRequest) error {
		once.Do(func() {
			go func() {
				// Fast reply racing right as waiter registered
				_ = engine.DeliverResumeByMessageID(req.MessageID, "Retry (reset counter)")
			}()
		})
		return nil
	})

	runDir := t.TempDir()
	res, err := engine.Execute(context.Background(), defn, RunContext{
		SessionID: "chat-fast-race",
		RunID:     "run-fast-race",
		RunDir:    runDir,
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, RunStatusCompleted, res.Status)
	assert.Equal(t, PersistStatusCompleted, store.get("run-fast-race").Status)
}

func TestEngine_Resume_ConcurrentReplyDuringReplay(t *testing.T) {
	defn, err := workflowspec.ParseDefinition([]byte(parallelDualLoopYAML))
	require.NoError(t, err)

	store := newMemStore()
	engine1, _ := newLoopPersistenceEngine(t, NewCommandRunner(false), store)

	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() {
		_, _ = engine1.Execute(ctx1, defn, RunContext{
			SessionID: "chat-concurrent-replay",
			RunID:     "run-concurrent-replay",
			RunDir:    t.TempDir(),
		})
	}()

	waitFor(t, func() bool {
		snap := store.get("run-concurrent-replay")
		return snap != nil && len(snap.SuspendedNodes) == 2
	}, "dual suspended")

	snap := store.get("run-concurrent-replay")
	msgA := snap.SuspendedNodes["human_a"].MessageID
	msgB := snap.SuspendedNodes["human_b"].MessageID

	cancel1()
	waitFor(t, func() bool {
		return store.get("run-concurrent-replay").Status == PersistStatusCancelled
	}, "cancelled")
	require.NoError(t, store.MarkWaitingHuman(snap))

	// Recreate engine2
	engine2, _ := newLoopPersistenceEngine(t, NewCommandRunner(false), store)

	// Concurrently start replay for msgA and deliver reply for msgB during replayPending phase
	go func() {
		time.Sleep(5 * time.Millisecond)
		_, _ = engine2.ResumeByMessageID(context.Background(), msgB, "ok", nil)
	}()

	resA, err := engine2.ResumeByMessageID(context.Background(), msgA, "ok", nil)
	require.NoError(t, err)
	require.NotNil(t, resA)
	assert.Equal(t, RunStatusCompleted, resA.Status)
	assert.Equal(t, PersistStatusCompleted, store.get("run-concurrent-replay").Status)
}

func TestEngine_ParallelHuman_BrotherNodeSettlement_Persisted(t *testing.T) {
	brotherExecCount := 0
	brotherRunner := &brotherCountingRunner{count: &brotherExecCount}

	defn, err := workflowspec.ParseDefinition([]byte(parallelDualLoopWithBrotherCommandYAML))
	require.NoError(t, err)

	store := newMemStore()
	registry := NewNodeRunnerRegistry()
	registry.Register(brotherRunner)
	engine1 := NewEngine(registry)
	engine1.SetRunStore(store)
	engine1.SetHumanSuspender(func(req SuspendRequest) error {
		return nil
	})

	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() {
		_, _ = engine1.Execute(ctx1, defn, RunContext{
			SessionID: "chat-brother",
			RunID:     "run-brother",
			RunDir:    t.TempDir(),
		})
	}()

	waitFor(t, func() bool {
		snap := store.get("run-brother")
		return snap != nil && len(snap.SuspendedNodes) == 2 && snap.NodeStates["brother_cmd"].Status == string(workflowspec.StatusSucceeded)
	}, "both suspended and brother_cmd settled in snapshot")

	snap := store.get("run-brother")
	require.Equal(t, string(workflowspec.StatusSucceeded), snap.NodeStates["brother_cmd"].Status)
	msgA := snap.SuspendedNodes["human_a"].MessageID
	msgB := snap.SuspendedNodes["human_b"].MessageID

	cancel1()
	waitFor(t, func() bool {
		return store.get("run-brother").Status == PersistStatusCancelled
	}, "cancelled")
	require.NoError(t, store.MarkWaitingHuman(snap))

	assert.Equal(t, 1, brotherExecCount)

	// Recreate engine2 and resume
	engine2 := NewEngine(registry)
	engine2.SetRunStore(store)
	engine2.SetHumanSuspender(func(req SuspendRequest) error {
		return nil
	})

	type outcome struct {
		result *WorkflowRunResult
		err    error
	}
	outCh := make(chan outcome, 1)
	go func() {
		res, err := engine2.ResumeByMessageID(context.Background(), msgA, "ok", nil)
		outCh <- outcome{result: res, err: err}
	}()

	waitFor(t, func() bool {
		s := store.get("run-brother")
		return s != nil && s.Status == PersistStatusWaitingHuman && len(s.SuspendedNodes) == 1
	}, "human_b waiting on engine2")

	_, err = engine2.ResumeByMessageID(context.Background(), msgB, "ok", nil)
	require.NoError(t, err)

	out := <-outCh
	require.NoError(t, out.err)
	require.NotNil(t, out.result)
	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Equal(t, 1, brotherExecCount, "brother_cmd should not have been re-executed during replay")
}

type brotherCountingRunner struct {
	count *int
}

func (b *brotherCountingRunner) Supports(t workflowspec.NodeType) bool {
	return t == workflowspec.NodeTypeCommand
}

func (b *brotherCountingRunner) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	if nctx.Node.ID == "brother_cmd" {
		*b.count++
	}
	return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded, Output: "done"}, nil
}

// parallelTripleLoopYAML defines a DAG with three parallel loops, each exhausting to an on_exhausted orphan human node.
const parallelTripleLoopYAML = `
name: parallel-triple-loop
loops:
  - id: loop_a
    nodes: [review_a, verdict_a, fixer_a]
    max_iterations: 1
    on_exhausted: human_a
  - id: loop_b
    nodes: [review_b, verdict_b, fixer_b]
    max_iterations: 1
    on_exhausted: human_b
  - id: loop_c
    nodes: [review_c, verdict_c, fixer_c]
    max_iterations: 1
    on_exhausted: human_c
nodes:
  - id: root
    type: command
    command: "echo root"
  - id: review_a
    type: command
    command: "echo review_a"
    depends:
      - node: root
      - node: fixer_a
      - node: human_a
        when: "nodes.human_a.output == 'retry'"
        resets_loop: loop_a
    join: always
  - id: verdict_a
    type: command
    command: "echo verdict_a"
    allowed_exit_codes: [0, 1]
    depends:
      - node: review_a
  - id: fixer_a
    type: command
    command: "echo fixer_a"
    depends:
      - node: verdict_a
        when: "nodes.verdict_a.exit_code == 0"
        counts_loop: loop_a
  - id: review_b
    type: command
    command: "echo review_b"
    depends:
      - node: root
      - node: fixer_b
      - node: human_b
        when: "nodes.human_b.output == 'retry'"
        resets_loop: loop_b
    join: always
  - id: verdict_b
    type: command
    command: "echo verdict_b"
    allowed_exit_codes: [0, 1]
    depends:
      - node: review_b
  - id: fixer_b
    type: command
    command: "echo fixer_b"
    depends:
      - node: verdict_b
        when: "nodes.verdict_b.exit_code == 0"
        counts_loop: loop_b
  - id: review_c
    type: command
    command: "echo review_c"
    depends:
      - node: root
      - node: fixer_c
      - node: human_c
        when: "nodes.human_c.output == 'retry'"
        resets_loop: loop_c
    join: always
  - id: verdict_c
    type: command
    command: "echo verdict_c"
    allowed_exit_codes: [0, 1]
    depends:
      - node: review_c
  - id: fixer_c
    type: command
    command: "echo fixer_c"
    depends:
      - node: verdict_c
        when: "nodes.verdict_c.exit_code == 0"
        counts_loop: loop_c
  - id: human_a
    type: human
    prompt: "Human A prompt"
    options: ["ok", "retry"]
  - id: human_b
    type: human
    prompt: "Human B prompt"
    options: ["ok", "retry"]
  - id: human_c
    type: human
    prompt: "Human C prompt"
    options: ["ok", "retry"]
  - id: final
    type: command
    command: "echo final"
    depends:
      - node: human_a
      - node: human_b
      - node: human_c
`

func TestResumeConcurrentReSuspensionThreeWaiters(t *testing.T) {
	defn, err := workflowspec.ParseDefinition([]byte(parallelTripleLoopYAML))
	require.NoError(t, err)

	store := newMemStore()
	engine1, _ := newLoopPersistenceEngine(t, NewCommandRunner(false), store)

	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() {
		_, _ = engine1.Execute(ctx1, defn, RunContext{
			SessionID: "chat-triple",
			RunID:     "run-triple",
			RunDir:    t.TempDir(),
		})
	}()

	waitFor(t, func() bool {
		snap := store.get("run-triple")
		return snap != nil && snap.Status == PersistStatusWaitingHuman && len(snap.SuspendedNodes) == 3
	}, "all three human nodes suspended")

	snap := store.get("run-triple")
	require.Len(t, snap.SuspendedNodes, 3)
	msgA := snap.SuspendedNodes["human_a"].MessageID
	msgB := snap.SuspendedNodes["human_b"].MessageID
	msgC := snap.SuspendedNodes["human_c"].MessageID
	require.NotEmpty(t, msgA)
	require.NotEmpty(t, msgB)
	require.NotEmpty(t, msgC)

	// Simulate server shutdown / cancellation
	cancel1()
	waitFor(t, func() bool {
		return store.get("run-triple").Status == PersistStatusCancelled
	}, "cancelled")
	require.NoError(t, store.MarkWaitingHuman(snap))

	// Recreate engine2 (server restarted)
	engine2, _ := newLoopPersistenceEngine(t, NewCommandRunner(false), store)

	type outcome struct {
		result *WorkflowRunResult
		err    error
	}
	outCh := make(chan outcome, 1)
	go func() {
		res, err := engine2.ResumeByMessageID(context.Background(), msgA, "ok", nil)
		outCh <- outcome{result: res, err: err}
	}()

	// Wait for engine2 to re-suspend at human_b and human_c concurrently
	waitFor(t, func() bool {
		s := store.get("run-triple")
		return s != nil && s.Status == PersistStatusWaitingHuman && len(s.SuspendedNodes) == 2 &&
			s.SuspendedNodes["human_b"].MessageID == msgB &&
			s.SuspendedNodes["human_c"].MessageID == msgC
	}, "human_b and human_c re-suspend concurrently with preserved message IDs")

	// Resume human_b in memory on engine2
	resB, err := engine2.ResumeByMessageID(context.Background(), msgB, "ok", nil)
	require.NoError(t, err)
	assert.Nil(t, resB)

	// Wait for store to update to only human_c suspended
	waitFor(t, func() bool {
		s := store.get("run-triple")
		return s != nil && s.Status == PersistStatusWaitingHuman && len(s.SuspendedNodes) == 1 &&
			s.SuspendedNodes["human_c"].MessageID == msgC
	}, "human_c remaining suspended")

	// Resume human_c in memory on engine2
	resC, err := engine2.ResumeByMessageID(context.Background(), msgC, "ok", nil)
	require.NoError(t, err)
	assert.Nil(t, resC)

	out := <-outCh
	require.NoError(t, out.err)
	require.NotNil(t, out.result)
	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Equal(t, PersistStatusCompleted, store.get("run-triple").Status)
}
