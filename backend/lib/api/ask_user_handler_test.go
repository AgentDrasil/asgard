package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

func TestAskUserHandler_ReplyBroadcastsFullMessage(t *testing.T) {
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

	chatID := "chat-ask-full"
	err = repo.SaveSession(&dbmodels.Session{
		ChatID:       chatID,
		CurrentAgent: "alpha-agent",
	})
	require.NoError(t, err)

	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	// 1. Send ask-user question
	askReqBody, err := json.Marshal(AskUserRequest{
		ChatID:    chatID,
		AgentName: "alpha-agent",
		Question:  "Do you approve this deployment?",
		MessageID: "ask-msg-1",
	})
	require.NoError(t, err)

	// Trigger ask-user in background (since it blocks waiting for reply)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/ask-user", bytes.NewReader(askReqBody))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
	}()

	// Wait for the ask_user message event to be published
	select {
	case ev := <-subCh:
		assert.Equal(t, "message", ev.Type)
		require.NotNil(t, ev.Message)
		assert.Equal(t, "ask-msg-1", ev.Message.ID)
		assert.Equal(t, "ask_user", ev.Message.Role)
		assert.Equal(t, "Do you approve this deployment?", ev.Message.Content)
		assert.False(t, ev.Message.Replied)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for ask-user question event")
	}

	// 2. Submit reply to /api/ask-user/reply
	replyReqBody, err := json.Marshal(AskUserReplyRequest{
		ChatID:    chatID,
		MessageID: "ask-msg-1",
		ReplyText: "Yes, approved!",
	})
	require.NoError(t, err)

	replyReq := httptest.NewRequest(http.MethodPost, "/api/ask-user/reply", bytes.NewReader(replyReqBody))
	replyReq.Header.Set("Content-Type", "application/json")
	replyRR := httptest.NewRecorder()
	server.ServeHTTP(replyRR, replyReq)

	assert.Equal(t, http.StatusOK, replyRR.Code)

	// 3. Verify that the broadcasted message event contains the full message
	select {
	case ev := <-subCh:
		assert.Equal(t, "message", ev.Type)
		require.NotNil(t, ev.Message)
		assert.Equal(t, "ask-msg-1", ev.Message.ID)
		assert.Equal(t, "ask_user", ev.Message.Role)
		assert.Equal(t, "Do you approve this deployment?", ev.Message.Content)
		assert.Equal(t, "alpha-agent", ev.Message.AgentName)
		assert.True(t, ev.Message.Replied)
		assert.Equal(t, "Yes, approved!", ev.Message.ReplyText)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for replied ask-user message event")
	}
}

func TestAskUserHandler_MultipleWaiters_NoArbitraryFallback(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

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

	chatID := "chat-multi-waiters"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{
		ChatID: chatID,
	}))

	// Register 2 ask-user waiters for the same chatID
	ch1 := make(chan string, 1)
	ch2 := make(chan string, 1)

	askWaitersMu.Lock()
	askWaiters["msg-1"] = &askUserWaiter{replyCh: ch1, chatID: chatID}
	askWaiters["msg-2"] = &askUserWaiter{replyCh: ch2, chatID: chatID}
	askWaitersMu.Unlock()

	defer func() {
		askWaitersMu.Lock()
		delete(askWaiters, "msg-1")
		delete(askWaiters, "msg-2")
		askWaitersMu.Unlock()
	}()

	// 1. Reply with unknown message_id ("msg-unknown") -> Should NOT deliver to any waiter
	replyReqBody, err := json.Marshal(AskUserReplyRequest{
		ChatID:    chatID,
		MessageID: "msg-unknown",
		ReplyText: "Hello unknown",
	})
	require.NoError(t, err)

	replyReq := httptest.NewRequest(http.MethodPost, "/api/ask-user/reply", bytes.NewReader(replyReqBody))
	replyReq.Header.Set("Content-Type", "application/json")
	replyRR := httptest.NewRecorder()
	server.ServeHTTP(replyRR, replyReq)

	assert.Equal(t, http.StatusOK, replyRR.Code)

	select {
	case msg := <-ch1:
		t.Fatalf("unexpected message delivered to waiter 1: %s", msg)
	case msg := <-ch2:
		t.Fatalf("unexpected message delivered to waiter 2: %s", msg)
	case <-time.After(50 * time.Millisecond):
		// Expected: no delivery
	}

	// 2. Reply with empty message_id -> Multiple waiters in chat -> Should NOT deliver to any waiter (no arbitrary fallback)
	emptyReqBody, err := json.Marshal(AskUserReplyRequest{
		ChatID:    chatID,
		MessageID: "",
		ReplyText: "Hello empty",
	})
	require.NoError(t, err)

	emptyReq := httptest.NewRequest(http.MethodPost, "/api/ask-user/reply", bytes.NewReader(emptyReqBody))
	emptyReq.Header.Set("Content-Type", "application/json")
	emptyRR := httptest.NewRecorder()
	server.ServeHTTP(emptyRR, emptyReq)

	assert.Equal(t, http.StatusOK, emptyRR.Code)

	select {
	case msg := <-ch1:
		t.Fatalf("unexpected message delivered to waiter 1: %s", msg)
	case msg := <-ch2:
		t.Fatalf("unexpected message delivered to waiter 2: %s", msg)
	case <-time.After(50 * time.Millisecond):
		// Expected: no delivery
	}

	// 3. Reply with exact message_id ("msg-1") -> Should deliver strictly to waiter 1
	exactReqBody, err := json.Marshal(AskUserReplyRequest{
		ChatID:    chatID,
		MessageID: "msg-1",
		ReplyText: "Hello waiter 1",
	})
	require.NoError(t, err)

	exactReq := httptest.NewRequest(http.MethodPost, "/api/ask-user/reply", bytes.NewReader(exactReqBody))
	exactReq.Header.Set("Content-Type", "application/json")
	exactRR := httptest.NewRecorder()
	server.ServeHTTP(exactRR, exactReq)

	assert.Equal(t, http.StatusOK, exactRR.Code)

	select {
	case msg := <-ch1:
		assert.Equal(t, "Hello waiter 1", msg)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for delivery to waiter 1")
	}

	select {
	case msg := <-ch2:
		t.Fatalf("unexpected message delivered to waiter 2: %s", msg)
	default:
		// Expected
	}
}
