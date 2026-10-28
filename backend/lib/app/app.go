package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/elmhuangyu/configs/pkg/configlib"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/lib/api"
	"github.com/AgentDrasil/asgard/backend/lib/cleanup"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/proxy"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/backend/lib/sshagent"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

// Option mutates the Options during App initialization.
type Option func(*Options)

// Options holds configuration and injection options for App.
type Options struct {
	Config              *config.Config
	ConfigPath          string
	TreeManager         *configlib.ConfigManager[config.AsgardTreeConfig]
	EtcdClient          *clientv3.Client
	DB                  *gorm.DB
	FunctionRegistry    *workflow.FunctionRegistry
	Functions           map[string]workflow.WorkflowFunction
	CustomRunners       []workflow.NodeRunner
	SkipAgentValidation bool
	SkipSSHSetup        bool
}

// WithTreeManager injects a pre-configured ConfigManager for AsgardTreeConfig (useful for tests or custom sources).
func WithTreeManager(mgr *configlib.ConfigManager[config.AsgardTreeConfig]) Option {
	return func(o *Options) {
		o.TreeManager = mgr
	}
}

// WithEtcdClient injects a pre-configured etcd client.
func WithEtcdClient(client *clientv3.Client) Option {
	return func(o *Options) {
		o.EtcdClient = client
	}
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
	conf         *config.Config
	treeManager  *configlib.ConfigManager[config.AsgardTreeConfig]
	etcdClient   *clientv3.Client
	agentStore   *config.AgentStore
	db           *gorm.DB
	server       *api.Server
	scheduler    *cleanup.Scheduler
	proxyManager *proxy.ProxyManager
	runnerHub    *runnerhub.Hub
	stopOnce     sync.Once
	stopErr      error
}

// loggerOnce serializes the process-global logging configuration below.
// zerolog's level and the log package's Logger are process-wide singletons, so
// they must be configured exactly once: reconfiguring them while other
// goroutines log is a data race.
var loggerOnce sync.Once

// setupLogger applies the debug logging configuration of the first App built in
// this process. Later constructions are no-ops because the globals are already
// in effect for the whole process.
func setupLogger(conf *config.Config) {
	if conf == nil || !conf.Debug {
		return
	}

	loggerOnce.Do(func() {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
		log.Warn().Msg("Debug mode is enabled")
	})
}

