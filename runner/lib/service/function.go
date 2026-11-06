package service

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/pkg/pluginsdk"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

const (
	maxFunctionOutput      = 4 * 1024 * 1024 // 4 MiB cap
	maxConcurrentFunctions = 64
)

var functionSem = make(chan struct{}, maxConcurrentFunctions)

// RunFunction executes a workflow type: function node on this runner.
func (s *Service) RunFunction(ctx context.Context, req *runnerpb.RunFunctionRequest) (*runnerpb.RunFunctionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	chat := req.GetChatId()
	if !agents.IsValidChatID(chat) {
		return nil, status.Error(codes.InvalidArgument, "invalid chat id")
	}

	if len(req.GetWorkflowRunDirs()) > 0 && s.agents != nil {
		allowed := s.allowedRunDirs(false)
		if len(allowed) > 0 {
			for _, d := range req.GetWorkflowRunDirs() {
				if !filepath.IsAbs(d) {
					return nil, status.Errorf(codes.InvalidArgument, "workflow run dir %q is not absolute", d)
				}
				if !run.IsAllowedDir(d, allowed) {
					return nil, status.Errorf(codes.PermissionDenied, "workflow run dir %q is not permitted", d)
				}
			}
		}
	}

	rd, err := runDirString(chat, req.GetRunDir())
	if err != nil {
		return nil, grpcErr(err)
	}

	select {
	case functionSem <- struct{}{}:
		defer func() { <-functionSem }()
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}

	v, err := s.idem.do("RunFunction", req.GetRequestId(), func() (any, error) {
		fs := s.fsFor(chat)
		nctx := &pluginsdk.NodeContext{
			SessionID:       req.GetChatId(),
			RunID:           req.GetRunId(),
			Input:           req.GetInput(),
			RunDir:          rd,
			SessionDir:      fs.SessionDir(),
			TmpDir:          fs.TmpDir(),
			WorkflowRunDirs: req.GetWorkflowRunDirs(),
			WorkflowMountDirs: workflowspec.MountDirsConfig{
				ReadOnly:  req.GetWorkflowMountDirsReadonly(),
				ReadWrite: req.GetWorkflowMountDirsReadwrite(),
			},
			Iteration:         int(req.GetIteration()),
			AllowCrossSession: req.GetAllowCrossSession(),
			UserSubject:       req.GetUserSubject(),
			Node: &workflowspec.NodeSpec{
				ID:       req.GetNodeId(),
				Function: req.GetFunctionName(),
			},
		}

		fn, ok := pluginsdk.GetFunction(req.GetFunctionName())
		if !ok {
			errMsg := fmt.Sprintf("function %q is not registered", req.GetFunctionName())
			if req.GetNodeId() != "" {
				errMsg = fmt.Sprintf("node %s: function %q is not registered", req.GetNodeId(), req.GetFunctionName())
			}
			return &runnerpb.RunFunctionResponse{
				ExitCode: 1,
				Error:    errMsg,
			}, nil
		}

		fnCtx := ctx
		if req.GetTimeoutSeconds() > 0 {
			var cancel context.CancelFunc
			fnCtx, cancel = context.WithTimeout(ctx, time.Duration(req.GetTimeoutSeconds())*time.Second)
			defer cancel()
		}

		output, fnErr := invokeFunction(fnCtx, fn, nctx)
		if len(output) > maxFunctionOutput {
			output = output[:maxFunctionOutput]
		}
		if fnErr != nil {
			if ctxErr := fnCtx.Err(); ctxErr != nil {
				if req.GetNodeId() != "" {
					fnErr = fmt.Errorf("node %s: function %q interrupted: %w", req.GetNodeId(), req.GetFunctionName(), ctxErr)
				} else {
					fnErr = fmt.Errorf("function %q interrupted: %w", req.GetFunctionName(), ctxErr)
				}
			} else {
				if req.GetNodeId() != "" {
					fnErr = fmt.Errorf("node %s: function %q failed: %w", req.GetNodeId(), req.GetFunctionName(), fnErr)
				} else {
					fnErr = fmt.Errorf("function %q failed: %w", req.GetFunctionName(), fnErr)
				}
			}
			return &runnerpb.RunFunctionResponse{
				ExitCode: 1,
				Output:   output,
				Error:    fnErr.Error(),
			}, nil
		}

		return &runnerpb.RunFunctionResponse{
			ExitCode: 0,
			Output:   output,
		}, nil
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	return v.(*runnerpb.RunFunctionResponse), nil
}

func invokeFunction(ctx context.Context, fn pluginsdk.WorkflowFunction, nctx *pluginsdk.NodeContext) (output string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			output = ""
			err = fmt.Errorf("panic recovered: %v", rec)
		}
	}()
	return fn(ctx, nctx)
}
