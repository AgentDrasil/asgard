package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

func newTestFunctionNodeContext(node *workflowspec.NodeSpec) *NodeContext {
	return &NodeContext{
		SessionID: "sess-fn",
		Node:      node,
		Upstreams: map[string]*workflowspec.NodeResult{},
	}
}

func TestFunctionRunner_Supports(t *testing.T) {
	t.Parallel()

	r := NewFunctionRunner(nil)
	assert.True(t, r.Supports(workflowspec.NodeTypeFunction))
	assert.False(t, r.Supports(workflowspec.NodeTypeAgent))
	assert.False(t, r.Supports(workflowspec.NodeTypeLLM))
	assert.False(t, r.Supports(workflowspec.NodeTypeCommand))
	assert.False(t, r.Supports(workflowspec.NodeTypeHuman))
}

func TestFunctionRunner_Run_Success(t *testing.T) {
	t.Parallel()

	registry := NewFunctionRegistry()
	registry.Register("echo_upper", func(ctx context.Context, nctx *NodeContext) (string, error) {
		return strings.ToUpper(nctx.Interpolate("${nodes.src.output}")), nil
	})

	node := &workflowspec.NodeSpec{ID: "fn_node", Type: workflowspec.NodeTypeFunction, Function: "echo_upper"}
	nctx := newTestFunctionNodeContext(node)
	nctx.Upstreams["src"] = &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded, Output: "payload"}

	res, err := NewFunctionRunner(registry).Run(t.Context(), nctx)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, workflowspec.StatusSucceeded, res.Status)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "PAYLOAD", res.Output)
}

func TestFunctionRunner_Run_UnregisteredFunction(t *testing.T) {
	t.Parallel()

	registry := NewFunctionRegistry()
	node := &workflowspec.NodeSpec{ID: "fn_node", Type: workflowspec.NodeTypeFunction, Function: "ghost_fn"}

	res, err := NewFunctionRunner(registry).Run(t.Context(), newTestFunctionNodeContext(node))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, workflowspec.StatusFailed, res.Status)
	assert.Equal(t, 1, res.ExitCode)
	require.Error(t, res.Error)
	assert.Contains(t, res.Error.Error(), `function "ghost_fn" is not registered`)
}

func TestFunctionRunner_Run_FunctionError(t *testing.T) {
	t.Parallel()

	registry := NewFunctionRegistry()
	registry.Register("boom", func(ctx context.Context, nctx *NodeContext) (string, error) {
		return "", errors.New("kaboom")
	})

	node := &workflowspec.NodeSpec{ID: "fn_node", Type: workflowspec.NodeTypeFunction, Function: "boom"}
	res, err := NewFunctionRunner(registry).Run(t.Context(), newTestFunctionNodeContext(node))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, workflowspec.StatusFailed, res.Status)
	assert.Equal(t, 1, res.ExitCode)
	require.Error(t, res.Error)
	assert.Contains(t, res.Error.Error(), "kaboom")
}

func TestFunctionRunner_Run_PanicRecovered(t *testing.T) {
	t.Parallel()

	registry := NewFunctionRegistry()
	registry.Register("panic_fn", func(ctx context.Context, nctx *NodeContext) (string, error) {
		panic("unexpected error")
	})

	node := &workflowspec.NodeSpec{ID: "fn_node", Type: workflowspec.NodeTypeFunction, Function: "panic_fn"}
	res, err := NewFunctionRunner(registry).Run(t.Context(), newTestFunctionNodeContext(node))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, workflowspec.StatusFailed, res.Status)
	assert.Equal(t, 1, res.ExitCode)
	require.Error(t, res.Error)
	assert.Contains(t, res.Error.Error(), "unexpected error")
}

