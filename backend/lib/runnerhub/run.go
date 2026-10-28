package runnerhub

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// Result is the final outcome of a run: the RunResult plus the reassembled
// RunOutputChunk stream. A failure reported by the runner is in
// RunResult.Error, not a Go error.
type Result struct {
	RunResult *runnerpb.RunResult
	Output    []byte
}

// Run is the handle of a dispatched run.
type Run struct {
	runID  string
	nodeID string

	mu       sync.Mutex
	events   []*runnerpb.RunEvent
	lastIdx  uint64
	output   []byte
	done     chan struct{}
	finished bool
	result   *Result
	err      error
	changed  chan struct{}
}

func newRun(runID, nodeID string) *Run {
	return &Run{runID: runID, nodeID: nodeID, done: make(chan struct{}), changed: make(chan struct{})}
}

// RunID returns the attempt's run id.
func (r *Run) RunID() string { return r.runID }

// NodeID returns the runner executing the run.
func (r *Run) NodeID() string { return r.nodeID }

// Next returns the next RunEvent in order. It blocks until one is available
// and returns io.EOF once the run is finished and all events were consumed.
func (r *Run) Next(ctx context.Context) (*runnerpb.RunEvent, error) {
	for {
		r.mu.Lock()
		if len(r.events) > 0 {
			ev := r.events[0]
			r.events = r.events[1:]
			r.mu.Unlock()
			return ev, nil
		}
		if r.finished {
			r.mu.Unlock()
			return nil, io.EOF
		}
		ch := r.changed
		r.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Wait blocks until the run is finished and returns its result, or ErrRunLost
// / ErrHubClosed. Events stay readable through Next after Wait returns.
func (r *Run) Wait(ctx context.Context) (*Result, error) {
	select {
	case <-r.done:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// notifyLocked wakes Next waiters. Callers hold r.mu.
func (r *Run) notifyLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// addEvent applies a RunEvent idempotently by event_index.
func (r *Run) addEvent(ev *runnerpb.RunEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished || ev.GetEventIndex() <= r.lastIdx {
		return
	}
	r.lastIdx = ev.GetEventIndex()
	r.events = append(r.events, ev)
	r.notifyLocked()
}

// addChunk appends an output chunk; a replayed chunk (offset below the
// current length) is ignored and a gap is an error.
func (r *Run) addChunk(c *runnerpb.RunOutputChunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	have := uint64(len(r.output))
	switch {
	case c.GetOffset() > have:
		return fmt.Errorf("output gap for run %s: offset %d, have %d", r.runID, c.GetOffset(), have)
	case c.GetOffset()+uint64(len(c.GetData())) <= have:
		return nil
	}
	r.output = append(r.output, c.GetData()[have-c.GetOffset():]...)
	return nil
}

// outputSize returns the reassembled output length.
func (r *Run) outputSize() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return uint64(len(r.output))
}

// finish completes the run exactly once, with a result or an error. With a
// RunResult it attaches the reassembled output.
func (r *Run) finish(res *runnerpb.RunResult, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	if res != nil {
		r.result = &Result{RunResult: res, Output: r.output}
	}
	r.err = err
	close(r.done)
	r.notifyLocked()
}
