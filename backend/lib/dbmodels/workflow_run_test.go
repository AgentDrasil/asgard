package dbmodels

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
)

func TestWorkflowRunRepository(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	dir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string { return filepath.Join(dir, sessionID) })

	// GetRun of a non-existent run returns nil, nil.
	run, err := repo.GetRun("missing")
	require.NoError(t, err)
	assert.Nil(t, run)

	// SaveRun upserts by run_id.
	states, err := EncodeNodeStates(map[string]NodeState{
		"prep": {Status: "SUCCEEDED", ExitCode: 0},
	})
	require.NoError(t, err)
	loopIters, err := EncodeIntMap(map[string]int{"fix_loop": 2})
	require.NoError(t, err)
	execCounts, err := EncodeIntMap(map[string]int{"fixer": 2, "fix_fallback": 1})
	require.NoError(t, err)

	run1 := &WorkflowRun{
		RunID:              "run-1",
		SessionID:          "chat-1",
		Status:             WorkflowStatusRunning,
		DAGSpec:            "name: wf\n",
		NodeStates:         states,
		LoopIterations:     loopIters,
		ExecutionCounts:    execCounts,
		SuspendedNodeID:    "approval",
		SuspendedMessageID: "wf-run-1-approval",
	}
	require.NoError(t, repo.SaveRun(run1))

	loaded, err := repo.GetRun("run-1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, WorkflowStatusRunning, loaded.Status)
	assert.Equal(t, "wf-run-1-approval", loaded.SuspendedMessageID)

	decoded, err := DecodeNodeStates(loaded.NodeStates)
	require.NoError(t, err)
	assert.Equal(t, "SUCCEEDED", decoded["prep"].Status)

	decodedLoops, err := DecodeIntMap(loaded.LoopIterations)
	require.NoError(t, err)
	assert.Equal(t, 2, decodedLoops["fix_loop"])
	decodedExec, err := DecodeIntMap(loaded.ExecutionCounts)
	require.NoError(t, err)
	assert.Equal(t, 2, decodedExec["fixer"])
	assert.Equal(t, 1, decodedExec["fix_fallback"])

	// No waiting run yet.
	waiting, err := repo.FindWaitingHumanBySession("chat-1")
	require.NoError(t, err)
	assert.Nil(t, waiting)

	// Transition to WAITING_HUMAN.
	loaded.Status = WorkflowStatusWaitingHuman
	require.NoError(t, repo.SaveRun(loaded))

	waiting, err = repo.FindWaitingHumanBySession("chat-1")
	require.NoError(t, err)
	require.NotNil(t, waiting)
	assert.Equal(t, "run-1", waiting.RunID)
	assert.Equal(t, "approval", waiting.SuspendedNodeID)

	// Waiting runs of other sessions are not matched.
	waiting, err = repo.FindWaitingHumanBySession("chat-2")
	require.NoError(t, err)
	assert.Nil(t, waiting)

	loaded.Status = WorkflowStatusCompleted
	require.NoError(t, repo.SaveRun(loaded))
	waiting, err = repo.FindWaitingHumanBySession("chat-1")
	require.NoError(t, err)
	assert.Nil(t, waiting)
}

func TestWorkflowRun_StartupCleanup_RunningToFailed(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	dir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string { return filepath.Join(dir, sessionID) })

	// Seed 3 runs: RUNNING, WAITING_HUMAN, COMPLETED
	require.NoError(t, repo.SaveRun(&WorkflowRun{
		RunID:     "run-running-1",
		SessionID: "chat-1",
		Status:    WorkflowStatusRunning,
	}))
	require.NoError(t, repo.SaveRun(&WorkflowRun{
		RunID:     "run-running-2",
		SessionID: "chat-2",
		Status:    WorkflowStatusRunning,
	}))
	require.NoError(t, repo.SaveRun(&WorkflowRun{
		RunID:              "run-waiting",
		SessionID:          "chat-1",
		Status:             WorkflowStatusWaitingHuman,
		SuspendedNodeID:    "node-a",
		SuspendedMessageID: "wf-run-waiting-node-a",
	}))
	require.NoError(t, repo.SaveRun(&WorkflowRun{
		RunID:     "run-completed",
		SessionID: "chat-1",
		Status:    WorkflowStatusCompleted,
	}))

	// Execute startup reset
	require.NoError(t, repo.ResetAllRunningWorkflows())

	// Assertions
	r1, err := repo.GetRun("run-running-1")
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusFailed, r1.Status)

	r2, err := repo.GetRun("run-running-2")
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusFailed, r2.Status)

	rw, err := repo.GetRun("run-waiting")
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusWaitingHuman, rw.Status)

	rc, err := repo.GetRun("run-completed")
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, rc.Status)
}

