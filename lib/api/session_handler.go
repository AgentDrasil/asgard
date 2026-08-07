package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

// ChatSession represents a session response/request payload for the WebUI.
type ChatSession struct {
	ChatID       string             `json:"chatID"`
	Title        string             `json:"title"`
	CurrentAgent string             `json:"currentAgent"`
	RunDir       string             `json:"runDir"`
	GitRoot      string             `json:"gitRoot,omitempty"`
	IsRunning    bool               `json:"isRunning"`
	Messages     dbmodels.Messages  `json:"messages,omitempty"`
	Artifacts    dbmodels.Artifacts `json:"artifacts,omitempty"`
}

type CreateSessionRequest struct {
	CurrentAgent string `json:"currentAgent"`
	RunDir       string `json:"runDir"`
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
		s.handleCreateSession(w, r)
	case http.MethodDelete:
		s.handleDeleteSession(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if r.Body != nil && r.Body != http.NoBody {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	chatID := uuid.Must(uuid.NewV7()).String()

	runDirOpt := optional.None[string]()
	if req.RunDir != "" {
		runDirOpt = optional.Some(req.RunDir)
	}

	if err := s.repo.UpdateAgentSession(chatID, req.CurrentAgent, "", "", runDirOpt); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to create session: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(ChatSession{
		ChatID:       chatID,
		CurrentAgent: req.CurrentAgent,
		RunDir:       req.RunDir,
		GitRoot:      findGitRoot(req.RunDir),
	})
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
		GitRoot:      findGitRoot(sess.RunDir),
		IsRunning:    sess.IsRunning(),
		Messages:     sess.Messages,
		Artifacts:    sess.Artifacts,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(chatSession)
}

func (s *Server) handleGetSessions(w http.ResponseWriter, _ *http.Request) {
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
			GitRoot:      findGitRoot(sess.RunDir),
			IsRunning:    sess.IsRunning(),
			Artifacts:    sess.Artifacts,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(sessions)
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