// New creates and initializes an App instance according to the provided options.
func New(opts ...Option) (*App, error) {
	options := &Options{}
	for _, opt := range opts {
		if opt != nil {
			opt(options)
		}
	}

	diagnostics := api.NewSystemDiagnostics()

	// 1. Resolve configuration
	conf := options.Config
	treeMgr := options.TreeManager
	etcdClient := options.EtcdClient
	var agentStore *config.AgentStore
	var proxyHolder atomic.Pointer[proxy.ProxyManager]
	var hubHolder atomic.Pointer[runnerhub.Hub]
	var resolvedConfigPath string

	if conf != nil {
		// Direct injection (e.g. unit tests)
		resolvedConfigPath = options.ConfigPath
		if resolvedConfigPath == "" {
			resolvedConfigPath = paths.ConfigFile()
		}
		if conf.ConfigPath == "" {
			conf.ConfigPath = resolvedConfigPath
		}
	} else {
		if treeMgr == nil {
			var mgrErr error
			treeMgr, etcdClient, mgrErr = config.NewEtcdConfigManager(context.Background())
			if mgrErr != nil {
				return nil, fmt.Errorf("failed to initialize etcd configuration: %w", mgrErr)
			}
		}

		// If treeMgr has not yet been started, register OnError and OnChange before Start
		if treeMgr.Get() == nil {
			treeMgr.OnError(func(err error) {
				log.Error().Err(err).Msg("etcd configuration manager reload/fetch error")
				if diagnostics != nil {
					diagnostics.AddError("config", err.Error())
				}
			})
			treeMgr.OnChange(func(oldTree, newTree *config.AsgardTreeConfig) {
				if newTree == nil {
					return
				}
				log.Info().Msg("etcd configuration update detected, reloading proxy and main config")
				newProxy := newTree.Asgard.Proxy
				conf.SetProxy(&newProxy)
				reloadRunnerConfig(conf, newTree.Asgard.Config.Runner, diagnostics, hubHolder.Load())
				if pm := proxyHolder.Load(); pm != nil {
					if err := pm.Reload(&newProxy); err != nil {
						log.Error().Err(err).Msg("failed to live-reload proxy configuration from etcd")
						if diagnostics != nil {
							diagnostics.AddError("proxy", err.Error())
						}
					} else {
						log.Info().Msg("proxy configuration live-reloaded successfully from etcd")
						if diagnostics != nil {
							diagnostics.ResetSource("proxy")
						}
					}
				}
			})
			if startErr := treeMgr.Start(context.Background()); startErr != nil {
				if etcdClient != nil {
					_ = etcdClient.Close()
				}
				return nil, fmt.Errorf("failed to start etcd config manager: %w", startErr)
			}
		}

		treeCfg := treeMgr.Get()
		if treeCfg == nil {
			return nil, fmt.Errorf("etcd configuration snapshot is nil")
		}
		mainCfg := treeCfg.Asgard.Config.Clone()
		conf = mainCfg
		if treeCfg.Asgard.Proxy.Enable || len(treeCfg.Asgard.Proxy.Rules) > 0 {
			proxyCfg := treeCfg.Asgard.Proxy
			conf.SetProxy(&proxyCfg)
		}
		if err := conf.ApplyDefaultsAndValidate(); err != nil {
			return nil, fmt.Errorf("invalid configuration from etcd: %w", err)
		}
		if etcdClient != nil {
			agentStore = config.NewEtcdAgentStore(etcdClient, config.EtcdPrefix())
			if err := agentStore.Start(context.Background()); err != nil {
				_ = etcdClient.Close()
				return nil, fmt.Errorf("failed to start agent definition store: %w", err)
			}
		}
	}
	configLoaded := true

	// 2. Validate CLI agent setups (unless skipped or config failed to load)
	if !options.SkipAgentValidation && configLoaded {
		for _, provider := range conf.GetProviders() {
			switch provider {
			case "agy":
				if err := agentwrapper.ValidateAgySetup(); err != nil {
					log.Warn().Err(err).Msg("agy agent setup validation failed")
					diagnostics.AddError("cli_auth", err.Error())
				}
			case "claude":
				if err := agentwrapper.ValidateClaudeSetup(); err != nil {
					log.Warn().Err(err).Msg("claude agent setup validation failed")
					diagnostics.AddError("cli_auth", err.Error())
				}
			case "opencode":
				if err := agentwrapper.ValidateOpencodeSetup(); err != nil {
					log.Warn().Err(err).Msg("opencode agent setup validation failed")
					diagnostics.AddError("cli_auth", err.Error())
				}
			case "simplest":
				if err := agentwrapper.ValidateSimplestSetup(); err != nil {
					log.Warn().Err(err).Msg("simplest agent setup validation failed")
					diagnostics.AddError("cli_auth", err.Error())
				}
			}
		}
	}

	// 3. Configure Logger
	setupLogger(conf)

	// 4. Initialize SSH Agent (unless skipped)
	if !options.SkipSSHSetup {
		if err := sshagent.SetupSSHAgent(); err != nil {
			log.Warn().Err(err).Msg("failed to setup SSH agent")
			diagnostics.AddError("ssh", err.Error())
		}
	}

	// 5. Connect and migrate database
	database := options.DB
	if database == nil {
		var err error
		database, err = db.NewDB(conf)
		if err != nil {
			log.Warn().Err(err).Msg("failed to connect to database, entering degraded mode")
			diagnostics.AddError("db", err.Error())
			database = nil
		}
	}

	if database != nil {
		if err := dbmodels.AutoMigrate(database); err != nil {
			log.Warn().Err(err).Msg("failed to migrate database, entering degraded mode")
			diagnostics.AddError("db", err.Error())
			database = nil
		}
	}

	// 6. Initialize cleanup scheduler
	var scheduler *cleanup.Scheduler
	if database != nil {
		repo := dbmodels.NewSessionRepository(database)
		var err error
		scheduler, err = cleanup.NewScheduler(repo)
		if err != nil {
			log.Warn().Err(err).Msg("failed to initialize cleanup scheduler")
		}
	}

	// 7. Initialize Proxy Manager if enabled (R7 degrade philosophy)
	var proxyMgr *proxy.ProxyManager
	if conf != nil && conf.IsProxyEnabled() {
		resolvedProxyConfigPath := conf.ResolvedProxyConfigPath()
		var pErr error
		proxyMgr, pErr = proxy.NewManager(conf.GetProxy(), resolvedProxyConfigPath)
		if pErr != nil {
			log.Warn().Err(pErr).Msg("failed to initialize proxy manager, entering degraded mode")
			diagnostics.AddError("proxy", pErr.Error())
			proxyMgr = nil
		}
	}
	if proxyMgr != nil {
		proxyHolder.Store(proxyMgr)
	}

	// 7b. Start the runner hub when the runner section is configured.
	var hub *runnerhub.Hub
	if rc := conf.GetRunner(); rc != nil {
		hubOpts := runnerhub.Options{
			TLSCertFile:  rc.TLSCertFile,
			TLSKeyFile:   rc.TLSKeyFile,
			ClientCAFile: rc.ClientCAFile,
			Addr:         fmt.Sprintf(":%d", rc.RegistryPort),
			AllowedNodes: conf.AllowedRunnerNodes,
		}
		if database != nil {
			hubOpts.Store = dbmodels.NewFinalizedRunRepository(database)
		}
		if len(rc.AllowedNodes) == 0 {
			log.Warn().Msg("runner.allowed_nodes is empty: no runner may connect")
		}
		hub = runnerhub.New(hubOpts)
		hubHolder.Store(hub)
		if err := hub.Start(); err != nil {
			log.Warn().Err(err).Msg("failed to start runner hub, entering degraded mode")
			diagnostics.AddError("runner", err.Error())
		}
	}

	// 8. Assemble API Server with options
	apiOpts := []api.ServerOption{
		api.WithDiagnostics(diagnostics),
		api.WithConfigPath(resolvedConfigPath),
		api.WithProxyManager(proxyMgr),
	}
	if hub != nil {
		apiOpts = append(apiOpts, api.WithRunnerRegistry(hub))
	}
	if options.FunctionRegistry != nil {
		apiOpts = append(apiOpts, api.WithFunctionRegistry(options.FunctionRegistry))
	}
	for name, fn := range options.Functions {
		apiOpts = append(apiOpts, api.WithFunction(name, fn))
	}
	if len(options.CustomRunners) > 0 {
		apiOpts = append(apiOpts, api.WithCustomRunners(options.CustomRunners...))
	}

	if agentStore != nil {
		apiOpts = append(apiOpts, api.WithAgentSource(agentStore))
	}

	srv, err := api.New(conf, database, apiOpts...)
	if err != nil {
		if hub != nil {
			hub.Stop()
		}
		if agentStore != nil {
			agentStore.Close()
		}
		if proxyMgr != nil {
			_ = proxyMgr.Shutdown(context.Background())
		}
		if scheduler != nil {
			_ = scheduler.Shutdown()
		}
		if etcdClient != nil {
			_ = etcdClient.Close()
		}
		return nil, fmt.Errorf("failed to initialize API server: %w", err)
	}

	if agentStore != nil {
		syncCollisions := func() {
			diagnostics.ResetSource("agent_collision")
			for _, cerr := range agentStore.Collisions() {
				log.Error().Err(cerr).Msg("agent name collision between global and runner agents")
				diagnostics.AddError("agent_collision", cerr.Error())
			}
			diagnostics.ResetSource("runner_agent_error")
			for _, rerr := range agentStore.RunnerErrors() {
				log.Error().Err(rerr).Msg("invalid runner agent definition")
				diagnostics.AddError("runner_agent_error", rerr.Error())
			}
		}
		agentStore.OnChange(func() {
			log.Info().Msg("etcd agent definitions changed, reloading agents")
			_ = srv.ReloadAgents()
			syncCollisions()
		})
		// Changes between api.New's initial load and the registration above
		// would otherwise be missed.
		_ = srv.ReloadAgents()
		syncCollisions()
	}

	return &App{
		agentStore:   agentStore,
		conf:         conf,
		treeManager:  treeMgr,
		etcdClient:   etcdClient,
		db:           database,
		server:       srv,
		scheduler:    scheduler,
		proxyManager: proxyMgr,
		runnerHub:    hub,
	}, nil
}

