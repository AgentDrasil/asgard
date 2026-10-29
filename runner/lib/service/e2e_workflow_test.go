package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/paths"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// TestWorkflowFileOpsOverRunnerService runs a workflow whose fanout reads its
// items from the run dir and writes its aggregate into the chat's tmp dir, all
// through the real RunnerService over gRPC (the engine's file operations).
func TestWorkflowFileOpsOverRunnerService(t *testing.T) {
	cl, ws := startService(t, Options{})
	require.NoError(t, os.WriteFile(filepath.Join(ws, "items.txt"), []byte("one\ntwo\n"), 0o644))

	childDefn, err := workflowspec.ParseDefinition([]byte("name: child\nnodes:\n  - id: s\n    type: command\n    command: \"echo processed ${input}\"\n"))
	require.NoError(t, err)
	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	sub := workflow.NewSubWorkflowRunner(func(string) (*workflowspec.WorkflowDefinition, error) { return childDefn, nil })
	registry.Register(sub)
	engine := workflow.NewEngine(registry)
	sub.SetEngine(engine)
	engine.SetFSResolver(func(id string) (runnerfs.FS, error) { return runnerfs.NewRemote(cl, id), nil })

	run := func(itemsFile string) *workflow.WorkflowRunResult {
		t.Helper()
		defn, err := workflowspec.ParseDefinition([]byte("name: parent\nnodes:\n  - id: fan\n    type: workflow\n    workflow: child\n    fanout:\n      items_file: " + itemsFile + "\n      output_file: out/results.jsonl\n"))
		require.NoError(t, err)
		res, err := engine.Execute(t.Context(), defn, workflow.RunContext{SessionID: chat, RunDir: ws})
		require.NoError(t, err)
		return res
	}

	res := run(filepath.Join(ws, "items.txt"))
	require.Equal(t, workflow.RunStatusCompleted, res.Status)
	out, err := os.ReadFile(filepath.Join(paths.SessionTmpDir(chat), "out", "results.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(out), `"item":"one"`)
	assert.Contains(t, string(out), `"item":"two"`)

	// The runner's confinement applies: a file outside every allowed root is
	// unreadable, so the fanout sees no items and rewrites an empty output.
	outside := filepath.Join(t.TempDir(), "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("leak\n"), 0o644))
	res = run(outside)
	require.Equal(t, workflow.RunStatusCompleted, res.Status)
	out, err = os.ReadFile(filepath.Join(paths.SessionTmpDir(chat), "out", "results.jsonl"))
	require.NoError(t, err)
	assert.Empty(t, out)

	// A run dir outside the runner's allowed dirs is refused up front.
	defn, err := workflowspec.ParseDefinition([]byte("name: parent\nnodes:\n  - id: fan\n    type: workflow\n    workflow: child\n    fanout:\n      items_file: items.txt\n"))
	require.NoError(t, err)
	_, err = workflow.NewWorkflowExecutor(engine, defn).Execute(t.Context(), workflow.WorkflowRunParams{SessionID: chat, RunDir: t.TempDir()})
	require.Error(t, err)
}
