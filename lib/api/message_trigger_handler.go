package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/google/uuid"
	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agents"
)

// TriggerMessageRequest represents the payload for POST /api/agents/{id}/message.
type TriggerMessageRequest struct {
	Prompt   string         `json:"prompt"`
	ChatID   string         `json:"chatId,omitempty"`
	RunDir   string         `json:"runDir,omitempty"`
	Model    string         `json:"model,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// handleTriggerMessage handles POST /api/agents/{id}/message, launching agent execution
// asynchronously and draining executor side-effects.
func (s *Server) handleTriggerMessage(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		http.Error(w, `{"error":"agent id is required"}`, http.StatusBadRequest)
		return
	}

	var targetAgent *agents.Agent
	s.mu.RLock()
	for _, a := range s.agents {
		if a.Config.ID == agentID || a.Config.Name == agentID {
			targetAgent = a
			break
		}
	}
	s.mu.RUnlock()

	if targetAgent == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "agent not found"})
		return
	}

	var req TriggerMessageRequest
	if r.Body != nil && r.Body != http.NoBody {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body: `+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
	}

	if strings.TrimSpace(req.Prompt) == "" {
		http.Error(w, `{"error":"prompt is required"}`, http.StatusBadRequest)
		return
	}

	chatID := req.ChatID
	if chatID == "" {
		chatID = uuid.Must(uuid.NewV7()).String()
	} else if !IsValidChatID(chatID) {
		http.Error(w, `{"error":"invalid chatId format"}`, http.StatusBadRequest)
		return
	}

	// Re-entrancy guard: prevent concurrent execution on the same chat ID
	if _, loaded := s.activeExecutions.LoadOrStore(chatID, struct{}{}); loaded {
		http.Error(w, `{"error":"session is already running a task"}`, http.StatusConflict)
		return
	}

	runDirOpt := optional.None[string]()
	if req.RunDir != "" {
		runDirOpt = optional.Some(req.RunDir)
	}

	if s.repo != nil {
		if err := s.repo.UpdateAgentSession(chatID, targetAgent.Config.ID, "", "", runDirOpt); err != nil {
			log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to update agent session on trigger message")
		}
	}

	userMsgID := ""
	if req.Metadata != nil {
		if mid, ok := req.Metadata["message_id"].(string); ok && mid != "" {
			userMsgID = mid
		}
	}
	if userMsgID == "" {
		userMsgID = fmt.Sprintf("user-%s", uuid.Must(uuid.NewV7()).String())
	}

	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(req.Prompt))
	msg.ID = userMsgID
	msg.Metadata = req.Metadata

	execCtx := &a2asrv.ExecutorContext{
		ContextID: chatID,
		Message:   msg,
		Metadata:  make(map[string]any),
	}
	if req.RunDir != "" {
		execCtx.Metadata["run_dir"] = req.RunDir
	}
	if req.Model != "" {
		execCtx.Metadata["model"] = req.Model
	}
	for k, v := range req.Metadata {
		execCtx.Metadata[k] = v
	}

	var exec a2asrv.AgentExecutor
	if targetAgent.Config.Type == "workflow" {
		exec = s.newWorkflowExecutor(targetAgent)
	} else {
		exec = NewSingleAgentExecutor(targetAgent, s.conf, s.repo, s, nil)
	}

	if exec == nil {
		s.activeExecutions.Delete(chatID)
		http.Error(w, `{"error":"failed to create agent executor"}`, http.StatusInternalServerError)
		return
	}

	// Async mode (State Sync Plan Phase 1):
	// Drain executor iterator in a background goroutine.
	// We deliberately discard the yielded SDK execution events here because all real state changes
	// (user prompt, status updates, agent messages, artifacts, done) are already persisted to DB
	// and broadcast to SSE subscribers via SessionEventHub emit-after-write hooks.
	go func() {
		defer s.activeExecutions.Delete(chatID)
		for _, err := range exec.Execute(s.Context(), execCtx) {
			if err != nil {
				log.Error().Err(err).Str("chat_id", chatID).Str("agent", targetAgent.Config.ID).Msg("async executor execution error")
			}
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "accepted",
		"chatId": chatID,
	})
}
