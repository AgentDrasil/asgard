package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
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

func (r *functionRunner) Supports(t workflowspec.NodeType) bool {
	return t == workflowspec.NodeTypeFunction
}

func (r *functionRunner) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	if fr, ok := ctx.Value(fsCtxKey{}).(runnerfs.FunctionRunner); ok {
		return r.runOnRunner(ctx, nctx, fr)
	}
	return r.runLocal(ctx, nctx)
}

func (r *functionRunner) runLocal(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	node := nctx.Node

	ctx, cancel := withNodeTimeout(ctx, node)
	defer cancel()

	registry := r.registry
	if registry == nil {
		registry = DefaultFunctionRegistry()
	}
	fn, ok := registry.Get(node.Function)
	if !ok {
		return &workflowspec.NodeResult{
			Status:   workflowspec.StatusFailed,
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
		return &workflowspec.NodeResult{
			Status:   workflowspec.StatusFailed,
			ExitCode: 1,
			Error:    err,
		}, nil
	}
	return &workflowspec.NodeResult{
		Status:   workflowspec.StatusSucceeded,
		ExitCode: 0,
		Output:   output,
	}, nil
}

func (r *functionRunner) runOnRunner(ctx context.Context, nctx *NodeContext, fr runnerfs.FunctionRunner) (*workflowspec.NodeResult, error) {
	node := nctx.Node

	callCtx, cancel := ctx, context.CancelFunc(func() {})
	if d := node.TimeoutDuration(); d > 0 {
		callCtx, cancel = context.WithTimeout(ctx, d+remoteTimeoutMargin)
	}
	defer cancel()

	spec := runnerfs.FunctionSpec{
		FunctionName:               node.Function,
		NodeID:                     node.ID,
		RunID:                      nctx.RunID,
		WorkflowRunDirs:            nctx.WorkflowRunDirs,
		WorkflowMountDirsReadOnly:  nctx.WorkflowMountDirs.ReadOnly,
		WorkflowMountDirsReadWrite: nctx.WorkflowMountDirs.ReadWrite,
		Timeout:                    node.TimeoutDuration(),
		Iteration:                  nctx.Iteration,
		RunDir:                     nctx.RunDir,
		AllowCrossSession:          nctx.AllowCrossSession,
		UserSubject:                nctx.UserSubject,
		Input:                      nctx.Input,
	}

	res, err := fr.RunFunction(callCtx, spec)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return &workflowspec.NodeResult{
				Status:   workflowspec.StatusFailed,
				ExitCode: 1,
				Error:    fmt.Errorf("node %s: function %q interrupted: %w", node.ID, node.Function, err),
			}, nil
		}
		return &workflowspec.NodeResult{
			Status:   workflowspec.StatusFailed,
			ExitCode: 1,
			Error:    fmt.Errorf("node %s: function %q failed on runner: %w", node.ID, node.Function, err),
		}, nil
	}

	if res.ExitCode != 0 {
		var err error
		if res.Error != "" {
			err = errors.New(res.Error)
		} else {
			err = fmt.Errorf("node %s: function %q failed with exit code %d", node.ID, node.Function, res.ExitCode)
		}
		return &workflowspec.NodeResult{
			Status:   workflowspec.StatusFailed,
			ExitCode: res.ExitCode,
			Output:   res.Output,
			Error:    err,
		}, nil
	}

	return &workflowspec.NodeResult{
		Status:   workflowspec.StatusSucceeded,
		ExitCode: 0,
		Output:   res.Output,
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
