package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

// ChatSession represents a session response/request payload for the WebUI.
type ChatSession struct {
	ChatID           string                   `json:"chatID"`
	Title            string                   `json:"title"`
	CurrentAgent     string                   `json:"currentAgent"`
	RunDir           string                   `json:"runDir"`
	GitRoot          string                   `json:"gitRoot,omitempty"`
	IsRunning        bool                     `json:"isRunning"`
	IsWaitingForUser bool                     `json:"isWaitingForUser,omitempty"`
	IsArchived       bool                     `json:"isArchived"`
	CreatedAt        *time.Time               `json:"createdAt,omitempty"`
	UpdatedAt        *time.Time               `json:"updatedAt,omitempty"`
	Messages         dbmodels.Messages        `json:"messages,omitempty"`
	Artifacts        dbmodels.Artifacts       `json:"artifacts,omitempty"`
	QueuedMessages   []dbmodels.QueuedMessage `json:"queuedMessages,omitempty"`
}

type CreateSessionRequest struct {
	CurrentAgent string `json:"currentAgent"`
	RunDir       string `json:"runDir"`
}

// handleSessions handles GET, POST, and DELETE requests to /api/sessions.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
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

	chatID := uuid.NewV7().String()

	normalizedRunDir := NormalizeSessionRunDir(req.RunDir, chatID)
	runDirOpt := optional.None[string]()
	if normalizedRunDir != "" {
		runDirOpt = optional.Some(normalizedRunDir)
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
		RunDir:       normalizedRunDir,
		GitRoot:      findGitRoot(normalizedRunDir),
	})
}

func (s *Server) handleGetSessionByID(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
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

	var createdAtPtr, updatedAtPtr *time.Time
	if !sess.CreatedAt.IsZero() {
		createdAtPtr = &sess.CreatedAt
	}
	if !sess.UpdatedAt.IsZero() {
		updatedPtr := sess.UpdatedAt
		updatedAtPtr = &updatedPtr
	}

	queuedMsgs, err := s.repo.GetQueuedMessages(id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to query queued messages: " + err.Error()})
		return
	}

	chatSession := ChatSession{
		ChatID:           sess.ChatID,
		Title:            sess.Title,
		CurrentAgent:     sess.CurrentAgent,
		RunDir:           sess.RunDir,
		GitRoot:          findGitRoot(sess.RunDir),
		IsRunning:        s.isSessionRunning(sess),
		IsWaitingForUser: sess.HasUnrepliedAskUser(),
		IsArchived:       sess.IsArchived,
		CreatedAt:        createdAtPtr,
		UpdatedAt:        updatedAtPtr,
		Messages:         sess.Messages,
		Artifacts:        sess.Artifacts,
		QueuedMessages:   queuedMsgs,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(chatSession)
}

func (s *Server) handleGetSessions(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		q = strings.TrimSpace(r.URL.Query().Get("query"))
	}
	archived := r.URL.Query().Get("archived") == "true"

	limit := dbmodels.DefaultSessionLimit
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil {
			limit = dbmodels.NormalizeSessionLimit(parsed)
		}
	}

	var dbSessions []dbmodels.Session
	var err error
	if q != "" {
		dbSessions, err = s.repo.SearchSessions(q, limit)
	} else {
		dbSessions, err = s.repo.GetSessions(archived, limit)
	}

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to list sessions: " + err.Error()})
		return
	}

	sessions := make([]ChatSession, 0, len(dbSessions))
	for _, sess := range dbSessions {
		sessCopy := sess
		var createdAtPtr, updatedAtPtr *time.Time
		if !sess.CreatedAt.IsZero() {
			createdAtCopy := sess.CreatedAt
			createdAtPtr = &createdAtCopy
		}
		if !sess.UpdatedAt.IsZero() {
			updatedAtCopy := sess.UpdatedAt
			updatedAtPtr = &updatedAtCopy
		}

		sessions = append(sessions, ChatSession{
			ChatID:           sess.ChatID,
			Title:            sess.Title,
			CurrentAgent:     sess.CurrentAgent,
			RunDir:           sess.RunDir,
			GitRoot:          findGitRoot(sess.RunDir),
			IsRunning:        s.isSessionRunning(&sessCopy),
			IsWaitingForUser: sess.HasUnrepliedAskUser(),
			IsArchived:       sess.IsArchived,
			CreatedAt:        createdAtPtr,
			UpdatedAt:        updatedAtPtr,
			Artifacts:        sess.Artifacts,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(sessions)
}

func (s *Server) handleArchiveSession(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
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

	if err := s.repo.ArchiveSession(id); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to archive session: " + err.Error()})
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
