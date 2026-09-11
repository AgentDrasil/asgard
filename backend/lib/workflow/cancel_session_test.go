package workflow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

func TestEngine_CancelSession_AbortsExecutingRun(t *testing.T) {
	defn, err := workflowspec.ParseDefinition([]byte(`
name: cancel-session
nodes:
  - id: a
    type: command
    command: "sleep 100"
`))
	require.NoError(t, err)

	var started atomic.Int32
	runner := &funcRunner{fn: func(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
		started.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	engine := NewEngineWithRunner(runner)

	resCh := make(chan *WorkflowRunResult, 1)
	go func() {
		res, _ := engine.Execute(context.Background(), defn, RunContext{SessionID: "cancel-sess", RunID: "run-cancel"})
		resCh <- res
	}()

	require.Eventually(t, func() bool { return started.Load() == 1 }, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, 1, engine.CancelSession("cancel-sess"))

	select {
	case res := <-resCh:
		require.NotNil(t, res)
		assert.Equal(t, RunStatusCanceled, res.Status)
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after CancelSession")
	}

	// Unknown sessions are a no-op.
	assert.Equal(t, 0, engine.CancelSession("missing-session"))
}

func TestEngine_CancelSession_LeavesSuspendedRunAlone(t *testing.T) {
	defn, err := workflowspec.ParseDefinition([]byte(`
name: suspended
nodes:
  - id: gate
    type: human
    prompt: "approve"
`))
	require.NoError(t, err)

	engine := NewEngineWithRunner(&funcRunner{fn: func(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
		return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded}, nil
	}})
	suspended := make(chan struct{})
	engine.SetHumanSuspender(func(req SuspendRequest) error {
		close(suspended)
		return nil
	})

	done := make(chan struct{})
	go func() {
		_, _ = engine.Execute(context.Background(), defn, RunContext{SessionID: "susp-sess", RunID: "run-susp"})
		close(done)
	}()

	select {
	case <-suspended:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not suspend")
	}

	// The run has an active human waiter, so it must not be cancelled.
	assert.Equal(t, 0, engine.CancelSession("susp-sess"))

	// Clean up the suspended run so Execute returns.
	assert.True(t, engine.DeliverResumeByMessageID(HumanMessageID("run-susp", "gate", 0), "ok"))

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("suspended run did not finish after resume")
	}
}
