package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func setupQueueTestServer(t *testing.T) (*Server, *dbmodels.SessionRepository, *SessionEventHub) {
	t.Helper()
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	hub := NewSessionEventHubWithCapacity(20)
	t.Cleanup(hub.Close)

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "test-agent",
			Name: "Test Agent",
			Type: "agent",
		},
	}
	wfAgent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "test-wf",
			Name: "Test Workflow",
			Type: "workflow",
		},
	}

	server := &Server{
		conf:     &config.Config{},
		repo:     repo,
		eventHub: hub,
		agents:   []*agentspec.Agent{agent, wfAgent},
	}
	server.mux = server.buildMuxLocked()
	return server, repo, hub
}

func TestQueueHandler_GetAndEnqueue(t *testing.T) {
	t.Parallel()
	server, repo, hub := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-000000000001"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	// Hold execution guard so startQueueConsumerIfIdle does not drain the queue before assertions
	server.activeExecutions.Store(chatID, struct{}{})
	t.Cleanup(func() { server.activeExecutions.Delete(chatID) })

	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	// 1. Initial GET /api/sessions/{id}/queue -> empty array []
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+chatID+"/queue", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	var list []dbmodels.QueuedMessage
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	assert.Empty(t, list)

	// 2. Enqueue message via POST /api/sessions/{id}/queue
	body := bytes.NewBufferString(`{"prompt":"Do task 1","model":"gemini-1.5-pro"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+chatID+"/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusCreated, rr.Code)

	var created dbmodels.QueuedMessage
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Equal(t, "Do task 1", created.Prompt)
	assert.Equal(t, "gemini-1.5-pro", created.Model)
	assert.NotEmpty(t, created.ID)

	// Verify SSE broadcast
	select {
	case ev := <-subCh:
		assert.Equal(t, EventTypeQueue, ev.Type)
		qPayload, ok := ev.Payload["queue"].([]dbmodels.QueuedMessage)
		require.True(t, ok)
		require.Len(t, qPayload, 1)
		assert.Equal(t, created.ID, qPayload[0].ID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queue SSE event")
	}

	// 3. GET should return 1 message
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/"+chatID+"/queue", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.Equal(t, created.ID, list[0].ID)

	// 4. Session not found returns 404
	missingID := "018f3a5b-0000-7000-8000-000000000099"
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+missingID+"/queue", bytes.NewBufferString(`{"prompt":"Hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestQueueHandler_CapacityLimit(t *testing.T) {
	t.Parallel()
	server, repo, _ := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-000000000002"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	// Hold the execution guard to simulate active task running, so consumer does not drain the queue immediately
	server.activeExecutions.Store(chatID, struct{}{})
	t.Cleanup(func() { server.activeExecutions.Delete(chatID) })

	// Enqueue 3 messages (capacity)
	for i := 1; i <= 3; i++ {
		body := bytes.NewBufferString(fmt.Sprintf(`{"prompt":"Msg %d"}`, i))
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+chatID+"/queue", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusCreated, rr.Code)
	}

	// 4th message should fail with 400 Bad Request
	body := bytes.NewBufferString(`{"prompt":"Msg 4 (overflow)"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+chatID+"/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "Queue limit reached (maximum 3 messages)")
}

func TestQueueHandler_UpdateAndPatch(t *testing.T) {
	t.Parallel()
	server, repo, hub := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-000000000003"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	msg, err := repo.EnqueueMessage(chatID, "Old prompt", "")
	require.NoError(t, err)

	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	// Update existing message
	patchBody := bytes.NewBufferString(`{"prompt":"Updated prompt text"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/"+chatID+"/queue/"+msg.ID, patchBody)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	var updated dbmodels.QueuedMessage
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &updated))
	assert.Equal(t, "Updated prompt text", updated.Prompt)

	// Verify SSE broadcast
	select {
	case ev := <-subCh:
		assert.Equal(t, EventTypeQueue, ev.Type)
		qPayload, ok := ev.Payload["queue"].([]dbmodels.QueuedMessage)
		require.True(t, ok)
		require.Len(t, qPayload, 1)
		assert.Equal(t, "Updated prompt text", qPayload[0].Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queue SSE event")
	}

	// Update non-existing message -> 404
	req = httptest.NewRequest(http.MethodPatch, "/api/sessions/"+chatID+"/queue/non-existent-msg", bytes.NewBufferString(`{"prompt":"X"}`))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestQueueHandler_DeleteAndClear(t *testing.T) {
	t.Parallel()
	server, repo, hub := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-000000000004"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	// Enqueue 3 messages
	m1, err := repo.EnqueueMessage(chatID, "Msg 1", "")
	require.NoError(t, err)
	m2, err := repo.EnqueueMessage(chatID, "Msg 2", "")
	require.NoError(t, err)
	_, err = repo.EnqueueMessage(chatID, "Msg 3", "")
	require.NoError(t, err)

	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	// 1. Delete m1
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+chatID+"/queue/"+m1.ID, nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	select {
	case ev := <-subCh:
		assert.Equal(t, EventTypeQueue, ev.Type)
		qPayload, ok := ev.Payload["queue"].([]dbmodels.QueuedMessage)
		require.True(t, ok)
		assert.Len(t, qPayload, 2)
		assert.Equal(t, m2.ID, qPayload[0].ID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queue SSE event")
	}

	// Capacity freed: we can now enqueue another message
	body := bytes.NewBufferString(`{"prompt":"Msg 4 newly allowed"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+chatID+"/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusCreated, rr.Code)

	// Delete non-existent -> 404
	req = httptest.NewRequest(http.MethodDelete, "/api/sessions/"+chatID+"/queue/non-existent-msg", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)

	// Clear all remaining messages
	req = httptest.NewRequest(http.MethodDelete, "/api/sessions/"+chatID+"/queue", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	msgs, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

func TestQueueHandler_OfflineAgent(t *testing.T) {
	t.Parallel()
	server, repo, _ := setupQueueTestServer(t)

	// 1. Session agent is a workflow -> enqueue rejected with 400
	wfChatID := "018f3a5b-0000-7000-8000-000000000005"
	require.NoError(t, repo.UpdateAgentSession(wfChatID, "test-wf", "", "", nil))

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+wfChatID+"/queue", bytes.NewBufferString(`{"prompt":"Run wf"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "Agent not found or offline")

	// 2. Session agent does not exist -> 400
	offlineChatID := "018f3a5b-0000-7000-8000-000000000006"
	require.NoError(t, repo.UpdateAgentSession(offlineChatID, "offline-agent", "", "", nil))

	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+offlineChatID+"/queue", bytes.NewBufferString(`{"prompt":"Run offline"}`))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "Agent not found or offline")
}
