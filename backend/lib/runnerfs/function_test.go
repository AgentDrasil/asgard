package runnerfs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

type fnClient struct {
	runnerpb.RunnerServiceClient
	mu    sync.Mutex
	calls []*runnerpb.RunFunctionRequest
	next  func(n int, req *runnerpb.RunFunctionRequest) (*runnerpb.RunFunctionResponse, error)
}

func (c *fnClient) RunFunction(_ context.Context, req *runnerpb.RunFunctionRequest, _ ...grpc.CallOption) (*runnerpb.RunFunctionResponse, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req)
	n := len(c.calls)
	c.mu.Unlock()
	return c.next(n, req)
}

func TestRemote_RunFunction(t *testing.T) {
	t.Parallel()

	t.Run("success maps request and response", func(t *testing.T) {
		t.Parallel()

		client := &fnClient{
			next: func(n int, req *runnerpb.RunFunctionRequest) (*runnerpb.RunFunctionResponse, error) {
				return &runnerpb.RunFunctionResponse{
					ExitCode: 0,
					Output:   "success output",
				}, nil
			},
		}

		remote := Remote{ChatID: "chat-42", Client: client}
		spec := FunctionSpec{
			FunctionName:               "test.func",
			NodeID:                     "node-1",
			RunID:                      "run-1",
			WorkflowRunDirs:            []string{"/workspace/Note"},
			WorkflowMountDirsReadOnly:  []string{"/ro"},
			WorkflowMountDirsReadWrite: []string{"/rw"},
			Timeout:                    2500 * time.Millisecond,
			Iteration:                  2,
			RunDir:                     "/workspace/Note",
			AllowCrossSession:          true,
			UserSubject:                "user-bob",
		}

		res, err := remote.RunFunction(t.Context(), spec)
		require.NoError(t, err)
		assert.Equal(t, 0, res.ExitCode)
		assert.Equal(t, "success output", res.Output)
		assert.Empty(t, res.Error)

		require.Len(t, client.calls, 1)
		req := client.calls[0]
		assert.Equal(t, "chat-42", req.GetChatId())
		assert.Equal(t, "test.func", req.GetFunctionName())
		assert.Equal(t, "node-1", req.GetNodeId())
		assert.Equal(t, "run-1", req.GetRunId())
		assert.Equal(t, []string{"/workspace/Note"}, req.GetWorkflowRunDirs())
		assert.Equal(t, []string{"/ro"}, req.GetWorkflowMountDirsReadonly())
		assert.Equal(t, []string{"/rw"}, req.GetWorkflowMountDirsReadwrite())
		assert.Equal(t, int32(3), req.GetTimeoutSeconds()) // ceil(2.5s) = 3s
		assert.Equal(t, int32(2), req.GetIteration())
		assert.True(t, req.GetAllowCrossSession())
		assert.Equal(t, "user-bob", req.GetUserSubject())
		assert.NotEmpty(t, req.GetRequestId())
	})

	t.Run("retries on unavailable and recovers", func(t *testing.T) {
		t.Parallel()

		client := &fnClient{
			next: func(n int, req *runnerpb.RunFunctionRequest) (*runnerpb.RunFunctionResponse, error) {
				if n == 1 {
					return nil, status.Error(codes.Unavailable, "runner disconnected")
				}
				return &runnerpb.RunFunctionResponse{
					ExitCode: 0,
					Output:   "retry succeeded",
				}, nil
			},
		}

		remote := Remote{ChatID: "chat-retry", Client: client}
		spec := FunctionSpec{
			RequestID:    "fixed-req-id",
			FunctionName: "test.retry",
		}

		res, err := remote.RunFunction(t.Context(), spec)
		require.NoError(t, err)
		assert.Equal(t, 0, res.ExitCode)
		assert.Equal(t, "retry succeeded", res.Output)

		require.Len(t, client.calls, 2)
		assert.Equal(t, "fixed-req-id", client.calls[0].GetRequestId())
		assert.Equal(t, "fixed-req-id", client.calls[1].GetRequestId())
	})

	t.Run("non-retryable error fails immediately", func(t *testing.T) {
		t.Parallel()

		client := &fnClient{
			next: func(n int, req *runnerpb.RunFunctionRequest) (*runnerpb.RunFunctionResponse, error) {
				return nil, status.Error(codes.InvalidArgument, "invalid request")
			},
		}

		remote := Remote{ChatID: "chat-err", Client: client}
		spec := FunctionSpec{FunctionName: "test.invalid"}

		res, err := remote.RunFunction(t.Context(), spec)
		require.Error(t, err)
		assert.Equal(t, 1, res.ExitCode)
		assert.Contains(t, err.Error(), "invalid request")
		require.Len(t, client.calls, 1)
	})

	t.Run("canceled context aborts retry", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		client := &fnClient{
			next: func(n int, req *runnerpb.RunFunctionRequest) (*runnerpb.RunFunctionResponse, error) {
				cancel()
				return nil, status.Error(codes.Unavailable, "network down")
			},
		}

		remote := Remote{ChatID: "chat-cancel", Client: client}
		_, err := remote.RunFunction(ctx, FunctionSpec{FunctionName: "test.cancel"})
		require.ErrorIs(t, err, context.Canceled)
	})
}
