package agent

import (
	"sync"

	"github.com/AgentDrasil/asgard/simplest/types"
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
}

// NewQueue returns an empty message queue.
func NewQueue() *Queue { return &Queue{} }

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

// Poll drains and returns all pending messages (possibly nil). This is the
// shape expected by Request.GetSteeringMessages / GetFollowUpMessages.
func (q *Queue) Poll() []types.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.msgs
	q.msgs = nil
	return out
}
