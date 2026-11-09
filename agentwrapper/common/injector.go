package common

import (
	"io"
	"sync"

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
	i := &Injector{w: w, format: format, replayEcho: replayEcho, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go i.writeLoop()
	return i
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
	line, err := i.format(prompt)
	if err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return types.ErrNotInjectable
	}
	i.pending = append(i.pending, line)
	i.sent++
	i.signal()
	return nil
}

// OnBackgroundTasks records how many background tasks the CLI reports running.
func (i *Injector) OnBackgroundTasks(n int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.background = n
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
	consumed := i.results
	if i.replayEcho {
		consumed = i.echoed
	}
	if consumed >= i.sent && i.background == 0 {
		i.closeLocked()
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
	i.signal()
}
