package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

// syncRecorder is an httptest.ResponseRecorder safe to poll while the SSE
// handler goroutine is still writing to it.
type syncRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(b)
}

func (r *syncRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *syncRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

// waitBody polls until the streamed body contains every substring. The stream
// is asynchronous, so a fixed sleep is not a barrier and flakes under load.
func (r *syncRecorder) waitBody(t *testing.T, subs ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b := r.body()
		ok := true
		for _, sub := range subs {
			if !strings.Contains(b, sub) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in SSE body:\n%s", subs, b)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitSubscriber polls until the handler has registered a hub subscription.
func waitSubscriber(t *testing.T, hub *SessionEventHub, chatID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		hub.topicsMu.Lock()
		topic := hub.topics[chatID]
		hub.topicsMu.Unlock()
		if topic != nil {
			topic.mu.Lock()
			n := len(topic.subscribers)
			topic.mu.Unlock()
			if n > 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for SSE subscriber")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSessionEventsHandler_SSEStream(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
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
	rr := newSyncRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServeHTTP(rr, req)
	}()

	// Wait for the handler to subscribe before publishing
	waitSubscriber(t, hub, chatID)

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
	rr.waitBody(t, "hello SSE")
	cancel() // Cancel request context to terminate stream handler
	<-done

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/event-stream")

	body := rr.body()
	assert.Contains(t, body, "event: message")
	assert.Contains(t, body, "hello SSE")
}

func TestSessionEventsHandler_LastEventIDReplay(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
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
	rr := newSyncRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServeHTTP(rr, req)
	}()

	rr.waitBody(t, fmt.Sprintf("id: %d\n", eventIDs[2]))
	cancel()
	<-done

	body := rr.body()
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
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
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
	rr := newSyncRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServeHTTP(rr, req)
	}()

	rr.waitBody(t, "event: resync")
	cancel()
	<-done

	body := rr.body()
	assert.Contains(t, body, "event: resync")
}

func TestSessionEventsHandler_RunnerBindingCrossChatFilter(t *testing.T) {
	t.Parallel()

	srv, p := newAccessServer(t)
	srv.eventHub = NewSessionEventHub()
	t.Cleanup(srv.eventHub.Close)
	admin := bearer(t, p, "admin-1", "admin")
	user := bearer(t, p, "user-1")

	// Trigger registration
	_ = doJSON(srv, http.MethodGet, "/api/runners", user, nil)
	_ = doJSON(srv, http.MethodGet, "/api/runners", admin, nil)

	// Create sessions on runner-a and runner-b
	require.NoError(t, srv.repo.SaveSession(&dbmodels.Session{
		ChatID:       "chat-user-a",
		Title:        "Alpha Chat",
		RunnerNodeID: "runner-a",
	}))
	require.NoError(t, srv.repo.SaveSession(&dbmodels.Session{
		ChatID:       "chat-unauthorized-b",
		Title:        "Beta Chat",
		RunnerNodeID: "runner-b",
	}))
	require.NoError(t, srv.repo.SaveSession(&dbmodels.Session{
		ChatID:       "chat-authorized-a2",
		Title:        "Alpha Chat 2",
		RunnerNodeID: "runner-a",
	}))

	// Admin assigns runner-a to user-1
	rec := doJSON(srv, http.MethodPut, "/api/manage/runners/runner-a/users", admin, SetRunnerUsersRequest{Users: []string{"user-1"}})
	require.Equal(t, http.StatusNoContent, rec.Code)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	token := user["Authorization"][len("Bearer "):]
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/chat-user-a/events?access_token="+token, nil).WithContext(ctx)
	rr := newSyncRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeHTTP(rr, req)
	}()

	waitSubscriber(t, srv.eventHub, "chat-user-a")

	// 1. Broadcast an event for an authorized session on runner-a
	srv.BroadcastSessionEvent(SessionEvent{
		ChatID: "chat-authorized-a2",
		Type:   EventTypeStatus,
		Payload: map[string]any{
			"test": "authorized-message",
		},
	})

	// 2. Broadcast an event for an unauthorized session on runner-b
	srv.BroadcastSessionEvent(SessionEvent{
		ChatID: "chat-unauthorized-b",
		Type:   EventTypeStatus,
		Payload: map[string]any{
			"test": "secret-unauthorized-message",
		},
	})

	// Wait for authorized event in stream
	rr.waitBody(t, "authorized-message")

	cancel()
	<-done

	body := rr.body()
	assert.Contains(t, body, "authorized-message")
	assert.NotContains(t, body, "secret-unauthorized-message")
	assert.NotContains(t, body, "chat-unauthorized-b")
}
