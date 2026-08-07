package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
)

// handleTTYD handles reverse proxy requests to /api/ttyd/{session_id...}
func (s *Server) handleTTYD(w http.ResponseWriter, r *http.Request) {
	if s.ttydManager == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "ttyd manager not initialized"})
		return
	}

	rawPath := r.PathValue("session_id")
	if rawPath == "" {
		// Fallback for path stripping / wildcard routing
		rawPath = strings.TrimPrefix(r.URL.Path, "/api/ttyd/")
	}

	parts := strings.SplitN(rawPath, "/", 2)
	sessionKey := parts[0]

	if sessionKey == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "session_id is required"})
		return
	}

	// Determine working directory for session
	workingDir := ""
	if sessionKey == "sidebar" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			workingDir = home
		}
	} else if strings.HasPrefix(sessionKey, "agent-") {
		agentSessionID := strings.TrimPrefix(sessionKey, "agent-")
		if s.repo != nil {
			sess, err := s.repo.GetSession(agentSessionID)
			if err != nil {
				log.Error().Err(err).Str("agent_session_id", agentSessionID).Msg("failed to query agent session for ttyd working directory")
			} else if sess == nil {
				log.Warn().Str("agent_session_id", agentSessionID).Msg("agent session not found for ttyd working directory")
			} else if sess.RunDir != "" {
				workingDir = sess.RunDir
			} else {
				log.Warn().Str("agent_session_id", agentSessionID).Msg("agent session RunDir is empty")
			}
		}
	}

	inst, err := s.ttydManager.GetOrStart(sessionKey, workingDir)
	if err != nil {
		log.Error().Err(err).Str("session_key", sessionKey).Msg("failed to get or start ttyd session")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to start ttyd session: " + err.Error()})
		return
	}

	// Serve request via reverse proxy (httputil.ReverseProxy handles HTTP & WebSocket hijacking transparently)
	inst.Proxy.ServeHTTP(w, r)
}
