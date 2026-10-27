package agent

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func TestQueuePollDrainsAll(t *testing.T) {
	q := NewQueue(QueueAll)
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

func TestQueueDefaultOneAtATime(t *testing.T) {
	t.Parallel()

	modes := []struct {
		name string
		q    *Queue
	}{
		{name: "default", q: NewQueue()},
		{name: "explicit one-at-a-time", q: NewQueue(QueueOneAtATime)},
		{name: "empty mode defaults", q: NewQueue("")},
	}
	for _, tt := range modes {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			q := tt.q
			assert.Nil(t, q.Poll(), "empty queue must poll nil")

			m1 := &types.UserMessage{Content: types.TextOnly("a"), Timestamp: 1}
			m2 := &types.UserMessage{Content: types.TextOnly("b"), Timestamp: 2}
			m3 := &types.UserMessage{Content: types.TextOnly("c"), Timestamp: 3}
			q.Push(m1, m2, m3)
			require.Equal(t, 3, q.Len())

			for i, want := range []types.Message{m1, m2, m3} {
				got := q.Poll()
				require.Len(t, got, 1, "poll %d must return exactly one message", i)
				assert.Same(t, want, got[0], "messages must be FIFO")
				assert.Equal(t, 2-i, q.Len())
			}
			assert.Nil(t, q.Poll())
		})
	}
}

func TestQueueConcurrentPushAndPoll(t *testing.T) {
	t.Parallel()

	for _, mode := range []QueueMode{QueueOneAtATime, QueueAll} {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()

			q := NewQueue(mode)
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
				t.Fatalf("mode %s lost messages: drained=%d pending=%d want %d",
					mode, drained, q.Len(), writers*perWriter)
			}
		})
	}
}

// The loop's own usage: Poll satisfies the queue-func signatures.
func TestQueueFitsRequestFuncs(t *testing.T) {
	t.Parallel()

	for _, mode := range []QueueMode{QueueOneAtATime, QueueAll} {
		steer := NewQueue(mode)
		req := Request{
			GetSteeringMessages: steer.Poll,
			GetFollowUpMessages: steer.Poll,
		}
		if req.GetSteeringMessages == nil || req.GetFollowUpMessages == nil {
			t.Fatalf("mode %s: method values must be assignable", mode)
		}
	}
}

func TestQueuePeekModes(t *testing.T) {
	t.Parallel()

	m1 := &types.UserMessage{Content: types.TextOnly("first"), Timestamp: 100}
	m2 := &types.UserMessage{Content: types.TextOnly("second"), Timestamp: 200}

	tests := []struct {
		name  string
		q     *Queue
		want  []types.Message
		after int
	}{
		{name: "one-at-a-time", q: NewQueue(), want: []types.Message{m1}, after: 2},
		{name: "all", q: NewQueue(QueueAll), want: []types.Message{m1, m2}, after: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			q := tt.q
			assert.Nil(t, q.Peek())
			assert.Nil(t, q.PeekQueuedMessages())

			q.Push(m1, m2)
			peek1 := q.Peek()
			require.Len(t, peek1, len(tt.want))
			for i := range tt.want {
				assert.Same(t, tt.want[i], peek1[i])
			}
			assert.Equal(t, tt.after, q.Len(), "peek must not consume")

			peek2 := q.PeekQueuedMessages()
			require.Len(t, peek2, len(tt.want))
			assert.Same(t, tt.want[0], peek2[0])

			// Mutating the returned slice must not affect internal state.
			peek2[0] = &types.UserMessage{Content: types.TextOnly("mutated")}
			peek3 := q.Peek()
			require.NotEmpty(t, peek3)
			assert.Same(t, m1, peek3[0])
			assert.Equal(t, tt.after, q.Len())
		})
	}
}

func TestQueuePollOneAtATimeThenAll(t *testing.T) {
	t.Parallel()

	// QueueAll still returns FIFO order and clears the queue.
	q := NewQueue(QueueAll)
	m1 := &types.UserMessage{Content: types.TextOnly("a"), Timestamp: 1}
	m2 := &types.UserMessage{Content: types.TextOnly("b"), Timestamp: 2}
	q.Push(m1, m2)
	got := q.Poll()
	require.Len(t, got, 2)
	assert.Same(t, m1, got[0])
	assert.Same(t, m2, got[1])
	assert.Nil(t, q.Poll())
}