func TestWorkflowRunRepository_FindWaitingHumansBySession(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	dir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string { return filepath.Join(dir, sessionID) })

	mkRun := func(runID, status string) {
		require.NoError(t, repo.SaveRun(&WorkflowRun{
			RunID:     runID,
			SessionID: "chat-1",
			Status:    status,
			DAGSpec:   "name: wf\n",
		}))
	}
	mkRun("run-a", WorkflowStatusWaitingHuman)
	mkRun("run-b", WorkflowStatusRunning)
	mkRun("run-c", WorkflowStatusWaitingHuman)
	mkRun("run-d", WorkflowStatusCompleted)
	mkRun("run-e", WorkflowStatusWaitingHuman)

	runs, err := repo.FindWaitingHumansBySession("chat-1")
	require.NoError(t, err)
	require.Len(t, runs, 3)
	ids := []string{runs[0].RunID, runs[1].RunID, runs[2].RunID}
	assert.ElementsMatch(t, []string{"run-a", "run-c", "run-e"}, ids)
	for _, run := range runs {
		assert.Equal(t, WorkflowStatusWaitingHuman, run.Status)
	}

	other, err := repo.FindWaitingHumansBySession("chat-2")
	require.NoError(t, err)
	assert.Empty(t, other)
}

func TestWorkflowRunRepository_FindWaitingHumanByMessageID(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	dir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string { return filepath.Join(dir, sessionID) })

	suspendedNodes, err := EncodeSuspendedNodes(map[string]SuspendedNodeInfo{
		"review": {MessageID: "wf-run-1-review", Iteration: 1},
	})
	require.NoError(t, err)

	// Matched via the dedicated suspended_message_id column.
	require.NoError(t, repo.SaveRun(&WorkflowRun{
		RunID:              "run-1",
		SessionID:          "chat-1",
		Status:             WorkflowStatusWaitingHuman,
		DAGSpec:            "name: wf\n",
		SuspendedNodeID:    "approval",
		SuspendedMessageID: "wf-run-1-approval",
		SuspendedNodes:     suspendedNodes,
	}))

	run, err := repo.FindWaitingHumanByMessageID("wf-run-1-approval")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, "run-1", run.RunID)

	// Matched via the suspended_nodes JSON payload.
	run, err = repo.FindWaitingHumanByMessageID("wf-run-1-review")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, "run-1", run.RunID)

	// Not found returns nil, nil.
	run, err = repo.FindWaitingHumanByMessageID("wf-missing")
	require.NoError(t, err)
	assert.Nil(t, run)

	// Settled runs are never matched.
	saved, err := repo.GetRun("run-1")
	require.NoError(t, err)
	require.NotNil(t, saved)
	saved.Status = WorkflowStatusCompleted
	require.NoError(t, repo.SaveRun(saved))
	run, err = repo.FindWaitingHumanByMessageID("wf-run-1-approval")
	require.NoError(t, err)
	assert.Nil(t, run)
}

