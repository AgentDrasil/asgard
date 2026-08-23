package agent

import (
	"sync"
	"testing"

	"github.com/AgentDrasil/asgard/simplest/types"
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
