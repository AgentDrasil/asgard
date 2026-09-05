package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/josexy/mitmproxy-go"
	"github.com/rs/zerolog/log"
)

// ProxyManager manages the MITM proxy lifecycle, rule matching, and hot-reload.
type ProxyManager struct {
	configPath string
	caCertPath string
	caKeyPath  string
	rulesPtr   atomic.Pointer[[]Rule]
	debugPtr   atomic.Pointer[DebugConfig]
	handler    mitmproxy.DynamicMitmProxyHandler
	server     *http.Server
	running    atomic.Bool
	mu         sync.Mutex
}

// extractHosts extracts unique hosts from a slice of rules.
func extractHosts(rules []Rule) []string {
	seen := make(map[string]struct{})
	var hosts []string
	for _, r := range rules {
		h := strings.TrimSpace(r.Host)
		if h == "" {
			continue
		}
		// Strip port if present for inclusion matching
		if hostPart, _, err := net.SplitHostPort(h); err == nil {
			h = hostPart
		}
		if _, ok := seen[h]; !ok {
			seen[h] = struct{}{}
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// NewManager creates a new ProxyManager with the provided configuration.
// It ensures CA root certificate and key are available, extracts hosts from rules,
// applies Fail-Closed defense if hosts are empty, and sets up dynamic mitmproxy handler.
// Optional extraOpts can be passed for test configurations (e.g. WithRootCAs).
func NewManager(cfg *Config, configPath string, extraOpts ...mitmproxy.Option) (*ProxyManager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	caCert := cfg.ResolvedCACertPath()
	caKey := cfg.ResolvedCAKeyPath()

	if err := EnsureCA(caCert, caKey); err != nil {
		return nil, fmt.Errorf("failed to ensure CA: %w", err)
	}

	hosts := extractHosts(cfg.Rules)
	// Fail-closed defense (R2): empty rules or host targets must not decrypt all traffic
	if len(hosts) == 0 {
		return nil, fmt.Errorf("cannot initialize proxy manager: rules list or host targets must not be empty")
	}

	pm := &ProxyManager{
		configPath: configPath,
		caCertPath: caCert,
		caKeyPath:  caKey,
	}

	rulesCopy := make([]Rule, len(cfg.Rules))
	copy(rulesCopy, cfg.Rules)
	pm.rulesPtr.Store(&rulesCopy)

	debugCopy := cfg.Debug
	pm.debugPtr.Store(&debugCopy)

	opts := []mitmproxy.Option{
		mitmproxy.WithCACertPath(caCert),
		mitmproxy.WithCAKeyPath(caKey),
		mitmproxy.WithIncludeHosts(hosts...),
		mitmproxy.WithHTTPInterceptor(pm.Interceptor),
	}
	opts = append(opts, extraOpts...)

	handler, err := mitmproxy.NewDynamicMitmProxyHandler(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic mitmproxy handler: %w", err)
	}
	pm.handler = handler

	addr := cfg.Server.Addr
	if addr == "" {
		addr = "127.0.0.1:8082"
	}

	pm.server = &http.Server{
		Addr:    addr,
		Handler: pm.handler,
	}

	return pm, nil
}

// Start starts the proxy HTTP server.
func (pm *ProxyManager) Start() error {
	pm.mu.Lock()
	if pm.running.Load() {
		pm.mu.Unlock()
		return fmt.Errorf("proxy manager is already running")
	}
	pm.running.Store(true)
	pm.mu.Unlock()

	log.Info().Str("addr", pm.server.Addr).Msg("proxy manager starting HTTP server")
	err := pm.server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		pm.running.Store(false)
		return fmt.Errorf("proxy server ListenAndServe error: %w", err)
	}
	return nil
}

// ServeListener starts the proxy HTTP server with a pre-configured listener.
func (pm *ProxyManager) ServeListener(ln net.Listener) error {
	pm.mu.Lock()
	if pm.running.Load() {
		pm.mu.Unlock()
		return fmt.Errorf("proxy manager is already running")
	}
	pm.running.Store(true)
	pm.mu.Unlock()

	err := pm.server.Serve(ln)
	if err != nil && err != http.ErrServerClosed {
		pm.running.Store(false)
		return fmt.Errorf("proxy server Serve error: %w", err)
	}
	return nil
}

// Shutdown gracefully shuts down the HTTP server and cleans up mitmproxy handler resources.
func (pm *ProxyManager) Shutdown(ctx context.Context) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if !pm.running.Load() {
		if pm.handler != nil {
			pm.handler.Cleanup()
		}
		return nil
	}

	pm.running.Store(false)
	var sErr error
	if pm.server != nil {
		sErr = pm.server.Shutdown(ctx)
	}
	if pm.handler != nil {
		pm.handler.Cleanup()
	}
	return sErr
}

// Reload updates proxy rules and debug settings atomically without stopping the server.
func (pm *ProxyManager) Reload(newCfg *Config) error {
	if newCfg == nil {
		return fmt.Errorf("new config cannot be nil")
	}
	newCfg.ApplyDefaults()
	if err := newCfg.Validate(); err != nil {
		return fmt.Errorf("invalid proxy config: %w", err)
	}

	hosts := extractHosts(newCfg.Rules)
	if len(hosts) == 0 {
		return fmt.Errorf("cannot reload proxy: rules list or host targets must not be empty")
	}

	rulesCopy := make([]Rule, len(newCfg.Rules))
	copy(rulesCopy, newCfg.Rules)
	pm.rulesPtr.Store(&rulesCopy)

	debugCopy := newCfg.Debug
	pm.debugPtr.Store(&debugCopy)

	// Dynamically update mitmproxy include hosts filters
	pm.handler.SetHostFilters(hosts, nil)

	log.Info().Int("rules_count", len(rulesCopy)).Strs("hosts", hosts).Msg("proxy manager reloaded rules")
	return nil
}

// ReloadFromFile re-reads config from configPath and reloads proxy settings.
func (pm *ProxyManager) ReloadFromFile() (*Config, error) {
	if pm.configPath == "" {
		return nil, fmt.Errorf("no config path configured for reload")
	}
	cfg, err := LoadConfigFile(pm.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load proxy config from file: %w", err)
	}
	if err := pm.Reload(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// GetMaskedRules returns a deep copy of the active rules with secret tokens safely masked.
func (pm *ProxyManager) GetMaskedRules() []Rule {
	rules := pm.rulesPtr.Load()
	if rules == nil {
		return nil
	}

	masked := make([]Rule, len(*rules))
	for i, r := range *rules {
		m := r
		m.RealSecret = MaskSecret(r.RealSecret)
		m.DummySecret = MaskSecret(r.DummySecret)
		masked[i] = m
	}
	return masked
}

// Interceptor intercepts HTTP requests, matches rules by Host and PathPrefix,
// performs credential replacement, handles debug logging and body sampling,
// and invokes upstream request delegation.
func (pm *ProxyManager) Interceptor(ctx context.Context, req *http.Request, invoker mitmproxy.HTTPDelegatedInvoker) (*http.Response, error) {
	if req == nil {
		return invoker.Invoke(req)
	}

	rules := pm.rulesPtr.Load()
	var debugCfg DebugConfig
	if d := pm.debugPtr.Load(); d != nil {
		debugCfg = *d
	}

	// Match host (strip port if present)
	reqHost := req.Host
	if reqHost == "" && req.URL != nil {
		reqHost = req.URL.Host
	}
	hostOnly := reqHost
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		hostOnly = h
	}

	reqPath := ""
	if req.URL != nil {
		reqPath = req.URL.Path
	}

	var matchedRule *Rule
	if rules != nil {
		for i := range *rules {
			r := &(*rules)[i]
			rHost := r.Host
			if h, _, err := net.SplitHostPort(rHost); err == nil {
				rHost = h
			}

			if strings.EqualFold(hostOnly, rHost) {
				if r.PathPrefix == "" || strings.HasPrefix(reqPath, r.PathPrefix) {
					matchedRule = r
					break
				}
			}
		}
	}

	if matchedRule != nil {
		prevVal := req.Header.Get(matchedRule.HeaderKey)
		// If dummy_secret is empty OR matches prevVal, substitute with real_secret
		if matchedRule.DummySecret == "" || prevVal == matchedRule.DummySecret {
			req.Header.Set(matchedRule.HeaderKey, matchedRule.RealSecret)
			if debugCfg.Enable {
				log.Debug().
					Str("host", hostOnly).
					Str("path", reqPath).
					Str("header", matchedRule.HeaderKey).
					Str("dummy", MaskSecret(prevVal)).
					Str("real", MaskSecret(matchedRule.RealSecret)).
					Msg("proxy substituted secret header")
			}
		}
	}

	// Dump body on demand (R5): Only touch req.Body if Enable AND DumpBody are true
	if debugCfg.Enable && debugCfg.DumpBody {
		bodySnippet, err := safeDumpAndRestoreBody(req, debugCfg.MaxBodyBytes)
		if err != nil {
			log.Warn().Err(err).Msg("failed to dump request body for proxy debug")
		} else {
			log.Debug().
				Str("host", hostOnly).
				Str("path", reqPath).
				Str("body_snippet", bodySnippet).
				Msg("proxy dumped request body")
		}
	}

	if debugCfg.Enable && debugCfg.DumpHeaders {
		safeHeaders := req.Header.Clone()
		if rules != nil {
			for i := range *rules {
				r := &(*rules)[i]
				if v := safeHeaders.Get(r.HeaderKey); v != "" {
					safeHeaders.Set(r.HeaderKey, MaskSecret(v))
				}
			}
		}
		log.Debug().
			Str("host", hostOnly).
			Str("path", reqPath).
			Interface("headers", safeHeaders).
			Msg("proxy dumped request headers")
	}

	return invoker.Invoke(req)
}
