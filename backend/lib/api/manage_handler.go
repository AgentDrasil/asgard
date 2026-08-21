package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/lib/config"
)

const agentFatherID = "agent_father"

// Reload reloads the agent configurations and refreshes the HTTP handlers.
func (s *Server) reload() error {
	loader := agents.NewLoader(s.conf.AgentDir)
	agents, err := loader.LoadAll()
	if err != nil {
		return err
	}

	hasAgentFather := false
	for _, a := range agents {
		if a.Config.ID == agentFatherID {
			hasAgentFather = true
			break
		}
	}

	if !hasAgentFather {
		return fmt.Errorf("agent_father is required as the initial root agent, but was not found in the agents directory (%s). You can clone the default agents via: git clone https://github.com/AgentDrasil/asgard-agents.git %s", s.conf.AgentDir, s.conf.AgentDir)
	}

	s.mu.Lock()
	s.agents = agents
	if s.workflowEngine != nil {
		s.workflowEngine.SetAgents(agents)
	}
	s.mux = s.buildMuxLocked()
	s.mu.Unlock()

	return nil
}

// handleReload handles POST /api/manage/reload.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := s.reload(); err != nil {
		log.Error().Err(err).Msg("Failed to reload agents")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "agents reloaded"})
}

// handleQuota handles GET /api/quota.
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	res, err := agentwrapper.GetQuota(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("Failed to fetch quota info")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(res)
}

// ConfigResponse represents the public configuration sent to web clients.
type ConfigResponse struct {
	FirebaseWebpushWeb *config.FirebaseWebpushWebConfig `json:"firebase_webpush_web,omitempty"`
}

// handleConfig handles GET /api/config.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	resp := ConfigResponse{
		FirebaseWebpushWeb: s.conf.FirebaseWebpushWeb,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
