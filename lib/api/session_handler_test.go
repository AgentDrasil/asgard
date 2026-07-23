package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/db"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

func TestSessionHandler(t *testing.T) {
	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	conf := &config.Config{
		Host: "http://localhost:8080",
	}

	server := &Server{
		conf: conf,
		repo: repo,
	}
	server.mux = server.buildMuxLocked()

	// 1. GET /api/sessions should start empty
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	var sessions []ChatSession
	err = json.Unmarshal(rr.Body.Bytes(), &sessions)
	require.NoError(t, err)
	assert.Empty(t, sessions)

	// 2. Insert session via repo
	err = repo.SaveSession(&dbmodels.Session{
		ChatID:       "chat-1",
		Title:        "My First Chat",
		CurrentAgent: "agent-alpha",
		RunDir:       "/path/to/run",
		Messages: []dbmodels.ChatMessage{
			{
				ID:      "msg-1",
				Role:    "user",
				Content: "Hello",
			},
			{
				ID:      "msg-2",
				Role:    "assistant",
				Content: "Hi there",
			},
		},
	})
	require.NoError(t, err)

	// 3. GET /api/sessions/{id} should return the created session with messages
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/chat-1", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	var fetchedSession ChatSession
	err = json.Unmarshal(rr.Body.Bytes(), &fetchedSession)
	require.NoError(t, err)
	assert.Equal(t, "chat-1", fetchedSession.ChatID)
	assert.Equal(t, "My First Chat", fetchedSession.Title)
	assert.Equal(t, "agent-alpha", fetchedSession.CurrentAgent)
	assert.Equal(t, "/path/to/run", fetchedSession.RunDir)
	require.Len(t, fetchedSession.Messages, 2)
	assert.Equal(t, "msg-1", fetchedSession.Messages[0].ID)
	assert.Equal(t, "Hello", fetchedSession.Messages[0].Content)

	// 4. Test limit 20 and ordering by update time
	// Delete chat-1 first so we start clean
	req = httptest.NewRequest(http.MethodDelete, "/api/sessions?chat_id=chat-1", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	// Insert 22 sessions directly via repo
	for i := 1; i <= 22; i++ {
		err := repo.SaveSession(&dbmodels.Session{
			ChatID:       fmt.Sprintf("chat-%d", i),
			Title:        fmt.Sprintf("Chat %d", i),
			CurrentAgent: "agent",
			RunDir:       "/",
		})
		require.NoError(t, err)
	}

	// GET /api/sessions should return 20 sessions
	req = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	err = json.Unmarshal(rr.Body.Bytes(), &sessions)
	require.NoError(t, err)
	assert.Len(t, sessions, 20)

	// The first session in the list should be the last one inserted (chat-22)
	assert.Equal(t, "chat-22", sessions[0].ChatID)
	assert.Equal(t, "chat-3", sessions[19].ChatID) // chat-1 and chat-2 are pushed out of the top 20
}
