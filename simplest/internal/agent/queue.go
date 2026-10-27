package agent

import (
	"sync"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// QueueMode controls how many messages a poll/peek returns.
type QueueMode string

const (
	// QueueOneAtATime delivers exactly one FIFO message per poll (default).
	QueueOneAtATime QueueMode = "one-at-a-time"
	// QueueAll drains every pending message per poll.
	QueueAll QueueMode = "all"
)

// Queue is a mutex-guarded FIFO for steering and follow-up messages.
//
// The library polls GetSteeringMessages/GetFollowUpMessages from the loop
// goroutine; Queue is the safe default implementation behind those funcs.
// A buffered channel plus a non-blocking drain works equally well.
//
// Ownership rule: once a message is pushed it belongs to the loop. Treat it
// as immutable afterwards — mutating it while the loop serializes it to the
// provider or persists it into a session is a data race.
type Queue struct {
	mu   sync.Mutex
	msgs []types.Message
	mode QueueMode
}

// NewQueue returns an empty message queue. With no mode (or an empty mode) it
// defaults to QueueOneAtATime, matching upstream steering/follow-up semantics.
// Pass QueueAll to drain every pending message per poll. Unknown modes fall
// back to the default.
func NewQueue(modes ...QueueMode) *Queue {
	mode := QueueOneAtATime
	if len(modes) > 0 && modes[0] != "" {
		if modes[0] == QueueAll {
			mode = QueueAll
		}
	}
	return &Queue{mode: mode}
}

// Push appends messages. Safe from any goroutine. The messages must not be
// mutated after this call (see ownership rule above).
func (q *Queue) Push(msgs ...types.Message) {
	if len(msgs) == 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.msgs = append(q.msgs, msgs...)
}

// Len reports how many messages are pending.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.msgs)
}

// Poll returns pending messages according to the queue mode: exactly one FIFO
// message for QueueOneAtATime, or all messages for QueueAll. It returns nil
// when empty. This is the shape expected by Request.GetSteeringMessages /
// GetFollowUpMessages.
func (q *Queue) Poll() []types.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.msgs) == 0 {
		return nil
	}
	if q.mode == QueueAll {
		out := q.msgs
		q.msgs = nil
		return out
	}
	out := []types.Message{q.msgs[0]}
	q.msgs = q.msgs[1:]
	if len(q.msgs) == 0 {
		q.msgs = nil
	}
	return out
}

// Peek returns a copy of the next pending messages without removing them: one
// item for QueueOneAtATime, or all items for QueueAll. It returns nil when
// empty. Mutating the returned slice does not affect the queue.
func (q *Queue) Peek() []types.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.msgs) == 0 {
		return nil
	}
	n := 1
	if q.mode == QueueAll {
		n = len(q.msgs)
	}
	out := make([]types.Message, n)
	copy(out, q.msgs[:n])
	return out
}

// PeekQueuedMessages is an alias of Peek for caller readability.
func (q *Queue) PeekQueuedMessages() []types.Message {
	return q.Peek()
}
