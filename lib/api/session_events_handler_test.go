package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/db"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

func TestSessionEventsHandler_SSEStream(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)

	server := &Server{
		conf:     &config.Config{},
		repo:     repo,
		eventHub: hub,
	}
	server.mux = server.buildMuxLocked()

	chatID := "chat-sse-1"
	err = repo.SaveSession(&dbmodels.Session{
		ChatID:       chatID,
		CurrentAgent: "test-agent",
		RunDir:       "/workspace",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sessions/%s/events", chatID), nil).WithContext(ctx)
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServeHTTP(rr, req)
	}()

	// Wait briefly for handler to connect and flush
	time.Sleep(50 * time.Millisecond)

	// Publish an event
	server.PublishSessionEvent(chatID, SessionEvent{
		Type: "message",
		Message: &dbmodels.ChatMessage{
			ID:      "msg-1",
			Role:    "user",
			Content: "hello SSE",
		},
	})

	// Wait for event to be written
	time.Sleep(50 * time.Millisecond)
	cancel() // Cancel request context to terminate stream handler
	<-done

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/event-stream")

	body := rr.Body.String()
	assert.Contains(t, body, "event: message")
	assert.Contains(t, body, "hello SSE")
}

func TestSessionEventsHandler_LastEventIDReplay(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)

	server := &Server{
		conf:     &config.Config{},
		repo:     repo,
		eventHub: hub,
	}
	server.mux = server.buildMuxLocked()

	chatID := "chat-replay-sse"
	err = repo.SaveSession(&dbmodels.Session{
		ChatID:       chatID,
		CurrentAgent: "test-agent",
		RunDir:       "/workspace",
	})
	require.NoError(t, err)

	initialSub, _, initCancel := hub.Subscribe(chatID, 0)

	// Publish 3 events prior to client connection
	for i := 1; i <= 3; i++ {
		server.PublishSessionEvent(chatID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"step": i},
		})
	}

	var eventIDs []int64
	for i := 1; i <= 3; i++ {
		select {
		case ev := <-initialSub:
			eventIDs = append(eventIDs, ev.EventID)
		case <-time.After(1 * time.Second):
			t.Fatalf("timed out reading initial event %d", i)
		}
	}
	initCancel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sessions/%s/events", chatID), nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", fmt.Sprintf("%d", eventIDs[0]))
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServeHTTP(rr, req)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := rr.Body.String()
	// Should contain event 2 and event 3, but not event 1
	assert.NotContains(t, body, fmt.Sprintf("id: %d\n", eventIDs[0]))
	assert.Contains(t, body, fmt.Sprintf("id: %d\n", eventIDs[1]))
	assert.Contains(t, body, fmt.Sprintf("id: %d\n", eventIDs[2]))
}

func TestSessionEventsHandler_EvictedResync(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	hub := NewSessionEventHubWithCapacity(3)
	t.Cleanup(hub.Close)

	server := &Server{
		conf:     &config.Config{},
		repo:     repo,
		eventHub: hub,
	}
	server.mux = server.buildMuxLocked()

	chatID := "chat-resync-sse"
	err = repo.SaveSession(&dbmodels.Session{
		ChatID:       chatID,
		CurrentAgent: "test-agent",
		RunDir:       "/workspace",
	})
	require.NoError(t, err)

	// Publish 6 events into a capacity-3 ring buffer
	for i := 1; i <= 6; i++ {
		server.PublishSessionEvent(chatID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"step": i},
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sessions/%s/events", chatID), nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "1") // 1 was evicted (buffered: 4, 5, 6)
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServeHTTP(rr, req)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := rr.Body.String()
	assert.Contains(t, body, "event: resync")
}
