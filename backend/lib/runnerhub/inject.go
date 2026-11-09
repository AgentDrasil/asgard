package runnerhub

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// InjectResult.reason values (see runner.proto).
const (
	injectReasonNotInjectable = "not_injectable"
	injectReasonError         = "error"
)

var (
	// InjectTimeout is how long the runner may take to inject a message: it
	// becomes InjectMessage.deadline_unix_ms. It is a variable for tests.
	InjectTimeout = 5 * time.Second
	// InjectMargin is how much longer than the deadline Inject waits for the
	// InjectResult. The runner decides whether a message arrived in time by
	// comparing deadline_unix_ms with ITS clock, so the backend must not give
	// up (and queue the message as a normal one) at the deadline: a result
	// for a message the runner still managed to inject could be in flight.
	// Waiting a margin past the deadline covers the result's transit and
	// clock skew between backend and runner (assumed below the margin, i.e.
	// NTP-synced hosts). It is a variable for tests.
	InjectMargin = time.Second

	// ErrInjectFailed is returned (with injected=false) when the runner
	// reported a failure with an unknown delivery state.
	ErrInjectFailed = errors.New("inject failed")
)

// injectWaiter is the pending Inject call for one inject_id.
type injectWaiter struct {
	nodeID string
	ch     chan *runnerpb.InjectResult // buffered 1; written at most once
}

// Inject tries to push prompt into the still-running agent CLI of run runID on
// node nodeID (mid-run injection) and reports whether it was injected.
//
// It is a best-effort fast path: whenever injected is false the caller must
// queue prompt as a normal message, regardless of err (err only says why:
// unknown run, ErrRunnerOffline, a runner failure, or ctx being done by then). injected is
// true only when the runner confirmed the injection.
//
// ctx cancellation does NOT shorten the wait: the InjectMessage is already on
// the wire and the runner may still inject until its deadline, so returning
// early would make the caller queue a message that is then injected as well.
// Inject therefore holds the caller's goroutine for at most
// InjectTimeout+InjectMargin; ctx only gates the start and, if done by the
// time Inject gives up, is returned as the error.
//
// Duplicate delivery is avoided by deadline, not by the backend's patience:
// the InjectMessage carries deadline = now + InjectTimeout and the runner
// refuses to inject after it (also for a replay after a reconnect). Inject
// therefore waits until deadline + InjectMargin for the result, so a message
// the runner could still inject is never also reported as not injected. This
// assumes backend and runner clocks differ by less than InjectMargin. The
// remaining unavoidable window (the runner injected, then the connection broke
// and the result only arrives after we gave up) can deliver the message twice;
// the queued InjectMessage is removed so it is never replayed.
func (h *Hub) Inject(ctx context.Context, nodeID, runID, prompt string) (injected bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(prompt) > maxPromptSize {
		return false, fmt.Errorf("%w: inject for run %s", ErrTooLarge, shortID(runID))
	}
	injectID := newBootID()
	deadline := time.Now().Add(InjectTimeout)
	w := &injectWaiter{nodeID: nodeID, ch: make(chan *runnerpb.InjectResult, 1)}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return false, ErrHubClosed
	}
	n := h.nodes[nodeID]
	r := h.runs[runID]
	if n == nil || r == nil || r.nodeID != nodeID || !h.isAllowed(nodeID) {
		h.mu.Unlock()
		return false, fmt.Errorf("%w: %s", ErrUnknownRun, shortID(runID))
	}
	if !n.online {
		h.mu.Unlock()
		return false, fmt.Errorf("%w: %s", ErrRunnerOffline, shortID(nodeID))
	}
	h.injects[injectID] = w
	n.enqueueLocked(&runnerpb.BackendMessage{Payload: &runnerpb.BackendMessage_InjectMessage{InjectMessage: &runnerpb.InjectMessage{
		RunId:          runID,
		InjectId:       injectID,
		Prompt:         prompt,
		DeadlineUnixMs: deadline.UnixMilli(),
	}}})
	h.mu.Unlock()
	// Whatever the outcome, forget the waiter and make sure an unacked
	// InjectMessage is never replayed to the runner later.
	defer h.dropInject(nodeID, injectID)

	// A runner disconnect does not end the wait: the message stays queued and
	// the runner either answers after reconnecting, in time, or drops it as
	// expired. Only the deadline decides. The run ending does not end it
	// either: the runner queues its RunResult before the InjectResult, so an
	// ok may still be in transit and must be honored (it was injected).
	timer := time.NewTimer(time.Until(deadline) + InjectMargin)
	defer timer.Stop()
	return h.awaitInject(ctx, w, timer.C)
}

// awaitInject waits for the result of w or for an exit condition.
//
// Several arms can be ready at once and select picks randomly, so every exit
// other than a result first drains w.ch: a result that already arrived always
// wins over giving up (else the caller would queue a message that was in fact
// injected, delivering it twice).
func (h *Hub) awaitInject(ctx context.Context, w *injectWaiter, timeout <-chan time.Time) (bool, error) {
	late := func(fallback bool, fallbackErr error) (bool, error) {
		select {
		case res := <-w.ch:
			return resultToReturn(res)
		default:
			if fallbackErr == nil {
				fallbackErr = ctx.Err() // informational only: still not injected
			}
			return fallback, fallbackErr
		}
	}
	select {
	case res := <-w.ch:
		return resultToReturn(res)
	case <-timeout:
		return late(false, nil)
	case <-h.ctx.Done():
		// Same theoretical window as above, but a stopping hub is failing
		// every run anyway, so a prompt exit is worth it.
		return late(false, ErrHubClosed)
	}
}

// resultToReturn maps a runner's InjectResult to Inject's return values.
func resultToReturn(res *runnerpb.InjectResult) (bool, error) {
	if res.GetOk() {
		return true, nil
	}
	if res.GetReason() == injectReasonNotInjectable {
		return false, nil
	}
	return false, fmt.Errorf("%w: %s", ErrInjectFailed, shortID(res.GetError()))
}

// dropInject removes the waiter and any still-queued InjectMessage of injectID.
func (h *Hub) dropInject(nodeID, injectID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.injects, injectID)
	n := h.nodes[nodeID]
	if n == nil {
		return
	}
	for i, m := range n.queue {
		if im := m.GetInjectMessage(); im != nil && im.GetInjectId() == injectID {
			n.queue = append(n.queue[:i:i], n.queue[i+1:]...)
			return
		}
	}
}

// applyInjectResult hands a result to its waiter. Results without a waiter
// (a duplicate, or one that arrived after the caller gave up) and results from
// a node other than the one asked are ignored.
func (h *Hub) applyInjectResult(c *connection, res *runnerpb.InjectResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	w := h.injects[res.GetInjectId()]
	if w == nil || w.nodeID != c.nodeID {
		log.Debug().Str("inject_id", shortID(res.GetInjectId())).Msg("ignoring inject result without waiter")
		return
	}
	delete(h.injects, res.GetInjectId())
	w.ch <- res // buffered 1 and the waiter was just removed: never blocks
}