func TestWorkflowRunRepository_FindWaitingHumanByMessageID_PrefixCollision(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	dir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string { return filepath.Join(dir, sessionID) })

	nodesA, err := EncodeSuspendedNodes(map[string]SuspendedNodeInfo{
		"review":   {MessageID: "wf-r1-node", Iteration: 1},
		"review_2": {MessageID: "wf-r1-node_2", Iteration: 1},
	})
	require.NoError(t, err)
	nodesB, err := EncodeSuspendedNodes(map[string]SuspendedNodeInfo{
		"review": {MessageID: "wf-r1-node-2", Iteration: 1},
	})
	require.NoError(t, err)

	require.NoError(t, repo.SaveRun(&WorkflowRun{
		RunID:          "r1",
		SessionID:      "chat-1",
		Status:         WorkflowStatusWaitingHuman,
		SuspendedNodes: nodesA,
	}))
	require.NoError(t, repo.SaveRun(&WorkflowRun{
		RunID:          "r2",
		SessionID:      "chat-1",
		Status:         WorkflowStatusWaitingHuman,
		SuspendedNodes: nodesB,
	}))

	// Exact match resolves to the owning run, not a prefix sibling.
	run, err := repo.FindWaitingHumanByMessageID("wf-r1-node")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, "r1", run.RunID)

	run, err = repo.FindWaitingHumanByMessageID("wf-r1-node-2")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, "r2", run.RunID)

	run, err = repo.FindWaitingHumanByMessageID("wf-r1-node_2")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, "r1", run.RunID)

	// Underscore is escaped, so it does not act as a single-char wildcard.
	run, err = repo.FindWaitingHumanByMessageID("wf-r1-nodeX2")
	require.NoError(t, err)
	assert.Nil(t, run)
}

func TestWorkflowRunRepository_RefreshSuspension(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	dir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string { return filepath.Join(dir, sessionID) })

	initialNodes, err := EncodeSuspendedNodes(map[string]SuspendedNodeInfo{
		"plan_approval": {MessageID: "wf-run-1-plan_approval", Iteration: 1},
		"code_review":   {MessageID: "wf-run-1-code_review", Iteration: 1},
	})
	require.NoError(t, err)
	require.NoError(t, repo.SaveRun(&WorkflowRun{
		RunID:              "run-1",
		SessionID:          "chat-1",
		Status:             WorkflowStatusWaitingHuman,
		DAGSpec:            "name: wf\n",
		NodeStates:         "{}",
		SuspendedNodeID:    "plan_approval",
		SuspendedMessageID: "wf-run-1-plan_approval",
		SuspendedNodes:     initialNodes,
	}))

	// plan_approval settles; the suspension set is pruned to code_review.
	states, err := EncodeNodeStates(map[string]NodeState{
		"prep":          {Status: "SUCCEEDED"},
		"plan_approval": {Status: "SUCCEEDED", ExitCode: 0},
	})
	require.NoError(t, err)
	decodedStates, err := DecodeNodeStates(states)
	require.NoError(t, err)

	require.NoError(t, repo.RefreshSuspension(
		"run-1",
		decodedStates,
		map[string]int{"fix_loop": 3},
		map[string]int{"fixer": 3},
		map[string]SuspendedNodeInfo{
			"code_review": {MessageID: "wf-run-1-code_review", Iteration: 1},
		},
	))

	run, err := repo.GetRun("run-1")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, WorkflowStatusWaitingHuman, run.Status)

	gotStates, err := DecodeNodeStates(run.NodeStates)
	require.NoError(t, err)
	assert.Len(t, gotStates, 2)
	assert.Equal(t, "SUCCEEDED", gotStates["plan_approval"].Status)

	loops, err := DecodeIntMap(run.LoopIterations)
	require.NoError(t, err)
	assert.Equal(t, 3, loops["fix_loop"])
	counts, err := DecodeIntMap(run.ExecutionCounts)
	require.NoError(t, err)
	assert.Equal(t, 3, counts["fixer"])

	nodes, err := DecodeSuspendedNodes(run.SuspendedNodes)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "wf-run-1-code_review", nodes["code_review"].MessageID)

	// The pruned node's compat columns are replaced by the remaining node.
	assert.Equal(t, "code_review", run.SuspendedNodeID)
	assert.Equal(t, "wf-run-1-code_review", run.SuspendedMessageID)

	// Refreshing with an empty set clears the compat columns.
	require.NoError(t, repo.RefreshSuspension("run-1", decodedStates, nil, nil, nil))
	run, err = repo.GetRun("run-1")
	require.NoError(t, err)
	assert.Empty(t, run.SuspendedNodeID)
	assert.Empty(t, run.SuspendedMessageID)

	// Refreshing an unknown run fails.
	err = repo.RefreshSuspension("missing", decodedStates, nil, nil, nil)
	assert.Error(t, err)
}