func TestFunctionRunner_Run_ContextTimeout(t *testing.T) {
	t.Parallel()

	registry := NewFunctionRegistry()
	registry.Register("slow_fn", func(ctx context.Context, nctx *NodeContext) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Second):
			return "never", nil
		}
	})

	node := &workflowspec.NodeSpec{ID: "fn_node", Type: workflowspec.NodeTypeFunction, Function: "slow_fn", Timeout: "200ms"}

	done := make(chan struct{})
	var res *workflowspec.NodeResult
	var err error
	go func() {
		defer close(done)
		res, err = NewFunctionRunner(registry).Run(context.Background(), newTestFunctionNodeContext(node))
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("function runner did not return within 2s of node timeout")
	}

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, workflowspec.StatusFailed, res.Status)
	assert.Equal(t, 1, res.ExitCode)
	require.Error(t, res.Error)
	assert.True(t,
		strings.Contains(res.Error.Error(), "deadline") || strings.Contains(res.Error.Error(), "canceled"),
		"expected deadline/canceled in error, got: %v", res.Error)
}

func TestFunctionRunner_EngineEndToEnd(t *testing.T) {
	t.Parallel()

	spec := `
name: fn-e2e-wf
nodes:
  - id: src
    type: command
    command: "echo source-payload"
  - id: fn_node
    type: function
    function: e2e_transform
    depends:
      - node: src
  - id: sink
    type: command
    command: "test -n '${nodes.fn_node.output}'"
    depends:
      - node: fn_node
`
	defn, err := workflowspec.ParseDefinition([]byte(spec))
	require.NoError(t, err)

	registry := NewFunctionRegistry()
	registry.Register("e2e_transform", func(ctx context.Context, nctx *NodeContext) (string, error) {
		return strings.ToUpper(nctx.Interpolate("${nodes.src.output}")), nil
	})

	runners := NewNodeRunnerRegistry()
	runners.Register(NewCommandRunner(false))
	runners.Register(NewFunctionRunner(registry))

	exec := NewWorkflowExecutor(NewEngine(runners), defn)
	res, err := exec.Execute(t.Context(), WorkflowRunParams{
		SessionID: "sess-fn-e2e",
		RunDir:    t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, RunStatusCompleted, res.Status)

	fnResult := res.Nodes["fn_node"]
	require.NotNil(t, fnResult)
	assert.Equal(t, workflowspec.StatusSucceeded, fnResult.Status)
	assert.Equal(t, "SOURCE-PAYLOAD", strings.TrimSpace(fnResult.Output))
}

type fakeFunctionFS struct {
	runnerfs.FS
	got []runnerfs.FunctionSpec
	do  func(ctx context.Context, spec runnerfs.FunctionSpec) (runnerfs.FunctionResult, error)
}

func (f *fakeFunctionFS) RunFunction(ctx context.Context, spec runnerfs.FunctionSpec) (runnerfs.FunctionResult, error) {
	f.got = append(f.got, spec)
	return f.do(ctx, spec)
}

func TestFunctionRunner_RunOnRunner(t *testing.T) {
	t.Parallel()

	node := &workflowspec.NodeSpec{
		ID:       "node_fn",
		Type:     workflowspec.NodeTypeFunction,
		Function: "notebook.scan_ingest_pending",
		Timeout:  "5s",
	}

	t.Run("success maps results", func(t *testing.T) {
		t.Parallel()

		fs := &fakeFunctionFS{
			do: func(ctx context.Context, spec runnerfs.FunctionSpec) (runnerfs.FunctionResult, error) {
				return runnerfs.FunctionResult{ExitCode: 0, Output: "scan complete: 5 items"}, nil
			},
		}

		nctx := &NodeContext{
			SessionID:         "sess-123",
			RunID:             "run-456",
			RunDir:            "/workspace/Note",
			WorkflowRunDirs:   []string{"/workspace/Note"},
			WorkflowMountDirs: workflowspec.MountDirsConfig{ReadOnly: []string{"/vault/ro"}, ReadWrite: []string{"/vault/rw"}},
			Iteration:         2,
			AllowCrossSession: true,
			UserSubject:       "user-chao",
			Node:              node,
		}

		ctx := withFS(t.Context(), fs)
		res, err := NewFunctionRunner(nil).Run(ctx, nctx)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, workflowspec.StatusSucceeded, res.Status)
		assert.Equal(t, 0, res.ExitCode)
		assert.Equal(t, "scan complete: 5 items", res.Output)
		assert.NoError(t, res.Error)

		require.Len(t, fs.got, 1)
		gotSpec := fs.got[0]
		assert.Equal(t, "notebook.scan_ingest_pending", gotSpec.FunctionName)
		assert.Equal(t, "node_fn", gotSpec.NodeID)
		assert.Equal(t, "run-456", gotSpec.RunID)
		assert.Equal(t, "/workspace/Note", gotSpec.RunDir)
		assert.Equal(t, []string{"/workspace/Note"}, gotSpec.WorkflowRunDirs)
		assert.Equal(t, []string{"/vault/ro"}, gotSpec.WorkflowMountDirsReadOnly)
		assert.Equal(t, []string{"/vault/rw"}, gotSpec.WorkflowMountDirsReadWrite)
		assert.Equal(t, 5*time.Second, gotSpec.Timeout)
		assert.Equal(t, 2, gotSpec.Iteration)
		assert.True(t, gotSpec.AllowCrossSession)
		assert.Equal(t, "user-chao", gotSpec.UserSubject)
	})

	t.Run("non-zero exit code marks failure", func(t *testing.T) {
		t.Parallel()

		fs := &fakeFunctionFS{
			do: func(ctx context.Context, spec runnerfs.FunctionSpec) (runnerfs.FunctionResult, error) {
				return runnerfs.FunctionResult{ExitCode: 2, Error: "custom function error", Output: "partial"}, nil
			},
		}

		nctx := &NodeContext{
			SessionID: "sess-err",
			Node:      node,
		}

		ctx := withFS(t.Context(), fs)
		res, err := NewFunctionRunner(nil).Run(ctx, nctx)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, workflowspec.StatusFailed, res.Status)
		assert.Equal(t, 2, res.ExitCode)
		assert.Equal(t, "partial", res.Output)
		require.Error(t, res.Error)
		assert.Equal(t, "custom function error", res.Error.Error())
	})

	t.Run("runner RPC failure", func(t *testing.T) {
		t.Parallel()

		fs := &fakeFunctionFS{
			do: func(ctx context.Context, spec runnerfs.FunctionSpec) (runnerfs.FunctionResult, error) {
				return runnerfs.FunctionResult{ExitCode: 1}, errors.New("connection broken")
			},
		}

		nctx := &NodeContext{
			SessionID: "sess-rpc-err",
			Node:      node,
		}

		ctx := withFS(t.Context(), fs)
		res, err := NewFunctionRunner(nil).Run(ctx, nctx)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, workflowspec.StatusFailed, res.Status)
		assert.Equal(t, 1, res.ExitCode)
		require.Error(t, res.Error)
		assert.Contains(t, res.Error.Error(), "failed on runner: connection broken")
	})

	t.Run("runner context deadline exceeded", func(t *testing.T) {
		t.Parallel()

		fs := &fakeFunctionFS{
			do: func(ctx context.Context, spec runnerfs.FunctionSpec) (runnerfs.FunctionResult, error) {
				return runnerfs.FunctionResult{ExitCode: 1}, context.DeadlineExceeded
			},
		}

		nctx := &NodeContext{
			SessionID: "sess-timeout",
			Node:      node,
		}

		ctx := withFS(t.Context(), fs)
		res, err := NewFunctionRunner(nil).Run(ctx, nctx)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, workflowspec.StatusFailed, res.Status)
		assert.Equal(t, 1, res.ExitCode)
		require.Error(t, res.Error)
		assert.Contains(t, res.Error.Error(), "interrupted: context deadline exceeded")
	})

	t.Run("fallback to runLocal without runnerfs", func(t *testing.T) {
		t.Parallel()

		registry := NewFunctionRegistry()
		registry.Register("local_only_fn", func(ctx context.Context, nctx *NodeContext) (string, error) {
			return "ran locally", nil
		})

		localNode := &workflowspec.NodeSpec{
			ID:       "node_local",
			Type:     workflowspec.NodeTypeFunction,
			Function: "local_only_fn",
		}
		nctx := &NodeContext{
			SessionID: "sess-local",
			Node:      localNode,
		}

		// withFS with runnerfs.Local which does NOT implement FunctionRunner
		localFS := runnerfs.NewLocal("sess-local")
		ctx := withFS(t.Context(), localFS)

		res, err := NewFunctionRunner(registry).Run(ctx, nctx)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, workflowspec.StatusSucceeded, res.Status)
		assert.Equal(t, 0, res.ExitCode)
		assert.Equal(t, "ran locally", res.Output)
	})
}
