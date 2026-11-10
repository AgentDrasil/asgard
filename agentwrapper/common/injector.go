package common

import (
	"io"
	"sync"
	"time"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// Injector feeds user messages to a CLI running in stream-json input mode and
// decides when stdin may be closed. In that mode the CLI does not exit until
// stdin hits EOF, so stdin is closed as soon as every message sent so far has
// been consumed and answered by a result.
//
// "Consumed" cannot be read off the result count: a CLI may merge a message
// injected mid-turn into the running turn and emit a single result for both.
// With replayEcho the CLI echoes each user message back when it takes it in
// (claude --replay-user-messages, always before the covering result), so a
// result only closes stdin if every sent message has been echoed already.
// Without it (agy) one result per message is assumed.
//
// A result can also come while background subagents still run (claude emits
// an interim result, then another turn once they finish). Stdin stays open
// while any are running so injection keeps working; the final result, which
// arrives after they are gone, closes it.
//
// A hold (SetHold) keeps stdin open even then: the run has live subagents
// whose finish notices are injected as new turns of this same process. While
// held and settled (every message answered) the injector is idle; it tells
// the idle notifier about every change of that, and closes stdin anyway when
// it stays idle for the hold's idle cap, so a release that never comes (the
// backend went away) cannot keep the process forever. A release closes stdin
// right away when idle, else at the result that ends the turn in progress.
type Injector struct {
	mu         sync.Mutex
	w          io.WriteCloser
	format     func(prompt string) ([]byte, error)
	replayEcho bool
	sent       int
	echoed     int
	results    int
	background int
	closed     bool

	// held: stdin stays open past the end of a turn. settled: every message
	// sent so far has been answered (the CLI waits for input). idleOut is the
	// last idle state told to the notifier.
	held     bool
	holdIdle time.Duration
	settled  bool
	idleOut  bool
	idleGen  uint64
	timer    *time.Timer
	// Idle changes are delivered in order by one goroutine, outside mu.
	notifyFn   func(idle bool, gen uint64)
	notifyQ    []idleChange
	notifyWake chan struct{}

	// Writes go through a queue drained by one goroutine, never under mu: a
	// pipe write blocks until the CLI reads, and the stream hooks (which also
	// take mu) run on the goroutine that keeps the CLI's stdout drained. A
	// write under mu would deadlock once a message outgrows the pipe buffer.
	pending [][]byte
	wake    chan struct{}
	done    chan struct{}
}

// NewInjector wraps the CLI's stdin. format renders one prompt as a complete
// NDJSON line (newline included). replayEcho tells whether the CLI echoes
// consumed user messages (see Injector).
func NewInjector(w io.WriteCloser, format func(prompt string) ([]byte, error), replayEcho bool) *Injector {
	i := &Injector{w: w, format: format, replayEcho: replayEcho, wake: make(chan struct{}, 1), done: make(chan struct{}),
		notifyWake: make(chan struct{}, 1)}
	go i.writeLoop()
	go i.notifyLoop()
	return i
}

// DefaultHoldIdle is how long a hold may stay idle (no turn, no message)
// before the injector closes stdin anyway.
const DefaultHoldIdle = 30 * time.Minute

// idleChange is one queued idle notification with the generation of the idle
// state it reports.
type idleChange struct {
	idle bool
	gen  uint64
}

// SetIdleNotify installs fn, called (in order, off the injector's locks) when
// the injector becomes idle (held and waiting for input) or stops being idle.
// gen numbers the changes: it grows with every change, so a receiver that
// gets reports out of band can tell a stale one from a current one. Set it
// before the CLI can produce results.
func (i *Injector) SetIdleNotify(fn func(idle bool, gen uint64)) {
	i.mu.Lock()
	i.notifyFn = fn
	i.mu.Unlock()
}

// SetHold keeps stdin open past the end of a turn (hold) or lifts that. idle
// bounds how long the hold may stay idle; zero means DefaultHoldIdle. Lifting
// a hold on an idle injector closes stdin. It returns types.ErrNotInjectable
// once stdin is closed.
func (i *Injector) SetHold(hold bool, idle time.Duration) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return types.ErrNotInjectable
	}
	i.held = hold
	if idle <= 0 {
		idle = DefaultHoldIdle
	}
	i.holdIdle = idle
	if !hold && i.settled {
		i.closeLocked()
		return nil
	}
	before := i.idleGen
	i.updateIdleLocked()
	if hold && i.idleOut && i.idleGen == before {
		// A hold that finds the injector idle already tells so again (same
		// generation): the earlier report may have been lost on its way.
		i.queueIdleLocked()
	}
	return nil
}

// updateIdleLocked recomputes the idle state: it arms or stops the idle cap
// and queues the change for the notifier.
func (i *Injector) updateIdleLocked() {
	if i.closed {
		return
	}
	idle := i.held && i.settled
	if idle == i.idleOut {
		return
	}
	i.idleOut = idle
	i.idleGen++
	if i.timer != nil {
		i.timer.Stop()
		i.timer = nil
	}
	if idle {
		gen := i.idleGen
		i.timer = time.AfterFunc(i.holdIdle, func() { i.idleExpired(gen) })
	}
	i.queueIdleLocked()
}

// queueIdleLocked queues a notification of the current idle state.
func (i *Injector) queueIdleLocked() {
	i.notifyQ = append(i.notifyQ, idleChange{i.idleOut, i.idleGen})
	select {
	case i.notifyWake <- struct{}{}:
	default:
	}
}

