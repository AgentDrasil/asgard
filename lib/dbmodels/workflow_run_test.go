package dbmodels

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/db"
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

	run1 := &WorkflowRun{
		RunID:              "run-1",
		SessionID:          "chat-1",
		Status:             WorkflowStatusRunning,
		DAGSpec:            "name: wf\n",
		NodeStates:         states,
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

	// Settled runs are no longer waiting.
	loaded.Status = WorkflowStatusCompleted
	require.NoError(t, repo.SaveRun(loaded))
	waiting, err = repo.FindWaitingHumanBySession("chat-1")
	require.NoError(t, err)
	assert.Nil(t, waiting)
}
