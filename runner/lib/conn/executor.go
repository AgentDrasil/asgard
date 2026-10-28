package conn

import (
	"context"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// Sink is how an Executor reports a run's progress upstream. Messages are
// queued in order and delivered reliably across reconnects. Calls block while
// the unacked queue is full.
type Sink interface {
	// Event reports a status update (event_index increasing from 1 per run).
	Event(ctx context.Context, ev *runnerpb.RunEvent) error
	// Output reports a chunk of the run's output (<= 1 MiB).
	Output(ctx context.Context, chunk *runnerpb.RunOutputChunk) error
	// Result finalizes the run; call it exactly once per run.
	Result(ctx context.Context, res *runnerpb.RunResult) error
}

// Executor runs the attempts the backend dispatches. Methods are called off
// the connection's reader goroutine, so implementations may block (including
// in Sink calls). Calls for one run_id are serialized in the order the backend
// sent them (a CancelRun is never delivered before its StartRun returns), so a
// blocking StartRun delays that run's CancelRun: start the work asynchronously
// if cancellation must stay responsive. Different runs proceed concurrently.
type Executor interface {
	// StartRun begins sr; progress and the final
	// result go through sink. Backend-side replays of a known run_id never
	// reach the executor.
	StartRun(ctx context.Context, sr *runnerpb.StartRun, sink Sink)
	// CancelRun stops the run if it is still executing.
	CancelRun(runID string)
}

// ErrNotImplemented is the RunResult.error of the placeholder executor.
const ErrNotImplemented = "execution not implemented"

// NotImplementedExecutor answers every StartRun with a failed RunResult.
type NotImplementedExecutor struct{}

// StartRun implements Executor.
func (NotImplementedExecutor) StartRun(ctx context.Context, sr *runnerpb.StartRun, sink Sink) {
	_ = sink.Result(ctx, &runnerpb.RunResult{
		RunId:          sr.GetRunId(),
		Error:          ErrNotImplemented,
		ChatId:         sr.GetChatId(),
		Attempt:        sr.GetAttempt(),
		WorkflowNodeId: sr.GetStatusScope().GetNodeId(),
		RunToken:       sr.GetStatusScope().GetRunToken(),
	})
}

// CancelRun implements Executor.
func (NotImplementedExecutor) CancelRun(string) {}
