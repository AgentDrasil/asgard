package workflow

import (
	"context"
	"fmt"
)

// functionRunner executes `type: function` nodes by invoking a Go function
// looked up in a FunctionRegistry.
type functionRunner struct {
	// registry is consulted for node functions; nil falls back to the
	// process-wide defaultFunctionRegistry.
	registry *FunctionRegistry
}

// NewFunctionRunner creates the runner for `function` nodes.
func NewFunctionRunner(registry *FunctionRegistry) NodeRunner {
	return &functionRunner{registry: registry}
}

func (r *functionRunner) Supports(t NodeType) bool {
	return t == NodeTypeFunction
}

func (r *functionRunner) Run(ctx context.Context, nctx *NodeContext) (*NodeResult, error) {
	node := nctx.Node

	ctx, cancel := withNodeTimeout(ctx, node)
	defer cancel()

	registry := r.registry
	if registry == nil {
		registry = DefaultFunctionRegistry()
	}
	fn, ok := registry.Get(node.Function)
	if !ok {
		return &NodeResult{
			Status:   StatusFailed,
			ExitCode: 1,
			Error:    fmt.Errorf("node %s: function %q is not registered", node.ID, node.Function),
		}, nil
	}

	output, err := r.invoke(ctx, fn, nctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = fmt.Errorf("node %s: function %q interrupted: %w", node.ID, node.Function, ctxErr)
		} else {
			err = fmt.Errorf("node %s: function %q failed: %w", node.ID, node.Function, err)
		}
		return &NodeResult{
			Status:   StatusFailed,
			ExitCode: 1,
			Error:    err,
		}, nil
	}
	return &NodeResult{
		Status:   StatusSucceeded,
		ExitCode: 0,
		Output:   output,
	}, nil
}

// invoke calls fn with panic isolation so a misbehaving function cannot crash
// the orchestrator process.
func (r *functionRunner) invoke(ctx context.Context, fn WorkflowFunction, nctx *NodeContext) (output string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			output = ""
			err = fmt.Errorf("panic recovered: %v", rec)
		}
	}()
	return fn(ctx, nctx)
}