func TestSuspendedNodes_EncodeDecode(t *testing.T) {
	nodes := map[string]SuspendedNodeInfo{
		"plan_approval": {MessageID: "wf-run-1-plan_approval", Iteration: 2},
		"code_review":   {MessageID: "wf-run-1-code_review", Iteration: 1},
	}
	raw, err := EncodeSuspendedNodes(nodes)
	require.NoError(t, err)
	assert.Contains(t, raw, `"message_id":"wf-run-1-plan_approval"`)
	assert.Contains(t, raw, `"iteration":2`)

	decoded, err := DecodeSuspendedNodes(raw)
	require.NoError(t, err)
	assert.Equal(t, nodes, decoded)

	// Empty inputs round-trip to empty maps.
	raw, err = EncodeSuspendedNodes(nil)
	require.NoError(t, err)
	assert.Equal(t, "{}", raw)
	decoded, err = DecodeSuspendedNodes("")
	require.NoError(t, err)
	assert.Empty(t, decoded)

	// Invalid JSON fails decoding.
	_, err = DecodeSuspendedNodes("{invalid")
	assert.Error(t, err)
}

func TestWorkflowRunRepository_HasRunningRunBySession(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	dir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string { return filepath.Join(dir, sessionID) })

	hasRunning, err := repo.HasRunningRunBySession("chat-1")
	require.NoError(t, err)
	assert.False(t, hasRunning)

	require.NoError(t, repo.SaveRun(&WorkflowRun{
		RunID:     "run-1",
		SessionID: "chat-1",
		Status:    WorkflowStatusRunning,
	}))

	hasRunning, err = repo.HasRunningRunBySession("chat-1")
	require.NoError(t, err)
	assert.True(t, hasRunning)

	hasRunning, err = repo.HasRunningRunBySession("chat-2")
	require.NoError(t, err)
	assert.False(t, hasRunning)

	require.NoError(t, repo.SaveRun(&WorkflowRun{
		RunID:     "run-1",
		SessionID: "chat-1",
		Status:    WorkflowStatusCompleted,
	}))

	hasRunning, err = repo.HasRunningRunBySession("chat-1")
	require.NoError(t, err)
	assert.False(t, hasRunning)
}

func TestWorkflowRunRepository_UpdateRunStatus(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	dir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string { return filepath.Join(dir, sessionID) })

	require.NoError(t, repo.SaveRun(&WorkflowRun{
		RunID:     "run-status-1",
		SessionID: "chat-status",
		Status:    WorkflowStatusWaitingHuman,
	}))

	hasRunning, err := repo.HasRunningRunBySession("chat-status")
	require.NoError(t, err)
	assert.False(t, hasRunning)

	require.NoError(t, repo.UpdateRunStatus("run-status-1", WorkflowStatusRunning))

	run, err := repo.GetRun("run-status-1")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, WorkflowStatusRunning, run.Status)

	hasRunning, err = repo.HasRunningRunBySession("chat-status")
	require.NoError(t, err)
	assert.True(t, hasRunning)
}

