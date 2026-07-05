package a2aagent

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
)

// Server manages the HTTP server hosting A2A agents.
type Server struct {
	conf   *config.Config
	mu     sync.RWMutex
	agents []*agents.Agent
	mux    *http.ServeMux
	repo   *dbmodels.SessionRepository
}

// New creates a new Server instance, loading all agents from the configured directory.
func New(conf *config.Config, dbConn *gorm.DB) (*Server, error) {
	var repo *dbmodels.SessionRepository
	if dbConn != nil {
		repo = dbmodels.NewSessionRepository(dbConn)
	}

	s := &Server{
		conf: conf,
		repo: repo,
	}

	if err := s.reload(); err != nil {
		return nil, fmt.Errorf("failed to load agents: %w", err)
	}

	return s, nil
}

// ServeHTTP delegates HTTP requests to the current active ServeMux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	mux := s.mux
	s.mu.RUnlock()
	mux.ServeHTTP(w, r)
}

func (s *Server) buildMuxLocked() *http.ServeMux {
	mux := http.NewServeMux()

	for _, agent := range s.agents {
		restHandler, card := NewAgentHandler(agent, s.conf.Host, s.repo)

		prefix := fmt.Sprintf("/agents/%s/", agent.Config.ID)
		mux.Handle(prefix, http.StripPrefix(fmt.Sprintf("/agents/%s", agent.Config.ID), restHandler))

		cardHandler := a2asrv.NewStaticAgentCardHandler(card)
		mux.Handle(prefix+strings.TrimPrefix(a2asrv.WellKnownAgentCardPath, "/"), cardHandler)

		log.Info().Msgf("Registered agent %s at /agents/%s/", agent.Config.Name, agent.Config.ID)
	}

	mux.HandleFunc("POST /manage/reload", s.handleReload)

	return mux
}

// Start starts the HTTP server hosting A2A agents with graceful shutdown.
func (s *Server) Start() error {
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.conf.Port),
		Handler: s,
	}

	// Channel to listen for errors from Server.ListenAndServe()
	serverErrors := make(chan error, 1)

	go func() {
		log.Info().Msgf("Starting HTTP server on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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

		// Context with timeout for graceful shutdown
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			// Force shutdown if graceful fails
			if err := srv.Close(); err != nil {
				log.Error().Err(err).Msg("Failed to close HTTP server")
			}
			return fmt.Errorf("graceful shutdown failed: %w", err)
		}
		log.Info().Msg("Server gracefully stopped")
	}

	return nil
}
