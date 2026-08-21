package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
)

// TriggerMessageRequest represents the payload for POST /api/agents/{id}/message.
type TriggerMessageRequest struct {
	Prompt   string         `json:"prompt"`
	ChatID   string         `json:"chatId,omitempty"`
	RunDir   string         `json:"runDir,omitempty"`
	Model    string         `json:"model,omitempty"`
	Wait     bool           `json:"wait,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// handleTriggerMessage handles POST /api/agents/{id}/message, launching agent execution
// either asynchronously (202 Accepted) or synchronously (200 OK when wait=true).
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

	wait := req.Wait || r.URL.Query().Get("wait") == "true"

	runTask := func(ctx context.Context) (status string, output string, err error) {
		if targetAgent.Config.Type == "workflow" {
			return s.runWorkflow(ctx, targetAgent, chatID, req)
		}
		return s.runSingleAgent(ctx, targetAgent, chatID, req)
	}

	if wait {
		defer s.activeExecutions.Delete(chatID)
		status, output, err := runTask(s.Context())
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": status,
				"error":  err.Error(),
				"chatId": chatID,
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": status,
			"output": output,
			"chatId": chatID,
		})
		return
	}

	// Async mode:
	go func() {
		defer s.activeExecutions.Delete(chatID)
		_, _, err := runTask(s.Context())
		if err != nil {
			log.Error().Err(err).Str("chat_id", chatID).Str("agent", targetAgent.Config.ID).Msg("async executor execution error")
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "accepted",
		"chatId": chatID,
	})
}

// runSingleAgent executes a single CLI agent synchronously, returning its final assistant text.
func (s *Server) runSingleAgent(ctx context.Context, agent *agents.Agent, chatID string, req TriggerMessageRequest) (status string, output string, err error) {
	exec := NewSingleAgentExecutor(agent, s.conf, s.repo, s, nil)
	out, err := exec.Execute(ctx, SingleAgentRunParams{
		ChatID:   chatID,
		Prompt:   req.Prompt,
		RunDir:   req.RunDir,
		Model:    req.Model,
		Metadata: req.Metadata,
	})
	if err != nil {
		return "failed", "", err
	}
	return "completed", out, nil
}