func TestWorkflowRun_OffloadLargeSpecAndOutput(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string {
		return filepath.Join(tempDir, sessionID)
	})

	largeDAG := "name: big-flow\n" + strings.Repeat("  key: value\n", 500)
	largeInput := strings.Repeat("user input line\n", 500)
	largeOutput := strings.Repeat("step output log text\n", 500)

	states, err := EncodeNodeStates(map[string]NodeState{
		"step1": {Status: "SUCCEEDED", ExitCode: 0, Output: largeOutput},
	})
	require.NoError(t, err)

	run := &WorkflowRun{
		RunID:      "run-large-1",
		SessionID:  "chat-large",
		Status:     WorkflowStatusRunning,
		DAGSpec:    largeDAG,
		Input:      largeInput,
		NodeStates: states,
	}

	require.NoError(t, repo.SaveRun(run))

	// Assert on DB row directly: DAGSpec and Input are gorm:"-", DB paths are non-empty, Output in JSON is empty
	var rawRun WorkflowRun
	require.NoError(t, testDB.First(&rawRun, "run_id = ?", "run-large-1").Error)

	assert.Empty(t, rawRun.DAGSpec, "DAGSpec in DB memory struct must be empty before hydration")
	assert.Empty(t, rawRun.Input, "Input in DB memory struct must be empty before hydration")
	assert.NotEmpty(t, rawRun.DAGSpecPath, "DAGSpecPath must be stored in DB")
	assert.NotEmpty(t, rawRun.InputPath, "InputPath must be stored in DB")

	// Verify physical offloaded files exist
	dagContent, err := os.ReadFile(rawRun.DAGSpecPath)
	require.NoError(t, err)
	assert.Equal(t, largeDAG, string(dagContent))

	inContent, err := os.ReadFile(rawRun.InputPath)
	require.NoError(t, err)
	assert.Equal(t, largeInput, string(inContent))

	rawStates, err := DecodeNodeStates(rawRun.NodeStates)
	require.NoError(t, err)
	assert.Empty(t, rawStates["step1"].Output, "Node Output in DB JSON must be cleared/empty")
	assert.NotEmpty(t, rawStates["step1"].OutputPath, "OutputPath in DB JSON must point to offloaded log")

	outContent, err := os.ReadFile(rawStates["step1"].OutputPath)
	require.NoError(t, err)
	assert.Equal(t, largeOutput, string(outContent))
}

func TestWorkflowRun_HydrateOnRead(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string {
		return filepath.Join(tempDir, sessionID)
	})

	expectedDAG := "name: hydrate-flow\n"
	expectedInput := "input data for test"
	expectedOutput := "node 1 log output"

	states, err := EncodeNodeStates(map[string]NodeState{
		"node1": {Status: "SUCCEEDED", ExitCode: 0, Output: expectedOutput},
	})
	require.NoError(t, err)

	run := &WorkflowRun{
		RunID:      "run-hydrate-1",
		SessionID:  "chat-hydrate",
		Status:     WorkflowStatusWaitingHuman,
		DAGSpec:    expectedDAG,
		Input:      expectedInput,
		NodeStates: states,
	}
	require.NoError(t, repo.SaveRun(run))

	// GetRun automatically hydrates DAGSpec, Input, and node Outputs
	hydrated, err := repo.GetRun("run-hydrate-1")
	require.NoError(t, err)
	require.NotNil(t, hydrated)

	assert.Equal(t, expectedDAG, hydrated.DAGSpec, "DAGSpec must be hydrated seamlessly from file")
	assert.Equal(t, expectedInput, hydrated.Input, "Input must be hydrated seamlessly from file")

	hydratedStates, err := DecodeNodeStates(hydrated.NodeStates)
	require.NoError(t, err)
	assert.Equal(t, expectedOutput, hydratedStates["node1"].Output, "Node Output must be hydrated seamlessly from file")
}

