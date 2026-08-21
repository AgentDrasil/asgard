package dbmodels

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
)

func TestWorkflowRunRepository(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))

	repo := NewWorkflowRunRepository(testDB)

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
