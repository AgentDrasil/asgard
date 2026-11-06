package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/pluginsdk"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func TestService_RunFunction(t *testing.T) {
	// Register test functions in pluginsdk registry
	fnSuccess := "test.fn_success"
	pluginsdk.RegisterFunction(fnSuccess, func(ctx context.Context, nctx *pluginsdk.NodeContext) (string, error) {
		return fmt.Sprintf("hello from %s", nctx.SessionID), nil
	})

	fnEchoCtx := "test.fn_echo_ctx"
	pluginsdk.RegisterFunction(fnEchoCtx, func(ctx context.Context, nctx *pluginsdk.NodeContext) (string, error) {
		if nctx.SessionDir == "" || nctx.TmpDir == "" {
			return "", errors.New("missing session or tmp dir")
		}
		if len(nctx.WorkflowRunDirs) == 0 || nctx.WorkflowRunDirs[0] != "/workspace/Note" {
			return "", errors.New("unexpected workflow run dirs")
		}
		if len(nctx.WorkflowMountDirs.ReadOnly) == 0 || nctx.WorkflowMountDirs.ReadOnly[0] != "/ro" {
			return "", errors.New("unexpected readonly mount dirs")
		}
		if len(nctx.WorkflowMountDirs.ReadWrite) == 0 || nctx.WorkflowMountDirs.ReadWrite[0] != "/rw" {
			return "", errors.New("unexpected readwrite mount dirs")
		}
		if nctx.Iteration != 3 {
			return "", fmt.Errorf("unexpected iteration: %d", nctx.Iteration)
		}
		if !nctx.AllowCrossSession {
			return "", errors.New("expected allow cross session to be true")
		}
		if nctx.UserSubject != "user-alice" {
			return "", fmt.Errorf("unexpected user subject: %s", nctx.UserSubject)
		}
		return "context verified", nil
	})

	fnFail := "test.fn_fail"
	pluginsdk.RegisterFunction(fnFail, func(ctx context.Context, nctx *pluginsdk.NodeContext) (string, error) {
		return "", errors.New("custom function failure")
	})

	fnPanic := "test.fn_panic"
	pluginsdk.RegisterFunction(fnPanic, func(ctx context.Context, nctx *pluginsdk.NodeContext) (string, error) {
		panic("something went completely wrong")
	})

	fnTimeout := "test.fn_timeout"
	pluginsdk.RegisterFunction(fnTimeout, func(ctx context.Context, nctx *pluginsdk.NodeContext) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "done", nil
		}
	})

	var onceCounter int32
	fnCounter := "test.fn_counter"
	pluginsdk.RegisterFunction(fnCounter, func(ctx context.Context, nctx *pluginsdk.NodeContext) (string, error) {
		cnt := atomic.AddInt32(&onceCounter, 1)
		return fmt.Sprintf("count=%d", cnt), nil
	})

	var allowedDirs []string
	client, ws := startService(t, Options{
		Agents: func() ([]*agentspec.Agent, error) {
			return []*agentspec.Agent{
				{
					Config: agentspec.AgentConfig{
						RunDirs: allowedDirs,
					},
				},
			}, nil
		},
	})
	allowedDirs = []string{ws, "/workspace/Note"}

	t.Run("success", func(t *testing.T) {
		resp, err := client.RunFunction(t.Context(), &runnerpb.RunFunctionRequest{
			ChatId:       chat,
			FunctionName: fnSuccess,
			NodeId:       "node1",
			RunId:        "run1",
		})
		require.NoError(t, err)
		assert.Equal(t, int32(0), resp.GetExitCode())
		assert.Equal(t, fmt.Sprintf("hello from %s", chat), resp.GetOutput())
		assert.Empty(t, resp.GetError())
	})

	t.Run("node context verified", func(t *testing.T) {
		resp, err := client.RunFunction(t.Context(), &runnerpb.RunFunctionRequest{
			ChatId:                     chat,
			FunctionName:               fnEchoCtx,
			NodeId:                     "node_ctx",
			RunId:                      "run_ctx",
			WorkflowRunDirs:            []string{"/workspace/Note"},
			WorkflowMountDirsReadonly:  []string{"/ro"},
			WorkflowMountDirsReadwrite: []string{"/rw"},
			Iteration:                  3,
			AllowCrossSession:          true,
			UserSubject:                "user-alice",
			RunDir: &runnerpb.DirRef{
				Scope:     runnerpb.FileScope_FILE_SCOPE_ABSOLUTE,
				RelOrPath: ws,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, int32(0), resp.GetExitCode(), "function error: %s", resp.GetError())
		assert.Equal(t, "context verified", resp.GetOutput())
		assert.Empty(t, resp.GetError())
	})

	t.Run("unregistered function", func(t *testing.T) {
		resp, err := client.RunFunction(t.Context(), &runnerpb.RunFunctionRequest{
			ChatId:       chat,
			FunctionName: "nonexistent.function",
			NodeId:       "node_missing",
		})
		require.NoError(t, err)
		assert.Equal(t, int32(1), resp.GetExitCode())
		assert.Contains(t, resp.GetError(), `function "nonexistent.function" is not registered`)
	})

	t.Run("function returns error", func(t *testing.T) {
		resp, err := client.RunFunction(t.Context(), &runnerpb.RunFunctionRequest{
			ChatId:       chat,
			FunctionName: fnFail,
			NodeId:       "node_fail",
		})
		require.NoError(t, err)
		assert.Equal(t, int32(1), resp.GetExitCode())
		assert.Contains(t, resp.GetError(), "custom function failure")
	})

	t.Run("function panics", func(t *testing.T) {
		resp, err := client.RunFunction(t.Context(), &runnerpb.RunFunctionRequest{
			ChatId:       chat,
			FunctionName: fnPanic,
			NodeId:       "node_panic",
		})
		require.NoError(t, err)
		assert.Equal(t, int32(1), resp.GetExitCode())
		assert.Contains(t, resp.GetError(), "panic recovered: something went completely wrong")
	})

	t.Run("timeout exceeded", func(t *testing.T) {
		resp, err := client.RunFunction(t.Context(), &runnerpb.RunFunctionRequest{
			ChatId:         chat,
			FunctionName:   fnTimeout,
			NodeId:         "node_timeout",
			TimeoutSeconds: 1,
		})
		require.NoError(t, err)
		assert.Equal(t, int32(1), resp.GetExitCode())
		assert.Contains(t, resp.GetError(), "interrupted")
	})

	t.Run("invalid chat id", func(t *testing.T) {
		_, err := client.RunFunction(t.Context(), &runnerpb.RunFunctionRequest{
			ChatId:       "../../invalid-chat!",
			FunctionName: fnSuccess,
		})
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.InvalidArgument, st.Code())
	})

	t.Run("unauthorized workflow run dir", func(t *testing.T) {
		_, err := client.RunFunction(t.Context(), &runnerpb.RunFunctionRequest{
			ChatId:          chat,
			FunctionName:    fnSuccess,
			WorkflowRunDirs: []string{"/etc"},
		})
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.PermissionDenied, st.Code())
	})

	t.Run("nil request", func(t *testing.T) {
		_, err := client.RunFunction(t.Context(), nil)
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.InvalidArgument, st.Code())
	})

	t.Run("idempotency with request id", func(t *testing.T) {
		reqID := "idem-req-123"
		req := &runnerpb.RunFunctionRequest{
			ChatId:       chat,
			FunctionName: fnCounter,
			NodeId:       "node_cnt",
			RequestId:    reqID,
		}
		resp1, err := client.RunFunction(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, int32(0), resp1.GetExitCode())

		resp2, err := client.RunFunction(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, int32(0), resp2.GetExitCode())
		assert.Equal(t, resp1.GetOutput(), resp2.GetOutput(), "idempotent call should return cached output")
	})
}
