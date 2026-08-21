package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
