package api

import (
	"encoding/json"
	"net/http"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
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
	// RunnerID is the owning runner of a runner-scoped agent (empty for
	// global ones).
	RunnerID string `json:"runner_id,omitempty"`
}

// handleAgents handles GET /api/agents to list loaded agent metadata.
//
// With ?runner=<id> it lists the agents that runner can run (global plus the
// runner's own); without it, the global agents.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	list := func() []*agentspec.Agent {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return append([]*agentspec.Agent(nil), s.agents...)
	}()
	if runner := r.URL.Query().Get("runner"); runner != "" {
		var err error
		if list, err = s.agentsFor(runner); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to list agents of runner: "+err.Error())
			return
		}
	}

	richAgents := make([]AgentInfo, 0, len(list))
	for _, agent := range list {
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
			RunnerID:    agent.NodeID,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(richAgents)
}
