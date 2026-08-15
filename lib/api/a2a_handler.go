package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

// NewAgentHandler creates the A2A HTTP REST handler and the AgentCard for the given agent.
func NewAgentHandler(agent *agents.Agent, conf *config.Config, repo *dbmodels.SessionRepository, server *Server) (http.Handler, *a2a.AgentCard) {
	executor := NewSingleAgentExecutor(agent, conf, repo, server, nil)
	handler := a2asrv.NewHandler(executor)
	restHandler := a2asrv.NewRESTHandler(handler)

	host := "http://localhost:8080"
	if conf != nil && conf.Host != "" {
		host = conf.Host
	}

	card := &a2a.AgentCard{
		Name:        agent.Config.Name,
		Description: agent.Config.Description,
		Version:     "1.0.0",
		Capabilities: a2a.AgentCapabilities{
			Streaming: true,
		},
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(fmt.Sprintf("%s/agents/%s", host, agent.Config.ID), a2a.TransportProtocolHTTPJSON),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}

	return restHandler, card
}

// AgentInfo holds details about an agent for the frontend UI.
type AgentInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	RunDirs     []string `json:"run_dirs"`
	MainAgent   bool     `json:"main_agent"`
	Models      []string `json:"models"`
}

// handleAgents handles GET /agents to list loaded agent names.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	richAgents := make([]AgentInfo, 0, len(s.agents))
	for _, agent := range s.agents {
		models := make([]string, 0, len(agent.Config.CLI))
		for _, target := range agent.Config.CLI {
			if target.Model != "" {
				models = append(models, target.Model)
			}
		}

		richAgents = append(richAgents, AgentInfo{
			ID:          agent.Config.ID,
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
