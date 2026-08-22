package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/backend/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/lib/api"
	"github.com/AgentDrasil/asgard/backend/lib/cleanup"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/sshagent"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
)

// Option mutates the Options during App initialization.
type Option func(*Options)

// Options holds configuration and injection options for App.
type Options struct {
	Config              *config.Config
	ConfigPath          string
	DB                  *gorm.DB
	FunctionRegistry    *workflow.FunctionRegistry
	Functions           map[string]workflow.WorkflowFunction
	CustomRunners       []workflow.NodeRunner
	SkipAgentValidation bool
	SkipSSHSetup        bool
}

// WithConfig injects a pre-loaded Config instance.
func WithConfig(conf *config.Config) Option {
	return func(o *Options) {
		o.Config = conf
	}
}

// WithConfigPath sets the path to the configuration file to load.
func WithConfigPath(path string) Option {
	return func(o *Options) {
		o.ConfigPath = path
	}
}

// WithDB injects a pre-configured database connection.
func WithDB(db *gorm.DB) Option {
	return func(o *Options) {
		o.DB = db
	}
}

// WithFunction registers a custom workflow function.
func WithFunction(name string, fn workflow.WorkflowFunction) Option {
	return func(o *Options) {
		if fn == nil {
			return
		}
		if o.Functions == nil {
			o.Functions = make(map[string]workflow.WorkflowFunction)
		}
		o.Functions[name] = fn
	}
}

// WithFunctionRegistry injects a custom workflow function registry.
func WithFunctionRegistry(reg *workflow.FunctionRegistry) Option {
	return func(o *Options) {
		o.FunctionRegistry = reg
	}
}

// WithNodeRunner injects a single custom workflow node runner.
func WithNodeRunner(runner workflow.NodeRunner) Option {
	return func(o *Options) {
		if runner != nil {
			o.CustomRunners = append(o.CustomRunners, runner)
		}
	}
}

// WithCustomRunners injects multiple custom workflow node runners.
func WithCustomRunners(runners ...workflow.NodeRunner) Option {
	return func(o *Options) {
		for _, r := range runners {
			if r != nil {
				o.CustomRunners = append(o.CustomRunners, r)
			}
		}
	}
}

// WithSkipAgentValidation configures whether to skip CLI agent setup validation.
func WithSkipAgentValidation(skip bool) Option {
	return func(o *Options) {
		o.SkipAgentValidation = skip
	}
}

// WithSkipSSHSetup configures whether to skip SSH agent setup.
func WithSkipSSHSetup(skip bool) Option {
	return func(o *Options) {
		o.SkipSSHSetup = skip
	}
}

// App encapsulates the Asgard runtime environment and lifecycle.
type App struct {
	conf      *config.Config
	db        *gorm.DB
	server    *api.Server
	scheduler *cleanup.Scheduler
	stopOnce  sync.Once
	stopErr   error
}

func setupLogger(conf *config.Config) {
	if conf != nil && conf.Debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
		log.Warn().Msg("Debug mode is enabled")
	}
}

// New creates and initializes an App instance according to the provided options.
func New(opts ...Option) (*App, error) {
	options := &Options{}
	for _, opt := range opts {
		if opt != nil {
			opt(options)
		}
	}

	// 1. Resolve configuration
	conf := options.Config
	if conf == nil {
		path := options.ConfigPath
		if path == "" {
			path = os.Getenv("CONFIG_PATH")
			if path == "" {
				path = "config.yaml"
			}
		}
		var err error
		conf, err = config.LoadConfig(path)
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
	}

	// 2. Validate CLI agent setups (unless skipped)
	if !options.SkipAgentValidation {
		if err := agentwrapper.ValidateAgySetup(); err != nil {
			return nil, fmt.Errorf("agy agent setup validation failed: %w", err)
		}
		if err := agentwrapper.ValidateOpencodeSetup(); err != nil {
			return nil, fmt.Errorf("opencode agent setup validation failed: %w", err)
		}
	}

	// 3. Configure Logger
	setupLogger(conf)

	// 4. Initialize SSH Agent (unless skipped)
	if !options.SkipSSHSetup {
		if err := sshagent.SetupSSHAgent(); err != nil {
			return nil, fmt.Errorf("failed to setup SSH agent: %w", err)
		}
	}

	// 5. Connect and migrate database
	database := options.DB
	if database == nil {
		var err error
		database, err = db.NewDB(conf)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to database: %w", err)
		}
	}

	if err := dbmodels.AutoMigrate(database); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	// 6. Initialize cleanup scheduler
	repo := dbmodels.NewSessionRepository(database)
	scheduler, err := cleanup.NewScheduler(repo)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cleanup scheduler: %w", err)
	}

	// 7. Assemble API Server with options
	var apiOpts []api.ServerOption
	if options.FunctionRegistry != nil {
		apiOpts = append(apiOpts, api.WithFunctionRegistry(options.FunctionRegistry))
	}
	for name, fn := range options.Functions {
		apiOpts = append(apiOpts, api.WithFunction(name, fn))
	}
	if len(options.CustomRunners) > 0 {
		apiOpts = append(apiOpts, api.WithCustomRunners(options.CustomRunners...))
	}

	srv, err := api.New(conf, database, apiOpts...)
	if err != nil {
		_ = scheduler.Shutdown()
		return nil, fmt.Errorf("failed to initialize API server: %w", err)
	}

	return &App{
		conf:      conf,
		db:        database,
		server:    srv,
		scheduler: scheduler,
	}, nil
}

// Config returns the resolved configuration of the App.
func (a *App) Config() *config.Config {
	if a == nil {
		return nil
	}
	return a.conf
}

// DB returns the underlying database connection of the App.
func (a *App) DB() *gorm.DB {
	if a == nil {
		return nil
	}
	return a.db
}

// Start starts the underlying HTTP server and blocks until exit.
func (a *App) Start() error {
	if a == nil || a.server == nil {
		return errors.New("app server is not initialized")
	}
	return a.server.Start()
}

// Stop performs idempotent graceful shutdown on both the server and the cleanup scheduler.
func (a *App) Stop(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.stopOnce.Do(func() {
		var errs []error
		if a.server != nil {
			if err := a.server.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("server shutdown failed: %w", err))
			}
		}
		if a.scheduler != nil {
			if err := a.scheduler.Shutdown(); err != nil {
				errs = append(errs, fmt.Errorf("scheduler shutdown failed: %w", err))
			}
		}
		a.stopErr = errors.Join(errs...)
	})
	return a.stopErr
}

// Run creates, starts, and manages the lifecycle of an App instance.
func Run(ctx context.Context, opts ...Option) error {
	a, err := New(opts...)
	if err != nil {
		return err
	}

	// Goroutine watching context cancellation to initiate graceful shutdown
	stopDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = a.Stop(shutdownCtx)
		case <-stopDone:
		}
	}()

	startErr := a.Start()
	close(stopDone)

	// Ensure cleanup is performed on all exit paths (idempotent due to sync.Once)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stopErr := a.Stop(shutdownCtx)

	if startErr != nil {
		if ctx.Err() != nil && errors.Is(startErr, api.ErrServerShutdownBeforeStart) {
			return nil
		}
		return startErr
	}
	return stopErr
}
