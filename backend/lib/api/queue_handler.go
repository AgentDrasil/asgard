package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

type EnqueueMessageRequest struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model,omitempty"`
}

type UpdateQueuedMessageRequest struct {
	Prompt string `json:"prompt"`
}

// handleGetQueue handles GET /api/sessions/{id}/queue
func (s *Server) handleGetQueue(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
		return
	}

	id := r.PathValue("id")
	if id == "" || !IsValidChatID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid session id format"})
		return
	}

	msgs, err := s.repo.GetQueuedMessages(id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to query queued messages: " + err.Error()})
		return
	}

	if msgs == nil {
		msgs = []dbmodels.QueuedMessage{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(msgs)
}

// handleEnqueueMessage handles POST /api/sessions/{id}/queue
func (s *Server) handleEnqueueMessage(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
		return
	}

	id := r.PathValue("id")
	if id == "" || !IsValidChatID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid session id format"})
		return
	}

	var req EnqueueMessageRequest
	if r.Body != nil && r.Body != http.NoBody {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body: " + err.Error()})
			return
		}
	}

	if strings.TrimSpace(req.Prompt) == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "prompt is required"})
		return
	}

	session, err := s.repo.GetSession(id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to get session: " + err.Error()})
		return
	}
	if session == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "session not found"})
		return
	}

	// Resolve target agent from session.CurrentAgent (must be single agent, non-workflow)
	var targetAgent *agentspec.Agent
	s.mu.RLock()
	for _, a := range s.agents {
		if (a.Config.ID == session.CurrentAgent || a.Config.Name == session.CurrentAgent) && a.Config.Type != "workflow" {
			targetAgent = a
			break
		}
	}
	s.mu.RUnlock()

	if targetAgent == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Agent not found or offline"})
		return
	}

	// Capacity check (already checked in EnqueueMessage, but check count for consistent 400 error)
	existingMsgs, err := s.repo.GetQueuedMessages(id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to check queue capacity: " + err.Error()})
		return
	}
	if len(existingMsgs) >= dbmodels.MaxQueuedMessagesPerSession {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Queue limit reached (maximum 3 messages)"})
		return
	}

	qmsg, err := s.repo.EnqueueMessage(id, req.Prompt, req.Model)
	if err != nil {
		if errors.Is(err, dbmodels.ErrQueueFull) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Queue limit reached (maximum 3 messages)"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to enqueue message: " + err.Error()})
		return
	}

	// Broadcast queue SSE
	updatedQueue, _ := s.repo.GetQueuedMessages(id)
	if updatedQueue == nil {
		updatedQueue = []dbmodels.QueuedMessage{}
	}
	s.PublishSessionEvent(id, SessionEvent{
		Type:    EventTypeQueue,
		Payload: map[string]any{"queue": updatedQueue},
	})

	// Attempt to start consumer if idle
	s.startQueueConsumerIfIdle(session.ChatID, targetAgent, session.RunDir)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(qmsg)
}

// handleUpdateQueuedMessage handles PATCH /api/sessions/{id}/queue/{messageId}
func (s *Server) handleUpdateQueuedMessage(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
		return
	}

	id := r.PathValue("id")
	if id == "" || !IsValidChatID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid session id format"})
		return
	}

	messageID := r.PathValue("messageId")
	if messageID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "messageId is required"})
		return
	}

	var req UpdateQueuedMessageRequest
	if r.Body != nil && r.Body != http.NoBody {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body: " + err.Error()})
			return
		}
	}

	if strings.TrimSpace(req.Prompt) == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "prompt is required"})
		return
	}

	updated, err := s.repo.UpdateQueuedMessage(id, messageID, req.Prompt)
	if err != nil {
		if errors.Is(err, dbmodels.ErrQueuedMessageNotFound) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "queued message not found"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to update queued message: " + err.Error()})
		return
	}

	// Broadcast queue snapshot
	updatedQueue, _ := s.repo.GetQueuedMessages(id)
	if updatedQueue == nil {
		updatedQueue = []dbmodels.QueuedMessage{}
	}
	s.PublishSessionEvent(id, SessionEvent{
		Type:    EventTypeQueue,
		Payload: map[string]any{"queue": updatedQueue},
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(updated)
}

// handleDeleteQueuedMessage handles DELETE /api/sessions/{id}/queue/{messageId}
func (s *Server) handleDeleteQueuedMessage(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
		return
	}

	id := r.PathValue("id")
	if id == "" || !IsValidChatID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid session id format"})
		return
	}

	messageID := r.PathValue("messageId")
	if messageID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "messageId is required"})
		return
	}

	if err := s.repo.DeleteQueuedMessage(id, messageID); err != nil {
		if errors.Is(err, dbmodels.ErrQueuedMessageNotFound) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "queued message not found"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to delete queued message: " + err.Error()})
		return
	}

	// Broadcast updated queue snapshot
	updatedQueue, _ := s.repo.GetQueuedMessages(id)
	if updatedQueue == nil {
		updatedQueue = []dbmodels.QueuedMessage{}
	}
	s.PublishSessionEvent(id, SessionEvent{
		Type:    EventTypeQueue,
		Payload: map[string]any{"queue": updatedQueue},
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// handleClearQueue handles DELETE /api/sessions/{id}/queue
func (s *Server) handleClearQueue(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
		return
	}

	id := r.PathValue("id")
	if id == "" || !IsValidChatID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid session id format"})
		return
	}

	if _, err := s.repo.ClearQueuedMessages(id); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to clear queued messages: " + err.Error()})
		return
	}

	s.PublishSessionEvent(id, SessionEvent{
		Type:    EventTypeQueue,
		Payload: map[string]any{"queue": []dbmodels.QueuedMessage{}},
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "cleared"})
}
