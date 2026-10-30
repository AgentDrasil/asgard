package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// TestWorkflowFileOpsOverRunnerService runs a workflow whose fanout reads its
// items from the run dir and writes its aggregate into the chat's tmp dir, all
// through the real RunnerService over gRPC (the engine's file operations).
func TestWorkflowFileOpsOverRunnerService(t *testing.T) {
	// The child's command node asks for no sandbox (bwrap is not available in
	// tests), which the runner only runs with the operator's opt-in.
	cl, ws := startService(t, Options{AllowUnsandboxed: true})
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
	out, err := os.ReadFile(filepath.Join(runnerpaths.SessionTmpDir(chat), "out", "results.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(out), `"item":"one"`)
	assert.Contains(t, string(out), `"item":"two"`)

	// The runner's confinement applies: a file outside every allowed root is
	// unreadable, so the fanout sees no items and rewrites an empty output.
	outside := filepath.Join(t.TempDir(), "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("leak\n"), 0o644))
	res = run(outside)
	require.Equal(t, workflow.RunStatusCompleted, res.Status)
	out, err = os.ReadFile(filepath.Join(runnerpaths.SessionTmpDir(chat), "out", "results.jsonl"))
	require.NoError(t, err)
	assert.Empty(t, out)

	// A run dir outside the runner's allowed dirs is refused up front.
	defn, err := workflowspec.ParseDefinition([]byte("name: parent\nnodes:\n  - id: fan\n    type: workflow\n    workflow: child\n    fanout:\n      items_file: items.txt\n"))
	require.NoError(t, err)
	_, err = workflow.NewWorkflowExecutor(engine, defn).Execute(t.Context(), workflow.WorkflowRunParams{SessionID: chat, RunDir: t.TempDir()})
	require.Error(t, err)
}

// TestWorkflowCommandNodesOverRunnerService runs command nodes of a workflow
// through the real RunnerService over gRPC: output, exit codes, the
// output_file artifact and the runner's refusals. The runner side opts in to
// unsandboxed commands because bwrap is not available in tests.
func TestWorkflowCommandNodesOverRunnerService(t *testing.T) {
	cl, ws := startService(t, Options{AllowUnsandboxed: true})
	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	engine := workflow.NewEngine(registry)
	engine.SetFSResolver(func(id string) (runnerfs.FS, error) { return runnerfs.NewRemote(cl, id), nil })

	defn, err := workflowspec.ParseDefinition([]byte(`name: cmds
nodes:
  - id: produce
    type: command
    command: "mkdir -p ${tmp_dir}/res && echo data > ${tmp_dir}/res/out.txt && echo hello && echo oops >&2 && pwd"
    working_dir: sub/dir
    output_file: res/out.txt
  - id: grepish
    type: command
    command: "exit 1"
    allowed_exit_codes: [0, 1]
  - id: bad
    type: command
    command: "echo why >&2; exit 2"
    on_fail: run
  - id: missing_output
    type: command
    command: "true"
    output_file: nothing.txt
`))
	require.NoError(t, err)
	res, err := engine.Execute(t.Context(), defn, workflow.RunContext{SessionID: chat, RunDir: ws})
	require.NoError(t, err)

	produce := res.Nodes["produce"]
	require.NotNil(t, produce)
	assert.Equal(t, workflowspec.StatusSucceeded, produce.Status, "%v", produce.Error)
	realWS, err := filepath.EvalSymlinks(filepath.Join(ws, "sub", "dir"))
	require.NoError(t, err)
	assert.Equal(t, "hello\n"+realWS+"\n", produce.Output)
	assert.Equal(t, map[string]string{"res/out.txt": "/tmp/res/out.txt"}, produce.Artifacts)
	data, err := os.ReadFile(filepath.Join(runnerpaths.SessionTmpDir(chat), "res", "out.txt"))
	require.NoError(t, err)
	assert.Equal(t, "data\n", string(data))

	grepish := res.Nodes["grepish"]
	assert.Equal(t, workflowspec.StatusSucceeded, grepish.Status)
	assert.Equal(t, 1, grepish.ExitCode)

	bad := res.Nodes["bad"]
	assert.Equal(t, workflowspec.StatusFailed, bad.Status)
	assert.Equal(t, 2, bad.ExitCode)
	require.Error(t, bad.Error)
	assert.Equal(t, "command exited with code 2: why\n", bad.Error.Error())

	assert.Empty(t, res.Nodes["missing_output"].Artifacts)

	// A working dir outside the runner's allowed dirs is refused by the runner.
	defn, err = workflowspec.ParseDefinition([]byte("name: esc\nnodes:\n  - id: esc\n    type: command\n    command: \"touch marker\"\n    working_dir: ../../escaped\n"))
	require.NoError(t, err)
	res, err = engine.Execute(t.Context(), defn, workflow.RunContext{SessionID: chat, RunDir: ws})
	require.NoError(t, err)
	assert.Equal(t, workflowspec.StatusFailed, res.Nodes["esc"].Status)
	assert.NoFileExists(t, filepath.Join(filepath.Dir(filepath.Dir(ws)), "escaped", "marker"))
}

// TestWorkflowCommandNodeSandboxRefusedWhenNotReady shows the fail-closed
// default: a node that wants the sandbox does not run when the runner's
// sandbox is not ready.
func TestWorkflowCommandNodeSandboxRefusedWhenNotReady(t *testing.T) {
	cl, ws := startService(t, Options{AllowUnsandboxed: true})
	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(true))
	engine := workflow.NewEngine(registry)
	engine.SetFSResolver(func(id string) (runnerfs.FS, error) { return runnerfs.NewRemote(cl, id), nil })

	defn, err := workflowspec.ParseDefinition([]byte("name: sb\nnodes:\n  - id: c\n    type: command\n    command: \"touch ran\"\n"))
	require.NoError(t, err)
	res, err := engine.Execute(t.Context(), defn, workflow.RunContext{SessionID: chat, RunDir: ws})
	require.NoError(t, err)
	assert.Equal(t, workflowspec.StatusFailed, res.Nodes["c"].Status)
	require.Error(t, res.Nodes["c"].Error)
	assert.Contains(t, res.Nodes["c"].Error.Error(), "sandbox not ready")
	assert.NoFileExists(t, filepath.Join(ws, "ran"))
}
