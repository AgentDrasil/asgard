package api

import (
	"encoding/json"
	"net/http"

	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

// ChatSession represents a session response/request payload for the WebUI.
type ChatSession struct {
	ChatID       string            `json:"chatID"`
	Title        string            `json:"title"`
	CurrentAgent string            `json:"currentAgent"`
	RunDir       string            `json:"runDir"`
	Messages     dbmodels.Messages `json:"messages,omitempty"`
}

// handleSessions handles GET, POST, and DELETE requests to /api/sessions.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.repo == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "session repository not initialized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGetSessions(w, r)
	case http.MethodPost:
		s.handleSaveSession(w, r)
	case http.MethodDelete:
		s.handleDeleteSession(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGetSessionByID(w http.ResponseWriter, r *http.Request) {
	if s.repo == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "session repository not initialized"})
		return
	}

	id := r.PathValue("id")
	if id == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "session id is required"})
		return
	}
	if !IsValidChatID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid session id format"})
		return
	}

	sess, err := s.repo.GetSession(id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to query session: " + err.Error()})
		return
	}

	if sess == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "session not found"})
		return
	}

	chatSession := ChatSession{
		ChatID:       sess.ChatID,
		Title:        sess.Title,
		CurrentAgent: sess.CurrentAgent,
		RunDir:       sess.RunDir,
		Messages:     sess.Messages,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(chatSession)
}

func (s *Server) handleGetSessions(w http.ResponseWriter, r *http.Request) {
	dbSessions, err := s.repo.GetSessions()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to list sessions: " + err.Error()})
		return
	}

	sessions := make([]ChatSession, 0, len(dbSessions))
	for _, sess := range dbSessions {
		sessions = append(sessions, ChatSession{
			ChatID:       sess.ChatID,
			Title:        sess.Title,
			CurrentAgent: sess.CurrentAgent,
			RunDir:       sess.RunDir,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(sessions)
}

func (s *Server) handleSaveSession(w http.ResponseWriter, r *http.Request) {
	var req ChatSession
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body: " + err.Error()})
		return
	}

	if req.ChatID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "chatID is required"})
		return
	}
	if !IsValidChatID(req.ChatID) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid chatID format"})
		return
	}

	session, err := s.repo.GetSession(req.ChatID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to query session: " + err.Error()})
		return
	}

	if session == nil {
		session = &dbmodels.Session{
			ChatID: req.ChatID,
		}
	}

	// Session title is generated and managed exclusively by the model/backend.
	// Users are not allowed to set or overwrite session.Title via handleSaveSession.
	session.CurrentAgent = req.CurrentAgent
	session.RunDir = req.RunDir
	session.Messages = req.Messages

	if err := s.repo.SaveSession(session); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to save session: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	chatID := r.URL.Query().Get("chat_id")
	if chatID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "chat_id is required"})
		return
	}
	if !IsValidChatID(chatID) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid chat_id format"})
		return
	}

	if err := s.repo.DeleteSession(chatID); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to delete session: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}
