package api

import (
	"context"
	"errors"
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

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/trigger"
	"github.com/AgentDrasil/asgard/backend/lib/ttyd"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
)

// ErrServerShutdownBeforeStart is returned by Start when Shutdown was called before or during Start.
var ErrServerShutdownBeforeStart = errors.New("server shut down before start completed")

// Server manages the HTTP server hosting agents.
type Server struct {
	conf             *config.Config
	mu               sync.RWMutex
	agents           []*agents.Agent
	mux              *http.ServeMux
	repo             *dbmodels.SessionRepository
	workflowRunRepo  *dbmodels.WorkflowRunRepository
	statusListeners  map[string][]*statusListener
	ttydManager      *ttyd.Manager
	workflowEngine   *workflow.Engine
	cronManager      *trigger.WorkflowCronManager
	eventHub         *SessionEventHub
	ctx              context.Context
	cancel           context.CancelFunc
	activeExecutions sync.Map // chatID -> struct{}
	funcRegistry     *workflow.FunctionRegistry
	customRunners    []workflow.NodeRunner
	publicSrv        *http.Server
	internalSrv      *http.Server
	shutdownStarted  chan struct{}
	shutdownOnce     sync.Once
	shutdownErr      error
}

// ServerOption mutates a Server during construction (functional options).
type ServerOption func(*Server)

// WithFunctionRegistry replaces the Server's workflow function registry.
func WithFunctionRegistry(reg *workflow.FunctionRegistry) ServerOption {
	return func(s *Server) {
		s.funcRegistry = reg
	}
}

// WithFunction injects a single Go-native function into the Server's registry.
// The registry is lazily created with the process-wide default registry as
// parent so globally registered functions remain resolvable.
func WithFunction(name string, fn workflow.WorkflowFunction) ServerOption {
	return func(s *Server) {
		if s.funcRegistry == nil {
			s.funcRegistry = workflow.NewFunctionRegistryWithParent(workflow.DefaultFunctionRegistry())
		}
		s.funcRegistry.Register(name, fn)
	}
}

// WithNodeRunner injects a single custom NodeRunner, replacing the default
// runner for every NodeType it supports.
func WithNodeRunner(runner workflow.NodeRunner) ServerOption {
	return func(s *Server) {
		if runner == nil {
			return
		}
		s.customRunners = append(s.customRunners, runner)
	}
}

// WithCustomRunners injects multiple custom NodeRunners.
func WithCustomRunners(runners ...workflow.NodeRunner) ServerOption {
	return func(s *Server) {
		for _, runner := range runners {
			if runner == nil {
				continue
			}
			s.customRunners = append(s.customRunners, runner)
		}
	}
}