// reloadRunnerConfig applies a reloaded runner section to conf so the hub sees
// the new allowed_nodes. An invalid section is reported and the previous one
// kept; TLS material and the listen port are only read at startup.
func reloadRunnerConfig(conf *config.Config, newRunner *config.RunnerConfig, diagnostics *api.SystemDiagnostics, hub *runnerhub.Hub) {
	if conf == nil || conf.GetRunner() == nil {
		return
	}
	if newRunner == nil {
		// Removing the section must not fail open: keep the hub but allow
		// nobody, dropping every connected runner.
		cur := conf.GetRunner()
		cur.AllowedNodes = nil
		conf.SetRunner(cur)
		if hub != nil {
			hub.RefreshAllowed()
		}
		log.Warn().Msg("runner section removed on reload: all runners disconnected, restart required to fully disable")
		if diagnostics != nil {
			diagnostics.AddError("runner_config", "runner section removed; no runner is allowed, restart required to fully disable the listener")
		}
		return
	}
	rc := *newRunner
	if err := rc.ApplyDefaultsAndValidate(); err != nil {
		log.Error().Err(err).Msg("invalid runner configuration on reload, keeping previous")
		if diagnostics != nil {
			diagnostics.AddError("runner_config", err.Error())
		}
		return
	}
	conf.SetRunner(&rc)
	if hub != nil {
		hub.RefreshAllowed()
	}
	if diagnostics != nil {
		diagnostics.ResetSource("runner_config")
	}
}

