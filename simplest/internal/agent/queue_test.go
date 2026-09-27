package agent

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func TestQueuePollDrains(t *testing.T) {
	q := NewQueue()
	if q.Poll() != nil {
		t.Fatal("empty queue must poll nil")
	}
	m1 := &types.UserMessage{Content: types.TextOnly("a"), Timestamp: 1}
	m2 := &types.UserMessage{Content: types.TextOnly("b"), Timestamp: 2}
	q.Push(m1)
	q.Push(m2, textMsg("x"))

	if q.Len() != 3 {
		t.Fatalf("len = %d", q.Len())
	}
	got := q.Poll()
	if len(got) != 3 || got[0] != m1 {
		t.Fatalf("poll = %+v", got)
	}
	if q.Poll() != nil || q.Len() != 0 {
		t.Fatal("queue must be drained after poll")
	}
}

func TestQueueConcurrentPushAndPoll(t *testing.T) {
	q := NewQueue()
	const writers, perWriter = 8, 100
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				q.Push(&types.UserMessage{Content: types.TextOnly("m"), Timestamp: int64(i)})
			}
		}()
	}
	stop := make(chan struct{})
	var drained int
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				drained += len(q.Poll())
				return
			default:
			}
			drained += len(q.Poll())
		}
	}()
	wg.Wait()
	close(stop)
	<-done

	if total := drained + q.Len(); total != writers*perWriter {
		t.Fatalf("lost messages: drained=%d pending=%d want %d", drained, q.Len(), writers*perWriter)
	}
}

// The loop's own usage: Poll satisfies the queue-func signatures.
func TestQueueFitsRequestFuncs(t *testing.T) {
	steer := NewQueue()
	req := Request{
		GetSteeringMessages: steer.Poll,
		GetFollowUpMessages: steer.Poll,
	}
	if req.GetSteeringMessages == nil || req.GetFollowUpMessages == nil {
		t.Fatal("method values must be assignable")
	}
}

func TestQueue_PeekQueuedMessages(t *testing.T) {
	t.Parallel()

	q := NewQueue()

	// Initial empty queue peek
	assert.Nil(t, q.Peek())
	assert.Nil(t, q.PeekQueuedMessages())

	m1 := &types.UserMessage{Content: types.TextOnly("first"), Timestamp: 100}
	m2 := &types.UserMessage{Content: types.TextOnly("second"), Timestamp: 200}
	q.Push(m1, m2)

	// Call Peek and PeekQueuedMessages multiple times
	peek1 := q.Peek()
	require.Len(t, peek1, 2)
	assert.Same(t, m1, peek1[0])
	assert.Same(t, m2, peek1[1])
	assert.Equal(t, 2, q.Len())

	peek2 := q.PeekQueuedMessages()
	require.Len(t, peek2, 2)
	assert.Same(t, m1, peek2[0])
	assert.Same(t, m2, peek2[1])
	assert.Equal(t, 2, q.Len())

	// Mutating the returned slice must not affect internal queue
	peek2[0] = &types.UserMessage{Content: types.TextOnly("mutated")}
	peek3 := q.Peek()
	assert.Same(t, m1, peek3[0])

	// Draining with Poll
	polled := q.Poll()
	require.Len(t, polled, 2)
	assert.Same(t, m1, polled[0])
	assert.Same(t, m2, polled[1])

	// Queue is now empty
	assert.Equal(t, 0, q.Len())
	assert.Nil(t, q.Peek())
	assert.Nil(t, q.PeekQueuedMessages())
}
