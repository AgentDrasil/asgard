package a2aagent

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agents"
)

// Reload reloads the agent configurations and refreshes the HTTP handlers.
func (s *Server) reload() error {
	loader := agents.NewLoader(s.conf.AgentDir)
	agents, err := loader.LoadAll()
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.agents = agents
	s.mux = s.buildMuxLocked()
	s.mu.Unlock()

	log.Info().Msg("Agents reloaded and handlers refreshed successfully")
	return nil
}

// handleReload handles HTTP requests to trigger agent reloading.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := s.reload(); err != nil {
		log.Error().Err(err).Msg("Failed to reload agents via HTTP")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "agents reloaded"})
}
