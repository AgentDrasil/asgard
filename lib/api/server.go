package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
	"github.com/AgentDrasil/asgard/lib/ttyd"
	"github.com/AgentDrasil/asgard/lib/workflow"
)

// Server manages the HTTP server hosting agents.
type Server struct {
	conf             *config.Config
	mu               sync.RWMutex
	agents           []*agents.Agent
	mux              *http.ServeMux
	repo             *dbmodels.SessionRepository
	statusListeners  map[string][]*statusListener
	ttydManager      *ttyd.Manager
	workflowEngine   *workflow.Engine
	eventHub         *SessionEventHub
	ctx              context.Context
	cancel           context.CancelFunc
	activeExecutions sync.Map // chatID -> struct{}
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

	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		conf:        conf,
		repo:        repo,
		ttydManager: ttydMgr,
		eventHub:    NewSessionEventHub(),
		ctx:         ctx,
		cancel:      cancel,
	}

	workflowEngine, err := newWorkflowEngine(conf, s)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize workflow engine: %w", err)
	}
	s.workflowEngine = workflowEngine

	if repo != nil {
		if err := repo.ResetAllRunningAgents(); err != nil {
			log.Warn().Err(err).Msg("failed to reset stale running agents on startup")
		}
		workflowEngine.SetRunStore(newWorkflowRunStore(dbmodels.NewWorkflowRunRepository(dbConn)))
		workflowEngine.SetHumanSuspender(s.suspendWorkflowHuman)
	}

	CheckPushNotificationSetup()

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

	for _, agent := range s.agents {
		log.Info().Msgf("Registered agent %s (%s)", agent.Config.Name, agent.Config.ID)
	}

	mux.HandleFunc("GET /team", s.handleTeam)
	mux.HandleFunc("POST /api/manage/reload", s.handleReload)
	mux.HandleFunc("GET /api/agents", s.handleAgents)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/quota", s.handleQuota)
	mux.HandleFunc("GET /api/subdirs", s.handleSubdirs)
	mux.HandleFunc("GET /api/git/diff", s.handleGitDiff)
	mux.HandleFunc("GET /api/sessions", s.handleSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSessionByID)
	mux.HandleFunc("GET /api/sessions/{id}/events", s.handleSessionEvents)
	mux.HandleFunc("POST /api/agents/{id}/message", s.handleTriggerMessage)
	mux.HandleFunc("POST /api/sessions", s.handleSessions)
	mux.HandleFunc("DELETE /api/sessions", s.handleSessions)
	mux.HandleFunc("/api/ask-user", s.handleAskUser)
	mux.HandleFunc("/api/ask-user/reply", s.handleAskUserReply)
	mux.HandleFunc("POST /api/push/tokens", s.handleRegisterPushToken)
	mux.HandleFunc("/api/ttyd/{session_id...}", s.handleTTYD)
	mux.HandleFunc("GET /api/v1/workspace/file", s.handleWorkspaceFile)

	if s.conf.WebUIPath != "" {
		fs := http.FileServer(http.Dir(s.conf.WebUIPath))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			path := filepath.Join(s.conf.WebUIPath, filepath.Clean(r.URL.Path))
			info, err := os.Stat(path)
			if err == nil && !info.IsDir() {
				fs.ServeHTTP(w, r)
				return
			}
			http.ServeFile(w, r, filepath.Join(s.conf.WebUIPath, "index.html"))
		})
		log.Info().Msgf("Registered static Web UI hosting from %s at /", s.conf.WebUIPath)
	}

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
	internalMux.HandleFunc("/agent-status", s.handleAgentStatus)
	internalMux.HandleFunc("/api/ask-user", s.handleAskUser)
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

		if s.cancel != nil {
			s.cancel()
		}

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
		if s.eventHub != nil {
			s.eventHub.Close()
		}
		log.Info().Msg("Servers gracefully stopped")
	}

	return nil
}

// Context returns the Server's root context (canceled on shutdown).
func (s *Server) Context() context.Context {
	if s == nil || s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// EventHub returns the Server's SessionEventHub instance.
func (s *Server) EventHub() *SessionEventHub {
	return s.eventHub
}

// PublishSessionEvent broadcasts a session event if the event hub is initialized.
func (s *Server) PublishSessionEvent(chatID string, ev SessionEvent) {
	if s == nil || s.eventHub == nil || chatID == "" {
		return
	}
	s.eventHub.Publish(chatID, ev)
}
