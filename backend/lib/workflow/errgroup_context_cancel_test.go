package workflow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// TestExternalContextCancel verifies that canceling the parent context makes
// every worker exit and settles the run as CANCELED.
func TestExternalContextCancel(t *testing.T) {
	defn, err := workflowspec.ParseDefinition([]byte(`
name: cancel-me
nodes:
  - id: a
    type: command
    command: "sleep 100"
  - id: b
    type: command
    command: "sleep 100"
  - id: c
    type: command
    command: "join"
    depends:
      - node: a
      - node: b
`))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	var started atomic.Int32
	blockingRunner := &funcRunner{fn: func(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
		started.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}}

	engine := NewEngineWithRunner(blockingRunner)

	resCh := make(chan *WorkflowRunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := engine.Execute(ctx, defn, RunContext{SessionID: "cancel"})
		resCh <- res
		errCh <- err
	}()

	require.Eventually(t, func() bool { return started.Load() == 2 }, 5*time.Second, 10*time.Millisecond,
		"both independent workers should be running")

	cancel()

	var res *WorkflowRunResult
	select {
	case res = <-resCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after context cancellation")
	}
	require.NoError(t, <-errCh)

	require.NotNil(t, res)
	assert.Equal(t, RunStatusCanceled, res.Status)
	assert.True(t, errors.Is(res.Error, context.Canceled))
}

// TestNodeFailureDoesNotCancelSiblings verifies the errgroup containment
// policy: a failing worker returns nil to the errgroup so sibling nodes keep
// running; only an external ctx cancel broadcasts cancellation.
func TestNodeFailureDoesNotCancelSiblings(t *testing.T) {
	defn, err := workflowspec.ParseDefinition([]byte(`
name: isolated-failure
nodes:
  - id: failer
    type: command
    command: "exit 1"
  - id: slow_sibling
    type: command
    command: "sleep 0.2 && ok"
  - id: downstream
    type: command
    command: "after"
    depends:
      - node: failer
`))
	require.NoError(t, err)

	engine := NewEngineWithRunner(&funcRunner{fn: func(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
		switch nctx.Node.ID {
		case "failer":
			return &workflowspec.NodeResult{Status: workflowspec.StatusFailed, ExitCode: 1, Error: errors.New("exit status 1")}, nil
		case "slow_sibling":
			select {
			case <-time.After(200 * time.Millisecond):
				return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded}, nil
		}
	}})

	res, err := engine.Execute(context.Background(), defn, RunContext{SessionID: "iso"})
	require.NoError(t, err)

	// Global status is FAILED because `failer` was not absorbed by a when
	// branch, but the slow sibling must have been allowed to finish.
	assert.Equal(t, RunStatusFailed, res.Status)
	assert.Equal(t, workflowspec.StatusFailed, res.Nodes["failer"].Status)
	assert.Equal(t, workflowspec.StatusSucceeded, res.Nodes["slow_sibling"].Status,
		"sibling node must not be canceled by a failing worker")
	assert.Equal(t, workflowspec.StatusSkipped, res.Nodes["downstream"].Status)
	assert.Equal(t, workflowspec.SkipReasonCascadedFailure, res.Nodes["downstream"].SkipReason)
}

// TestWorkflowEvents verifies the engine emits the full lifecycle event stream.
func TestWorkflowEvents(t *testing.T) {
	defn, err := workflowspec.ParseDefinition([]byte(`
name: events
nodes:
  - id: a
    type: command
    command: "ok"
  - id: b
    type: command
    command: "skip-me"
    depends:
      - node: a
        when: "nodes.a.exit_code != 0"
`))
	require.NoError(t, err)

	var events []WorkflowEvent
	res, err := NewEngineWithRunner(&funcRunner{fn: func(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
		return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded, ExitCode: 0}, nil
	}}).Execute(context.Background(), defn, RunContext{
		SessionID: "evts",
		EmitEvent: func(ev WorkflowEvent) { events = append(events, ev) },
	})
	require.NoError(t, err)
	assert.Equal(t, RunStatusCompleted, res.Status)

	var types []WorkflowEventType
	for _, ev := range events {
		types = append(types, ev.Type)
		if ev.Workflow != "events" || ev.SessionID != "evts" {
			t.Fatalf("event missing run identity: %+v", ev)
		}
	}
	assert.Contains(t, types, EventWorkflowStarted)
	assert.Contains(t, types, EventNodeStarted)
	assert.Contains(t, types, EventNodeFinished)
	assert.Contains(t, types, EventNodeSkipped)
	assert.Contains(t, types, EventWorkflowFinished)
}
