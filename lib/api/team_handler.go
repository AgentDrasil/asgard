package api

import (
	"encoding/json"
	"net/http"

	"github.com/AgentDrasil/asgard/lib/agents"
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

	if s.repo == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "session repository not initialized"})
		return
	}

	session, err := s.repo.GetSession(chatID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to get session: " + err.Error()})
		return
	}

	if session == nil || session.CurrentAgent == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "session or current agent not found"})
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var currentAgent *agents.Agent
	for _, a := range s.agents {
		if a.Config.Name == session.CurrentAgent {
			currentAgent = a
			break
		}
	}

	if currentAgent == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "current agent not loaded"})
		return
	}

	team := currentAgent.Config.Team
	teamAgents := []string{}

	if team != "" {
		for _, a := range s.agents {
			if a.Config.Team == team && a.Config.ID != currentAgent.Config.ID {
				teamAgents = append(teamAgents, a.Config.ID)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(teamAgents)
}
