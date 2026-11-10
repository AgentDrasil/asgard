package conn

import (
	"context"
	"time"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
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
	// InjectMessage pushes prompt into the still-running agent CLI of runID.
	// It is called off the reader goroutine and, unlike StartRun/CancelRun,
	// NOT serialized with the run's other calls (it may block up to deadline
	// and must not delay a CancelRun). The caller has already checked that
	// deadline has not passed. It returns nil when the message was injected,
	// an error matching types.ErrNotInjectable when it was definitely not
	// delivered (unknown or finished run, CLI without injection), and any
	// other error when the delivery state is unknown. injectID is for logging.
	InjectMessage(ctx context.Context, runID, injectID, prompt string, deadline time.Time) error
}

// GenInjector is implemented by executors whose InjectMessage can also report
// the idle generation of the run's agent after it accepted the message; it
// goes to the backend in InjectResult.idle_gen. Executor.InjectMessage is
// used otherwise.
type GenInjector interface {
	InjectMessageGen(ctx context.Context, runID, injectID, prompt string, deadline time.Time) (idleGen uint64, err error)
}

// RunController is implemented by executors that can hold a run's agent CLI
// alive while the run has live subagents (see runnerpb.RunControl). An
// executor without it ignores run controls.
type RunController interface {
	// ControlRun applies action ("hold" or "release") to the run. An error
	// matching types.ErrNotInjectable means the run is gone or its CLI cannot
	// take controls.
	ControlRun(ctx context.Context, runID, action string, holdIdle time.Duration) error
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

// InjectMessage implements Executor: nothing runs, so nothing is injectable.
func (NotImplementedExecutor) InjectMessage(context.Context, string, string, string, time.Time) error {
	return types.ErrNotInjectable
}
