package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
	"github.com/AgentDrasil/asgard/lib/ttyd"
)

// Server manages the HTTP server hosting A2A agents.
type Server struct {
	conf            *config.Config
	mu              sync.RWMutex
	agents          []*agents.Agent
	mux             *http.ServeMux
	repo            *dbmodels.SessionRepository
	statusListeners map[string][]chan AgentStatusUpdate
	ttydManager     *ttyd.Manager
}

// New creates a new Server instance, loading all agents from the configured directory.
func New(conf *config.Config, dbConn *gorm.DB) (*Server, error) {
	var repo *dbmodels.SessionRepository
	if dbConn != nil {
		repo = dbmodels.NewSessionRepository(dbConn)
	}

	ttydMgr, err := ttyd.NewManager("")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize ttyd manager: %w", err)
	}

	s := &Server{
		conf:        conf,
		repo:        repo,
		ttydManager: ttydMgr,
	}

	if err := s.reload(); err != nil {
		return nil, fmt.Errorf("failed to load agents: %w", err)
	}

	return s, nil
}

// ServeHTTP delegates HTTP requests to the current active ServeMux, adding CORS support.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS Headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, X-Requested-With")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	s.mu.RLock()
	mux := s.mux
	s.mu.RUnlock()
	mux.ServeHTTP(w, r)
}

func (s *Server) buildMuxLocked() *http.ServeMux {
	mux := http.NewServeMux()

	statusURL := fmt.Sprintf("http://127.0.0.1:%d/agent-status", s.conf.InternalPort)

	for _, agent := range s.agents {
		restHandler, card := NewAgentHandler(agent, s.conf.Host, s.repo, s, statusURL)

		prefix := fmt.Sprintf("/agents/%s/", agent.Config.ID)
		agentBase := fmt.Sprintf("/agents/%s", agent.Config.ID)

		// Standard routes: /agents/{id}/message:stream etc.
		mux.Handle(prefix, http.StripPrefix(agentBase, restHandler))

		// Compat routes for @a2a-js/sdk@0.x which:
		//   1. Prefixes all paths with /v1/  (e.g. /v1/message:stream)
		//   2. Sends message.content instead of message.parts
		v1Prefix := prefix + "v1/"
		mux.Handle(v1Prefix, http.StripPrefix(agentBase+"/v1", rewriteContentToParts(restHandler)))

		cardHandler := a2asrv.NewStaticAgentCardHandler(card)
		mux.Handle(prefix+strings.TrimPrefix(a2asrv.WellKnownAgentCardPath, "/"), cardHandler)

		log.Info().Msgf("Registered agent %s at /agents/%s/ (+ /v1/ compat)", agent.Config.Name, agent.Config.ID)
	}

	mux.HandleFunc("GET /team", s.handleTeam)
	mux.HandleFunc("POST /api/manage/reload", s.handleReload)
	mux.HandleFunc("GET /api/agents", s.handleAgents)
	mux.HandleFunc("GET /api/quota", s.handleQuota)
	mux.HandleFunc("GET /api/sessions", s.handleSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSessionByID)
	mux.HandleFunc("POST /api/sessions", s.handleSessions)
	mux.HandleFunc("DELETE /api/sessions", s.handleSessions)
	mux.HandleFunc("/api/ttyd/{session_id...}", s.handleTTYD)

	return mux
}

// Start starts the public HTTP server and an internal-only loopback HTTP server
// for agent status callbacks. Both shut down gracefully on SIGINT/SIGTERM.
func (s *Server) Start() error {
	// ── Public server ────────────────────────────────────────────────────────
	publicSrv := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.conf.Port),
		Handler: s,
	}

	// ── Internal server (loopback only) ──────────────────────────────────────
	internalMux := http.NewServeMux()
	internalMux.HandleFunc("POST /agent-status", s.handleAgentStatus)
	internalSrv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", s.conf.InternalPort),
		Handler: internalMux,
	}

	serverErrors := make(chan error, 2)

	go func() {
		log.Info().Msgf("Starting public HTTP server on %s", publicSrv.Addr)
		if err := publicSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrors <- err
		}
	}()

	go func() {
		log.Info().Msgf("Starting internal HTTP server on %s", internalSrv.Addr)
		if err := internalSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrors <- err
		}
	}()

	// Channel to listen for interrupt/terminate signals
	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		return fmt.Errorf("server error: %w", err)

	case sig := <-shutdownSignals:
		log.Info().Msgf("Shutdown signal received: %v. Starting graceful shutdown...", sig)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		publicErr := publicSrv.Shutdown(shutdownCtx)
		internalErr := internalSrv.Shutdown(shutdownCtx)

		if publicErr != nil {
			if err := publicSrv.Close(); err != nil {
				log.Error().Err(err).Msg("Failed to close public HTTP server")
			}
			return fmt.Errorf("public server graceful shutdown failed: %w", publicErr)
		}
		if internalErr != nil {
			if err := internalSrv.Close(); err != nil {
				log.Error().Err(err).Msg("Failed to close internal HTTP server")
			}
			return fmt.Errorf("internal server graceful shutdown failed: %w", internalErr)
		}
		log.Info().Msg("Servers gracefully stopped")
	}

	return nil
}
