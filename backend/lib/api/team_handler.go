package api

import (
	"encoding/json"
	"net/http"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// handleTeam handles requests to get other team agents for a given chat ID.
func (s *Server) handleTeam(w http.ResponseWriter, r *http.Request) {
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

	if !s.requireRepo(w) {
		return
	}

	teamAgents, status, msg := s.teamAgentsFor(chatID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if msg != "" {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
		return
	}
	_ = json.NewEncoder(w).Encode(teamAgents)
}

// teamAgentsFor returns the ids of the other agents on the team of the chat's
// current agent. On failure it returns the HTTP status and message to report.
// The repo must be set.
func (s *Server) teamAgentsFor(chatID string) (teamAgents []string, status int, msg string) {
	session, err := s.repo.GetSession(chatID)
	if err != nil {
		return nil, http.StatusInternalServerError, "failed to get session: " + err.Error()
	}

	if session == nil || session.CurrentAgent == "" {
		return nil, http.StatusNotFound, "session or current agent not found"
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var currentAgent *agentspec.Agent
	for _, a := range s.agents {
		if a.Config.Name == session.CurrentAgent || a.Config.ID == session.CurrentAgent {
			currentAgent = a
			break
		}
	}

	if currentAgent == nil {
		return nil, http.StatusNotFound, "current agent not loaded"
	}

	team := currentAgent.Config.Team
	teamAgents = []string{}

	if team != "" {
		for _, a := range s.agents {
			if a.Config.Team == team && a.Config.ID != currentAgent.Config.ID {
				teamAgents = append(teamAgents, a.Config.ID)
			}
		}
	}
	return teamAgents, http.StatusOK, ""
}
