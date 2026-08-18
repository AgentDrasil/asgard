package api

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

func TestSessionEventHub_PublishAndSubscribe(t *testing.T) {
	t.Parallel()

	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)

	chatID := "chat-test-1"
	subCh, doneCh, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)
	assert.NotNil(t, doneCh)

	ev1 := SessionEvent{
		Type: "message",
		Message: &dbmodels.ChatMessage{
			ID:      "msg-1",
			Role:    "user",
			Content: "hello",
		},
	}
	hub.Publish(chatID, ev1)

	var firstID int64
	select {
	case received := <-subCh:
		assert.Greater(t, received.EventID, int64(0))
		firstID = received.EventID
		assert.Equal(t, chatID, received.ChatID)
		assert.Equal(t, "message", received.Type)
		require.NotNil(t, received.Message)
		assert.Equal(t, "msg-1", received.Message.ID)
		assert.NotZero(t, received.Timestamp)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	ev2 := SessionEvent{
		Type:    "status",
		Payload: map[string]any{"isRunning": true},
	}
	hub.Publish(chatID, ev2)

	select {
	case received := <-subCh:
		assert.Equal(t, firstID+1, received.EventID)
		assert.Equal(t, "status", received.Type)
		assert.Equal(t, true, received.Payload["isRunning"])
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestSessionEventHub_ReplayMissedEvents(t *testing.T) {
	t.Parallel()

	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)

	chatID := "chat-replay"

	// First subscribe to catch the base event ID
	initialSub, _, initCancel := hub.Subscribe(chatID, 0)

	for i := 1; i <= 5; i++ {
		hub.Publish(chatID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"step": i},
		})
	}

	var eventIDs []int64
	for i := 1; i <= 5; i++ {
		select {
		case ev := <-initialSub:
			eventIDs = append(eventIDs, ev.EventID)
		case <-time.After(1 * time.Second):
			t.Fatalf("timed out reading initial event %d", i)
		}
	}
	initCancel()

	// Subscribing with lastEventID = eventIDs[1] (step 2) should replay events 3, 4, 5
	subCh, _, cancel := hub.Subscribe(chatID, eventIDs[1])
	t.Cleanup(cancel)

	for expectedIdx := 2; expectedIdx < 5; expectedIdx++ {
		select {
		case ev := <-subCh:
			assert.Equal(t, eventIDs[expectedIdx], ev.EventID)
			assert.Equal(t, expectedIdx+1, ev.Payload["step"])
		case <-time.After(1 * time.Second):
			t.Fatalf("timed out waiting for replayed event %d", expectedIdx)
		}
	}
}

func TestSessionEventHub_RingBufferEviction_Resync(t *testing.T) {
	t.Parallel()

	capacity := 5
	hub := NewSessionEventHubWithCapacity(capacity)
	t.Cleanup(hub.Close)

	chatID := "chat-evict"

	// Track published event IDs
	initialSub, _, initCancel := hub.Subscribe(chatID, 0)

	for i := 1; i <= 10; i++ {
		hub.Publish(chatID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"step": i},
		})
	}

	var eventIDs []int64
	for i := 1; i <= 10; i++ {
		select {
		case ev := <-initialSub:
			eventIDs = append(eventIDs, ev.EventID)
		case <-time.After(1 * time.Second):
			t.Fatalf("timed out reading initial event %d", i)
		}
	}
	initCancel()

	// Client requests lastEventID = eventIDs[1] (step 2), which is < oldest buffered ID (eventIDs[5])
	subCh, _, cancel := hub.Subscribe(chatID, eventIDs[1])
	t.Cleanup(cancel)

	select {
	case ev := <-subCh:
		assert.Equal(t, "resync", ev.Type)
		assert.Equal(t, eventIDs[9], ev.EventID)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for resync event")
	}
}

func TestSessionEventHub_SubscriberOverflow_ClosesDone(t *testing.T) {
	t.Parallel()

	capacity := 128
	hub := NewSessionEventHubWithCapacity(capacity)
	t.Cleanup(hub.Close)

	chatID := "chat-overflow"
	_, doneCh, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	// Publish more events than subscriber buffer (128) without consuming
	for i := 0; i < 200; i++ {
		hub.Publish(chatID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"step": i},
		})
	}

	select {
	case <-doneCh:
		// Succeeded: subscriber was disconnected due to overflow
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for subscriber overflow disconnection")
	}
}