func TestWorkflowRun_HydrateMissingFileError(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string {
		return filepath.Join(tempDir, sessionID)
	})

	states, err := EncodeNodeStates(map[string]NodeState{
		"node1": {Status: "SUCCEEDED", ExitCode: 0, Output: "some logs"},
	})
	require.NoError(t, err)

	run := &WorkflowRun{
		RunID:      "run-missing-file-1",
		SessionID:  "chat-missing-file",
		Status:     WorkflowStatusWaitingHuman,
		DAGSpec:    "name: missing-flow\n",
		Input:      "some input",
		NodeStates: states,
	}
	require.NoError(t, repo.SaveRun(run))

	// Corrupt: delete dag_spec.yaml
	var rawRun WorkflowRun
	require.NoError(t, testDB.First(&rawRun, "run_id = ?", "run-missing-file-1").Error)
	require.NoError(t, os.Remove(rawRun.DAGSpecPath))

	_, err = repo.GetRun("run-missing-file-1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrOffloadedFileMissing), "Must return ErrOffloadedFileMissing when dag_spec is missing")

	// Corrupt node log test
	run2 := &WorkflowRun{
		RunID:      "run-missing-file-2",
		SessionID:  "chat-missing-file",
		Status:     WorkflowStatusWaitingHuman,
		DAGSpec:    "name: missing-flow-2\n",
		NodeStates: states,
	}
	require.NoError(t, repo.SaveRun(run2))

	var rawRun2 WorkflowRun
	require.NoError(t, testDB.First(&rawRun2, "run_id = ?", "run-missing-file-2").Error)
	rawStates2, err := DecodeNodeStates(rawRun2.NodeStates)
	require.NoError(t, err)
	require.NoError(t, os.Remove(rawStates2["node1"].OutputPath))

	_, err = repo.GetRun("run-missing-file-2")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrOffloadedFileMissing), "Must return ErrOffloadedFileMissing when node log is missing")
}

func TestWorkflowRun_RefreshSuspension_Offload(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(sessionID string) string {
		return filepath.Join(tempDir, sessionID)
	})

	run := &WorkflowRun{
		RunID:      "run-refresh-offload",
		SessionID:  "chat-refresh-offload",
		Status:     WorkflowStatusWaitingHuman,
		DAGSpec:    "name: refresh-flow\n",
		NodeStates: "{}",
	}
	require.NoError(t, repo.SaveRun(run))

	freshStates := map[string]NodeState{
		"task1": {Status: "SUCCEEDED", ExitCode: 0, Output: "task 1 log content"},
		"task2": {Status: "WAITING_HUMAN", Output: "task 2 intermediate prompt"},
	}

	require.NoError(t, repo.RefreshSuspension(
		"run-refresh-offload",
		freshStates,
		map[string]int{"loop_a": 1},
		map[string]int{"task1": 1},
		map[string]SuspendedNodeInfo{
			"task2": {MessageID: "wf-msg-task2", Iteration: 1},
		},
	))

	// Verify DB state is slim
	var rawRun WorkflowRun
	require.NoError(t, testDB.First(&rawRun, "run_id = ?", "run-refresh-offload").Error)
	rawStates, err := DecodeNodeStates(rawRun.NodeStates)
	require.NoError(t, err)
	assert.Empty(t, rawStates["task1"].Output, "task1 output in DB JSON must be cleared")
	assert.NotEmpty(t, rawStates["task1"].OutputPath, "task1 output_path in DB JSON must exist")
	assert.Empty(t, rawStates["task2"].Output, "task2 output in DB JSON must be cleared")
	assert.NotEmpty(t, rawStates["task2"].OutputPath, "task2 output_path in DB JSON must exist")

	// Verify files have the log contents
	task1Log, err := os.ReadFile(rawStates["task1"].OutputPath)
	require.NoError(t, err)
	assert.Equal(t, "task 1 log content", string(task1Log))

	// Verify GetRun rehydrates both
	hydrated, err := repo.GetRun("run-refresh-offload")
	require.NoError(t, err)
	require.NotNil(t, hydrated)
	hydratedStates, err := DecodeNodeStates(hydrated.NodeStates)
	require.NoError(t, err)
	assert.Equal(t, "task 1 log content", hydratedStates["task1"].Output)
	assert.Equal(t, "task 2 intermediate prompt", hydratedStates["task2"].Output)
}

