package api

import (
	"encoding/json"
	"io"
	"net"
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

	// Forward the exact incoming request URL path to ttyd since ttyd expects its -b prefix
	req := r.Clone(r.Context())

	// Check if this is a WebSocket upgrade request
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		// Hijack the HTTP connection for WebSocket proxying
		targetConn, err := net.Dial("unix", inst.SocketPath)
		if err != nil {
			log.Error().Err(err).Msg("failed to dial unix socket for websocket")
			http.Error(w, "Failed to connect to ttyd socket", http.StatusInternalServerError)
			return
		}
		defer func() { _ = targetConn.Close() }()

		hj, ok := w.(http.Hijacker)
		if !ok {
			log.Error().Msg("webserver does not support connection hijacking")
			http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
			return
		}

		clientConn, brw, err := hj.Hijack()
		if err != nil {
			log.Error().Err(err).Msg("failed to hijack client connection")
			return
		}
		defer func() { _ = clientConn.Close() }()

		targetPath := req.URL.Path
		if req.URL.RawQuery != "" {
			targetPath += "?" + req.URL.RawQuery
		}

		// Send initial HTTP request to ttyd
		req.URL.Path = targetPath
		if err := req.Write(targetConn); err != nil {
			log.Error().Err(err).Msg("failed to write request to ttyd socket")
			return
		}

		// Flush any buffered reader data from hijacking before starting bi-directional copy
		errChan := make(chan error, 2)
		go func() {
			var err error
			if brw.Reader.Buffered() > 0 {
				buf := make([]byte, brw.Reader.Buffered())
				_, err = brw.Read(buf)
				if err == nil {
					_, err = targetConn.Write(buf)
				}
			}
			if err == nil {
				_, err = io.Copy(targetConn, clientConn)
			}
			errChan <- err
		}()

		go func() {
			_, err := io.Copy(clientConn, targetConn)
			errChan <- err
		}()

		<-errChan
		return
	}

	inst.Proxy.ServeHTTP(w, req)
}