func TestSessionEventHub_TopicGC(t *testing.T) {
	t.Parallel()

	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)

	chatID := "chat-gc"
	hub.Publish(chatID, SessionEvent{Type: "status"})

	hub.topicsMu.Lock()
	topic, exists := hub.topics[chatID]
	require.True(t, exists)
	// Simulate idle for longer than 10 minutes
	topic.lastActivity = time.Now().Add(-15 * time.Minute)
	hub.topicsMu.Unlock()

	hub.gc()

	hub.topicsMu.RLock()
	_, exists = hub.topics[chatID]
	hub.topicsMu.RUnlock()
	assert.False(t, exists, "expected idle topic to be garbage collected")
}

func TestSessionEventHub_ConcurrentPublish(t *testing.T) {
	t.Parallel()

	hub := NewSessionEventHubWithCapacity(500)
	t.Cleanup(hub.Close)

	chatID := "chat-concurrent"
	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	var wg sync.WaitGroup
	workers := 10
	eventsPerWorker := 20

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < eventsPerWorker; j++ {
				hub.Publish(chatID, SessionEvent{
					Type:    "status",
					Payload: map[string]any{"worker": workerID, "seq": j},
				})
			}
		}(i)
	}

	wg.Wait()

	totalExpected := workers * eventsPerWorker
	receivedCount := 0
	timeout := time.After(2 * time.Second)

	for receivedCount < totalExpected {
		select {
		case <-subCh:
			receivedCount++
		case <-timeout:
			t.Fatalf("timed out waiting for events, received %d of %d", receivedCount, totalExpected)
		}
	}
	assert.Equal(t, totalExpected, receivedCount)
}

func TestSessionEventHub_ReplayLargeBatch_NoDeadlock(t *testing.T) {
	t.Parallel()

	capacity := 200
	hub := NewSessionEventHubWithCapacity(capacity)
	t.Cleanup(hub.Close)

	chatID := "chat-large-replay"

	initialSub, _, initCancel := hub.Subscribe(chatID, 0)

	// Publish 150 events into a ring buffer of capacity 200
	for i := 1; i <= 150; i++ {
		hub.Publish(chatID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"seq": i},
		})
	}

	var eventIDs []int64
	for i := 1; i <= 150; i++ {
		select {
		case ev := <-initialSub:
			eventIDs = append(eventIDs, ev.EventID)
		case <-time.After(1 * time.Second):
			t.Fatalf("timed out reading initial event %d", i)
		}
	}
	initCancel()

	// Subscribing with lastEventID = eventIDs[0] should replay events 2..150 without deadlocking
	subCh, _, cancel := hub.Subscribe(chatID, eventIDs[0])
	t.Cleanup(cancel)

	received := 0
	timeout := time.After(2 * time.Second)
	for received < 149 {
		select {
		case ev := <-subCh:
			assert.Equal(t, eventIDs[received+1], ev.EventID)
			received++
		case <-timeout:
			t.Fatalf("deadlock/timeout waiting for replayed events, only received %d of 149", received)
		}
	}
	assert.Equal(t, 149, received)
}

func TestSessionEventHub_ReplayAfterGC_Resync(t *testing.T) {
	t.Parallel()

	hub := NewSessionEventHubWithCapacity(50)
	t.Cleanup(hub.Close)

	chatID := "chat-gc-resync"

	// Publish events
	for i := 1; i <= 5; i++ {
		hub.Publish(chatID, SessionEvent{Type: "status"})
	}

	// Simulate GC
	hub.topicsMu.Lock()
	topic, exists := hub.topics[chatID]
	require.True(t, exists)
	topic.lastActivity = time.Now().Add(-15 * time.Minute)
	hub.topicsMu.Unlock()
	hub.gc()

	// Topic is now deleted from hub
	hub.topicsMu.RLock()
	_, exists = hub.topics[chatID]
	hub.topicsMu.RUnlock()
	require.False(t, exists)

	// Client reconnects with lastEventID = 3 after topic was GC'd
	subCh, _, cancel := hub.Subscribe(chatID, 3)
	t.Cleanup(cancel)

	select {
	case ev := <-subCh:
		assert.Equal(t, "resync", ev.Type)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for resync event after GC")
	}
}