// New creates a new Server instance, loading all agents from the configured directory.
func New(conf *config.Config, dbConn *gorm.DB, opts ...ServerOption) (*Server, error) {
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
		conf:            conf,
		repo:            repo,
		ttydManager:     ttydMgr,
		eventHub:        NewSessionEventHub(),
		ctx:             ctx,
		cancel:          cancel,
		shutdownStarted: make(chan struct{}),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	workflowEngine, err := newWorkflowEngine(conf, s, s.funcRegistry, s.resolveWorkflowDefinition, s.customRunners...)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize workflow engine: %w", err)
	}
	s.workflowEngine = workflowEngine

	if repo != nil {
		if err := repo.ResetAllRunningAgents(); err != nil {
			log.Warn().Err(err).Msg("failed to reset stale running agents on startup")
		}
		wfRepo := dbmodels.NewWorkflowRunRepository(dbConn)
		s.workflowRunRepo = wfRepo
		if err := wfRepo.ResetAllRunningWorkflows(); err != nil {
			log.Warn().Err(err).Msg("failed to reset stale running workflows on startup")
		}
		workflowEngine.SetRunStore(newWorkflowRunStore(wfRepo))
		workflowEngine.SetHumanSuspender(s.suspendWorkflowHuman)
	}

	CheckPushNotificationSetup()

	cronMgr, err := trigger.NewWorkflowCronManager(repo, s.runWorkflowCronTrigger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize workflow cron manager: %w", err)
	}
	s.cronManager = cronMgr

	if err := s.reload(); err != nil {
		_ = cronMgr.Shutdown()
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
	mux.HandleFunc("GET /api/git/log", s.handleGitLog)
	mux.HandleFunc("POST /api/git/push", s.handleGitPush)
	mux.HandleFunc("POST /api/git/pull", s.handleGitPull)
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
	mux.HandleFunc("GET /api/files/tree", s.handleFilesTree)
	mux.HandleFunc("GET /api/files/content", s.handleFilesContent)
	mux.HandleFunc("GET /api/files/search", s.handleFilesSearch)

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
// for agent status callbacks. Both shut down gracefully on SIGINT/SIGTERM or
// when the Server's root context is canceled.
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

	s.mu.Lock()
	s.publicSrv = publicSrv
	s.internalSrv = internalSrv
	s.mu.Unlock()

	select {
	case <-s.shutdownStarted:
		// Shutdown was called before or during handle registration; clean up immediately.
		_ = publicSrv.Close()
		_ = internalSrv.Close()
		return ErrServerShutdownBeforeStart
	default:
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
	defer signal.Stop(shutdownSignals)

	select {
	case err := <-serverErrors:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if shErr := s.Shutdown(shutdownCtx); shErr != nil {
			log.Error().Err(shErr).Msg("Failed to shut down servers after error")
		}
		return fmt.Errorf("server error: %w", err)

	case sig := <-shutdownSignals:
		log.Info().Msgf("Shutdown signal received: %v. Starting graceful shutdown...", sig)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := s.Shutdown(shutdownCtx); err != nil {
			return err
		}
		log.Info().Msg("Servers gracefully stopped")

	case <-s.ctx.Done():
		log.Info().Msg("Server context canceled. Starting graceful shutdown...")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := s.Shutdown(shutdownCtx); err != nil {
			return err
		}
		log.Info().Msg("Servers gracefully stopped")
	}

	return nil
}

// Shutdown gracefully stops the HTTP servers and the event hub. It is idempotent:
// only the first invocation performs the shutdown; subsequent calls return the
// cached result without side effects.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.shutdownOnce.Do(func() {
		var errs []error

		if s.shutdownStarted != nil {
			close(s.shutdownStarted)
		}

		if s.cancel != nil {
			s.cancel()
		}

		s.mu.RLock()
		publicSrv, internalSrv := s.publicSrv, s.internalSrv
		s.mu.RUnlock()

		if publicSrv != nil {
			if err := publicSrv.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("public server graceful shutdown failed: %w", err))
			}
		}
		if internalSrv != nil {
			if err := internalSrv.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("internal server graceful shutdown failed: %w", err))
			}
		}
		if s.eventHub != nil {
			s.eventHub.Close()
		}
		if s.cronManager != nil {
			if err := s.cronManager.Shutdown(); err != nil {
				errs = append(errs, fmt.Errorf("cron manager shutdown failed: %w", err))
			}
		}

		s.shutdownErr = errors.Join(errs...)
	})
	return s.shutdownErr
}

// runWorkflowCronTrigger executes a scheduled workflow on behalf of WorkflowCronManager,
// participating in the activeExecutions mutual-exclusion guard.
func (s *Server) runWorkflowCronTrigger(ctx context.Context, agent *agents.Agent, chatID, prompt string, headless bool) error {
	if _, running := s.activeExecutions.LoadOrStore(chatID, struct{}{}); running {
		log.Warn().Str("chatId", chatID).Msg("skip cron cycle: execution already in flight")
		return nil
	}
	defer s.activeExecutions.Delete(chatID)
	_, _, err := s.runWorkflow(ctx, agent, chatID, TriggerMessageRequest{Prompt: prompt, Headless: headless})
	return err
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

// isSessionRunning determines if a session is currently executing an agent or workflow.
// It checks in-memory active executions, session agent status, and active workflow runs in DB.
func (s *Server) isSessionRunning(sess *dbmodels.Session) bool {
	if sess == nil {
		return false
	}
	if sess.ChatID != "" {
		if _, running := s.activeExecutions.Load(sess.ChatID); running {
			return true
		}
	}
	if sess.IsRunning() {
		return true
	}
	if s.workflowRunRepo != nil && sess.ChatID != "" {
		if running, err := s.workflowRunRepo.HasRunningRunBySession(sess.ChatID); err == nil && running {
			return true
		}
	}
	return false
}