func TestAutoMigrate_BackfillLegacyWorkflowRuns(t *testing.T) {
	testDB := db.NewDBForTest(t)

	// Create legacy table schema manually with dag_spec and input columns directly in the DB
	err := testDB.Exec(`
		CREATE TABLE workflow_runs (
			run_id VARCHAR(64) PRIMARY KEY,
			session_id VARCHAR(64) NOT NULL,
			status VARCHAR(32) NOT NULL,
			dag_spec TEXT,
			input TEXT,
			node_states TEXT,
			created_at DATETIME,
			updated_at DATETIME
		)
	`).Error
	require.NoError(t, err)

	statesJSON, err := EncodeNodeStates(map[string]NodeState{
		"step1": {Status: "SUCCEEDED", Output: "legacy step1 output text"},
	})
	require.NoError(t, err)

	legacyYAML := "name: legacy-test\nnodes:\n  - id: step1\n    type: command\n"
	legacyInput := "legacy input data"

	// Insert legacy row
	err = testDB.Exec(`
		INSERT INTO workflow_runs (run_id, session_id, status, dag_spec, input, node_states)
		VALUES (?, ?, ?, ?, ?, ?)
	`, "legacy-run-1", "chat-legacy-1", "WAITING_HUMAN", legacyYAML, legacyInput, statesJSON).Error
	require.NoError(t, err)

	// Set HOME to temp dir so defaultSessionDir writes into isolated directory
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Run AutoMigrate which should migrate schema (adding dag_spec_path, input_path) and backfill data
	err = AutoMigrate(testDB)
	require.NoError(t, err)

	// Inspect the raw DB row
	var rawRow struct {
		RunID       string `gorm:"column:run_id"`
		DAGSpec     string `gorm:"column:dag_spec"`
		Input       string `gorm:"column:input"`
		DAGSpecPath string `gorm:"column:dag_spec_path"`
		InputPath   string `gorm:"column:input_path"`
		NodeStates  string `gorm:"column:node_states"`
	}
	err = testDB.Table("workflow_runs").Where("run_id = ?", "legacy-run-1").Scan(&rawRow).Error
	require.NoError(t, err)

	assert.Empty(t, rawRow.DAGSpec, "legacy dag_spec in raw DB column should be cleared")
	assert.Empty(t, rawRow.Input, "legacy input in raw DB column should be cleared")
	assert.NotEmpty(t, rawRow.DAGSpecPath, "dag_spec_path should be populated")
	assert.NotEmpty(t, rawRow.InputPath, "input_path should be populated")
	assert.FileExists(t, rawRow.DAGSpecPath, "dag_spec file should exist on disk")
	assert.FileExists(t, rawRow.InputPath, "input file should exist on disk")

	content, err := os.ReadFile(rawRow.DAGSpecPath)
	require.NoError(t, err)
	assert.Equal(t, legacyYAML, string(content))

	inputContent, err := os.ReadFile(rawRow.InputPath)
	require.NoError(t, err)
	assert.Equal(t, legacyInput, string(inputContent))

	decodedStates, err := DecodeNodeStates(rawRow.NodeStates)
	require.NoError(t, err)
	assert.Empty(t, decodedStates["step1"].Output, "DB JSON output should be pruned")
	assert.NotEmpty(t, decodedStates["step1"].OutputPath, "DB JSON output_path should be populated")
	assert.FileExists(t, decodedStates["step1"].OutputPath, "Node output file should exist on disk")

	// Verify GetRun hydration
	repo := NewWorkflowRunRepository(testDB)
	repo.SetSessionDirFunc(func(sessionID string) string {
		return filepath.Join(tempHome, "session", sessionID)
	})
	hydrated, err := repo.GetRun("legacy-run-1")
	require.NoError(t, err)
	require.NotNil(t, hydrated)
	assert.Equal(t, legacyYAML, hydrated.DAGSpec)
	assert.Equal(t, legacyInput, hydrated.Input)
	hydratedStates, err := DecodeNodeStates(hydrated.NodeStates)
	require.NoError(t, err)
	assert.Equal(t, "legacy step1 output text", hydratedStates["step1"].Output)
}
