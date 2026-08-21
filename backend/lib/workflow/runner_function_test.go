package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestFunctionNodeContext(node *NodeSpec) *NodeContext {
	return &NodeContext{
		SessionID: "sess-fn",
		Node:      node,
		Upstreams: map[string]*NodeResult{},
	}
}

func TestFunctionRunner_Supports(t *testing.T) {
	t.Parallel()

	r := NewFunctionRunner(nil)
	assert.True(t, r.Supports(NodeTypeFunction))
	assert.False(t, r.Supports(NodeTypeAgent))
	assert.False(t, r.Supports(NodeTypeLLM))
	assert.False(t, r.Supports(NodeTypeCommand))
	assert.False(t, r.Supports(NodeTypeHuman))
}

func TestFunctionRunner_Run_Success(t *testing.T) {
	t.Parallel()

	registry := NewFunctionRegistry()
	registry.Register("echo_upper", func(ctx context.Context, nctx *NodeContext) (string, error) {
		return strings.ToUpper(nctx.Interpolate("${nodes.src.output}")), nil
	})

	node := &NodeSpec{ID: "fn_node", Type: NodeTypeFunction, Function: "echo_upper"}
	nctx := newTestFunctionNodeContext(node)
	nctx.Upstreams["src"] = &NodeResult{Status: StatusSucceeded, Output: "payload"}

	res, err := NewFunctionRunner(registry).Run(t.Context(), nctx)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, StatusSucceeded, res.Status)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "PAYLOAD", res.Output)
}

func TestFunctionRunner_Run_UnregisteredFunction(t *testing.T) {
	t.Parallel()

	registry := NewFunctionRegistry()
	node := &NodeSpec{ID: "fn_node", Type: NodeTypeFunction, Function: "ghost_fn"}

	res, err := NewFunctionRunner(registry).Run(t.Context(), newTestFunctionNodeContext(node))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, StatusFailed, res.Status)
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

	node := &NodeSpec{ID: "fn_node", Type: NodeTypeFunction, Function: "boom"}
	res, err := NewFunctionRunner(registry).Run(t.Context(), newTestFunctionNodeContext(node))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, StatusFailed, res.Status)
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

	node := &NodeSpec{ID: "fn_node", Type: NodeTypeFunction, Function: "panic_fn"}
	res, err := NewFunctionRunner(registry).Run(t.Context(), newTestFunctionNodeContext(node))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, StatusFailed, res.Status)
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

	node := &NodeSpec{ID: "fn_node", Type: NodeTypeFunction, Function: "slow_fn", Timeout: "200ms"}

	done := make(chan struct{})
	var res *NodeResult
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
	assert.Equal(t, StatusFailed, res.Status)
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
	defn, err := ParseDefinition([]byte(spec))
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
	assert.Equal(t, StatusSucceeded, fnResult.Status)
	assert.Equal(t, "SOURCE-PAYLOAD", strings.TrimSpace(fnResult.Output))
}
