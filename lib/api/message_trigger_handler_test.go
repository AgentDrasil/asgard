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

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/db"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

func TestMessageTriggerHandler(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)

	agentConfig := agents.AgentConfig{
		ID:          "test-agent",
		Name:        "Test Agent",
		Description: "A test agent",
		Type:        "agent",
	}
	agent := &agents.Agent{
		Config: agentConfig,
	}

	server := &Server{
		conf:     &config.Config{},
		repo:     repo,
		eventHub: hub,
		agents:   []*agents.Agent{agent},
	}
	server.mux = server.buildMuxLocked()

	t.Run("agent not found", func(t *testing.T) {
		t.Parallel()

		body := bytes.NewBufferString(`{"prompt":"hello"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/agents/unknown-agent/message", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("empty prompt rejected", func(t *testing.T) {
		t.Parallel()

		body := bytes.NewBufferString(`{"prompt":"   "}`)
		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("trigger message accepted and published", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-trigger-1"
		subCh, _, cancel := hub.Subscribe(chatID, 0)
		t.Cleanup(cancel)

		payload := TriggerMessageRequest{
			Prompt: "trigger prompt",
			ChatID: chatID,
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusAccepted, rr.Code)

		var resp map[string]any
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, "accepted", resp["status"])
		assert.Equal(t, chatID, resp["chatId"])

		// The executor will run in background and append user message, publishing it to EventHub
		select {
		case ev := <-subCh:
			assert.Equal(t, "message", ev.Type)
			require.NotNil(t, ev.Message)
			assert.Equal(t, "trigger prompt", ev.Message.Content)
		case <-time.After(1 * time.Second):
			t.Fatal("timed out waiting for triggered user message event")
		}
	})

	t.Run("concurrent trigger on same chat rejected with conflict", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-conflict-1"
		server.activeExecutions.Store(chatID, struct{}{})
		t.Cleanup(func() { server.activeExecutions.Delete(chatID) })

		payload := TriggerMessageRequest{
			Prompt: "another prompt",
			ChatID: chatID,
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusConflict, rr.Code)
	})
}