// RunnerHub returns the runner hub of the App (nil when not configured).
func (a *App) RunnerHub() *runnerhub.Hub {
	if a == nil {
		return nil
	}
	return a.runnerHub
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

// Server returns the underlying API server of the App.
func (a *App) Server() *api.Server {
	if a == nil {
		return nil
	}
	return a.server
}

// ProxyManager returns the underlying proxy manager of the App.
func (a *App) ProxyManager() *proxy.ProxyManager {
	if a == nil {
		return nil
	}
	return a.proxyManager
}

// Start starts the underlying HTTP server and proxy server, and blocks until exit.
func (a *App) Start() error {
	if a == nil || a.server == nil {
		return errors.New("app server is not initialized")
	}

	if a.proxyManager != nil {
		go func() {
			if err := a.proxyManager.Start(); err != nil {
				log.Warn().Err(err).Msg("proxy manager server failed to start, entering degraded mode")
				if a.server != nil && a.server.Diagnostics() != nil {
					a.server.Diagnostics().AddError("proxy", err.Error())
				}
			}
		}()
	}

	return a.server.Start()
}

// Stop performs idempotent graceful shutdown in strict order: proxyManager -> server -> scheduler.
func (a *App) Stop(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.stopOnce.Do(func() {
		var errs []error
		if a.proxyManager != nil {
			if err := a.proxyManager.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("proxy manager shutdown failed: %w", err))
			}
		}
		if a.runnerHub != nil {
			a.runnerHub.Stop()
		}
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
		if a.agentStore != nil {
			a.agentStore.Close()
		}
		if a.etcdClient != nil {
			_ = a.etcdClient.Close()
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