// idleExpired closes stdin of an injector that stayed idle for the cap.
func (i *Injector) idleExpired(gen uint64) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if gen != i.idleGen || !i.idleOut {
		return
	}
	i.held = false
	i.closeLocked()
}

func (i *Injector) notifyLoop() {
	for {
		i.mu.Lock()
		q, fn := i.notifyQ, i.notifyFn
		i.notifyQ = nil
		i.mu.Unlock()
		if fn != nil {
			for _, c := range q {
				fn(c.idle, c.gen)
			}
		}
		select {
		case <-i.notifyWake:
		case <-i.done:
			return
		}
	}
}

// Done is closed once the writer goroutine has flushed everything accepted
// and closed stdin (or given up after a write error).
func (i *Injector) Done() <-chan struct{} { return i.done }

// writeLoop writes queued lines in order. After Close it flushes what was
// accepted before closing stdin, so no accepted message is lost.
func (i *Injector) writeLoop() {
	defer close(i.done)
	for {
		i.mu.Lock()
		if len(i.pending) == 0 {
			if i.closed {
				i.mu.Unlock()
				_ = i.w.Close()
				return
			}
			i.mu.Unlock()
			<-i.wake
			continue
		}
		line := i.pending[0]
		i.pending = i.pending[1:]
		i.mu.Unlock()

		if _, err := i.w.Write(line); err != nil {
			// A dead pipe means the process is gone; nothing more can be
			// injected, and queued lines have nowhere to go.
			i.mu.Lock()
			i.closed = true
			i.pending = nil
			i.mu.Unlock()
			_ = i.w.Close()
			return
		}
	}
}

func (i *Injector) signal() {
	select {
	case i.wake <- struct{}{}:
	default:
	}
}

// Send queues a user message for the writer goroutine. Counting and queueing
// share one critical section so OnResult can never close stdin between
// deciding to send and the message being accepted. It returns
// types.ErrNotInjectable once stdin is closed (or the pipe died); a later
// write failure surfaces as ErrNotInjectable on the next Send.
func (i *Injector) Send(prompt string) error {
	_, err := i.SendGen(prompt)
	return err
}

// SendGen is Send that also returns the idle generation right after the
// message was accepted: the generation of the turn_active report it causes
// when the injector was idle (reports of an earlier generation predate the
// message).
func (i *Injector) SendGen(prompt string) (uint64, error) {
	line, err := i.format(prompt)
	if err != nil {
		return 0, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return 0, types.ErrNotInjectable
	}
	i.pending = append(i.pending, line)
	i.sent++
	i.settled = false
	i.updateIdleLocked()
	i.signal()
	return i.idleGen, nil
}

// OnBackgroundTasks records how many background tasks the CLI reports running.
func (i *Injector) OnBackgroundTasks(n int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.background = n
	if n > 0 {
		i.settled = false
		i.updateIdleLocked()
		return
	}
	// The last background task ended. If every message was answered already
	// (the results came while tasks ran), no later result may follow to
	// settle the injector, which would leave a hold without its idle cap.
	// Only a hold settles here: an injector that is not held still closes at
	// a result.
	if i.held && !i.settled && i.consumedLocked() >= i.sent {
		i.settled = true
		i.updateIdleLocked()
	}
}

// consumedLocked is how many of the sent messages the CLI has taken.
func (i *Injector) consumedLocked() int {
	if i.replayEcho {
		return i.echoed
	}
	return i.results
}

// OnEcho records that the CLI echoed one of our user messages back.
func (i *Injector) OnEcho() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.echoed++
}

// OnResult records one result line and closes stdin once all sent messages
// have been consumed (see Injector). A run may emit extra results (e.g. async
// subagent notifications); closing early on those is harmless only because a
// message sent after the close is rejected rather than lost.
func (i *Injector) OnResult() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.results++
	if i.consumedLocked() >= i.sent && i.background == 0 {
		i.settled = true
		if !i.held {
			i.closeLocked()
		} else {
			i.updateIdleLocked()
		}
	}
}

// Close stops accepting messages and closes stdin once the queue is flushed
// (idempotent); later Send calls return ErrNotInjectable.
func (i *Injector) Close() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.closeLocked()
}

// closeLocked rejects further Sends; the writer goroutine flushes the queue
// and then closes stdin.
func (i *Injector) closeLocked() {
	if i.closed {
		return
	}
	i.closed = true
	i.idleGen++
	if i.timer != nil {
		i.timer.Stop()
		i.timer = nil
	}
	i.signal()
}

// Entry types of the turn state reports a held run sends (ReportFunc
// entryType, content "idle" or "active"); the backend turns them into the
// chat's status instead of a transcript entry. The metadata of a report
// carries the injector's idle generation under MetaIdleGen.
const (
	EntryTypeTurnIdle   = "turn_idle"
	EntryTypeTurnActive = "turn_active"

	// MetaIdleGen is the metadata key of the idle generation of a turn report.
	MetaIdleGen = "idle_gen"
)

// WireHold connects inj to the host: control (opts.OnControl) gets the
// hold/release func, and idle changes are reported through report (nil: not
// reported) as EntryTypeTurnIdle / EntryTypeTurnActive.
func WireHold(inj *Injector, report types.ReportFunc, onControl func(func(bool, time.Duration) error)) {
	if report != nil {
		inj.SetIdleNotify(func(idle bool, gen uint64) {
			meta := map[string]any{MetaIdleGen: gen}
			if idle {
				report(0, "SYSTEM", EntryTypeTurnIdle, "idle", meta)
			} else {
				report(0, "SYSTEM", EntryTypeTurnActive, "active", meta)
			}
		})
	}
	onControl(inj.SetHold)
}
