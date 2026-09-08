package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// redriveTestYAML builds a workflow whose command nodes append one line per
// execution to a shared counter file, so re-executions are observable.
func redriveTestYAML(counterPath string) string {
	return fmt.Sprintf(`
name: redrive-test
nodes:
  - id: ok_step
    type: command
    command: "echo ok >> %s"
  - id: cond_skip
    type: command
    when: "nodes.ok_step.status == 'FAILED'"
    depends:
      - node: ok_step
    command: "echo skip >> %s"
  - id: failed_step
    type: command
    depends:
      - node: ok_step
    command: "echo failed >> %s"
  - id: downstream
    type: command
    depends:
      - node: failed_step
    command: "echo down >> %s"
`, counterPath, counterPath, counterPath, counterPath)
}

// seedFailedRunSnapshot mirrors the persisted shape of a run that died at
// failed_step: ok_step succeeded, cond_skip evaluated CONDITION_FALSE, and
// everything at or beyond failed_step never completed.
func seedFailedRunSnapshot(runID, sessionID, dagSpec, runDir string) *RunSnapshot {
	return &RunSnapshot{
		RunID:     runID,
		SessionID: sessionID,
		Status:    PersistStatusFailed,
		DAGSpec:   dagSpec,
		RunDir:    runDir,
		NodeStates: map[string]PersistedNodeState{
			"ok_step":      {Status: string(workflowspec.StatusSucceeded)},
			"cond_skip":    {Status: string(workflowspec.StatusSkipped), SkipReason: string(workflowspec.SkipReasonConditionFalse)},
			"failed_step":  {Status: string(workflowspec.StatusFailed), Error: "boom"},
			"downstream":   {Status: string(workflowspec.StatusSkipped), SkipReason: string(workflowspec.SkipReasonCascadedFailure)},
			"never_active": {Status: string(workflowspec.StatusSkipped), SkipReason: string(workflowspec.SkipReasonNeverActivated)},
		},
	}
}

func TestRedriveFailed_RerunsFailedAndDownstream(t *testing.T) {
	t.Parallel()

	runDir := t.TempDir()
	counter := filepath.Join(runDir, "counter.txt")
	dagSpec := redriveTestYAML(counter)

	store := newMemStore()
	snap := seedFailedRunSnapshot("run-redrive-1", "sess-redrive-1", dagSpec, runDir)
	store.runs[snap.RunID] = snap

	registry := NewNodeRunnerRegistry()
	registry.Register(NewCommandRunner(false))
	engine := NewEngine(registry)
	engine.SetRunStore(store)

	res, err := engine.RedriveFailed(context.Background(), snap.RunID, nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, RunStatusCompleted, res.Status)

	// ok_step must NOT re-execute (seeded), failed_step and downstream must
	// run exactly once each, cond_skip must stay skipped.
	data, err := os.ReadFile(counter)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.ElementsMatch(t, []string{"failed", "down"}, lines)

	// The never-activated leftover state must be replaced by a real terminal
	// result (it does not exist in the new definition, so it is absent).
	settled, err := store.GetRun(snap.RunID)
	require.NoError(t, err)
	assert.Equal(t, PersistStatusCompleted, settled.Status)
	require.Contains(t, settled.NodeStates, "ok_step")
	assert.Equal(t, string(workflowspec.StatusSucceeded), settled.NodeStates["ok_step"].Status)
	require.Contains(t, settled.NodeStates, "cond_skip")
	assert.Equal(t, string(workflowspec.StatusSkipped), settled.NodeStates["cond_skip"].Status)
	require.Contains(t, settled.NodeStates, "failed_step")
	assert.Equal(t, string(workflowspec.StatusSucceeded), settled.NodeStates["failed_step"].Status)
	require.Contains(t, settled.NodeStates, "downstream")
	assert.Equal(t, string(workflowspec.StatusSucceeded), settled.NodeStates["downstream"].Status)
}

func TestRedriveFailed_OnlyFailedRuns(t *testing.T) {
	t.Parallel()

	runDir := t.TempDir()
	store := newMemStore()
	store.runs["run-completed"] = &RunSnapshot{
		RunID:     "run-completed",
		SessionID: "sess-x",
		Status:    PersistStatusCompleted,
		DAGSpec:   redriveTestYAML(filepath.Join(runDir, "counter.txt")),
		RunDir:    runDir,
	}

	engine := NewEngine(NewNodeRunnerRegistry())
	engine.SetRunStore(store)

	_, err := engine.RedriveFailed(context.Background(), "run-completed", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not FAILED")

	_, err = engine.RedriveFailed(context.Background(), "run-missing", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
