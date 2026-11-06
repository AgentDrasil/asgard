package runnerfs

import (
	"context"
	"time"
	"uuid"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// FunctionSpec describes one function node execution on the chat's runner.
type FunctionSpec struct {
	// RequestID makes the call idempotent; empty generates a fresh one.
	RequestID                  string
	FunctionName               string
	NodeID                     string
	RunID                      string
	WorkflowRunDirs            []string
	WorkflowMountDirsReadOnly  []string
	WorkflowMountDirsReadWrite []string
	Timeout                    time.Duration
	Iteration                  int
	RunDir                     string
	AllowCrossSession          bool
	UserSubject                string
	Input                      string
}

// FunctionResult is the outcome of a function execution on the runner.
type FunctionResult struct {
	ExitCode int
	Output   string
	Error    string
}

// FunctionRunner is implemented by an FS whose chat executes function nodes on a runner.
type FunctionRunner interface {
	RunFunction(ctx context.Context, spec FunctionSpec) (FunctionResult, error)
}

var _ FunctionRunner = Remote{}

var (
	functionRetryBackoff = 200 * time.Millisecond
	functionMaxBackoff   = 5 * time.Second
	functionMaxAttempts  = 8
)

// RunFunction implements FunctionRunner on Remote.
func (r Remote) RunFunction(ctx context.Context, spec FunctionSpec) (FunctionResult, error) {
	if spec.RequestID == "" {
		spec.RequestID = uuid.NewV7().String()
	}
	req := &runnerpb.RunFunctionRequest{
		ChatId:                     r.ChatID,
		FunctionName:               spec.FunctionName,
		NodeId:                     spec.NodeID,
		RunId:                      spec.RunID,
		WorkflowRunDirs:            spec.WorkflowRunDirs,
		WorkflowMountDirsReadonly:  spec.WorkflowMountDirsReadOnly,
		WorkflowMountDirsReadwrite: spec.WorkflowMountDirsReadWrite,
		RequestId:                  spec.RequestID,
		Iteration:                  int32(spec.Iteration),
		RunDir:                     RunDirRef(r.ChatID, spec.RunDir),
		AllowCrossSession:          spec.AllowCrossSession,
		UserSubject:                spec.UserSubject,
		Input:                      spec.Input,
	}
	if spec.Timeout > 0 {
		req.TimeoutSeconds = int32((spec.Timeout + time.Second - 1) / time.Second)
	}

	backoff := functionRetryBackoff
	for attempt := 0; ; attempt++ {
		resp, err := r.Client.RunFunction(ctx, req)
		if err == nil {
			return FunctionResult{
				ExitCode: int(resp.GetExitCode()),
				Output:   resp.GetOutput(),
				Error:    resp.GetError(),
			}, nil
		}
		if ctx.Err() != nil {
			return FunctionResult{ExitCode: 1}, ctx.Err()
		}
		if !retryableFunctionErr(err) || attempt+1 >= functionMaxAttempts {
			return FunctionResult{ExitCode: 1}, ToError(err)
		}
		select {
		case <-ctx.Done():
			return FunctionResult{ExitCode: 1}, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, functionMaxBackoff)
	}
}

func retryableFunctionErr(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.Aborted:
		return true
	}
	return false
}
