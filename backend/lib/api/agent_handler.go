package api

import (
	"encoding/json"
	"net/http"
)

// AgentInfo holds details about an agent for the frontend UI.
type AgentInfo struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	RunDirs     []string `json:"run_dirs"`
	MainAgent   bool     `json:"main_agent"`
	Models      []string `json:"models"`
}

// handleAgents handles GET /api/agents to list loaded agent metadata.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	richAgents := make([]AgentInfo, 0, len(s.agents))
	for _, agent := range s.agents {
		models := make([]string, 0, len(agent.Config.CLI))
		for _, target := range agent.Config.CLI {
			if target.Model != "" && s.conf.IsProviderEnabled(target.CLI) {
				models = append(models, target.Model)
			}
		}

		agentType := agent.Config.Type
		if agentType == "" {
			agentType = "agent"
		}

		richAgents = append(richAgents, AgentInfo{
			ID:          agent.Config.ID,
			Type:        agentType,
			Name:        agent.Config.Name,
			Description: agent.Config.Description,
			Icon:        agent.Config.Icon,
			RunDirs:     agent.Config.RunDirs,
			MainAgent:   agent.Config.IsMainAgent(),
			Models:      models,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(richAgents)
}
