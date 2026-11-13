package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	configPath   string
	caCertPath   string
	caKeyPath    string
	rulesPtr     atomic.Pointer[[]Rule]
	debugPtr     atomic.Pointer[DebugConfig]
	handler      mitmproxy.DynamicMitmProxyHandler
	server       *http.Server
	running      atomic.Bool
	stopped      atomic.Bool
	listenAddr   atomic.Pointer[string]
	excludeMu    sync.Mutex
	excludeHosts []string
	mu           sync.Mutex

	userMu      sync.RWMutex
	userSecrets map[string]userSecret // keyed by dummy value
}

// UserSecret is a user's real key and where the proxy swaps it in. With an
// empty Host the key is only usable through an admin rule with the same Env.
type UserSecret struct {
	Env        string
	Real       string
	Host       string
	PathPrefix string
	HeaderKey  string
}

// userSecret is a registered per-run dummy for a UserSecret.
type userSecret struct {
	UserSecret
}

// RegisterUserSecrets gives every secret a fresh random dummy (returned by env
// name, for the sandbox). A request that carries the dummy where the secret's
// rule says (or, without a host, matches the admin rule with the same Env)
// gets the real secret substituted. Hosts of the secrets are added to the
// MITM set until release is called.
func (pm *ProxyManager) RegisterUserSecrets(secrets []UserSecret) (dummyByEnv map[string]string, release func(), err error) {
	dummyByEnv = make(map[string]string, len(secrets))
	added := make([]string, 0, len(secrets))
	pm.userMu.Lock()
	if pm.userSecrets == nil {
		pm.userSecrets = make(map[string]userSecret)
	}
	forgetLocked := func() {
		for _, d := range added {
			delete(pm.userSecrets, d)
		}
	}
	for _, sec := range secrets {
		buf := make([]byte, 24)
		if _, rerr := rand.Read(buf); rerr != nil {
			forgetLocked()
			pm.userMu.Unlock()
			return nil, nil, fmt.Errorf("generating dummy secret: %w", rerr)
		}
		dummy := "asgard-dummy-" + hex.EncodeToString(buf)
		sec.Host = strings.ToLower(strings.TrimSpace(sec.Host))
		pm.userSecrets[dummy] = userSecret{sec}
		added = append(added, dummy)
		dummyByEnv[sec.Env] = dummy
	}
	pm.userMu.Unlock()
	pm.refreshHostFilters()
	return dummyByEnv, func() {
		pm.userMu.Lock()
		forgetLocked()
		pm.userMu.Unlock()
		pm.refreshHostFilters()
	}, nil
}

// userHosts returns the hosts of registered user secrets.
func (pm *ProxyManager) userHosts() []string {
	pm.userMu.RLock()
	defer pm.userMu.RUnlock()
	var rules []Rule
	for _, us := range pm.userSecrets {
		if us.Host != "" {
			rules = append(rules, Rule{Host: us.Host})
		}
	}
	return extractHosts(rules)
}

// allHosts is the MITM host set: admin rule hosts plus user secret hosts.
func (pm *ProxyManager) allHosts(rules []Rule) []string {
	return extractHosts(append(append([]Rule(nil), rules...), hostRules(pm.userHosts())...))
}

func hostRules(hosts []string) []Rule {
	rules := make([]Rule, 0, len(hosts))
	for _, h := range hosts {
		rules = append(rules, Rule{Host: h})
	}
	return rules
}

// refreshHostFilters re-applies the MITM host filters to the current rules.
func (pm *ProxyManager) refreshHostFilters() {
	if pm.handler == nil {
		return
	}
	var rules []Rule
	if r := pm.rulesPtr.Load(); r != nil {
		rules = *r
	}
	pm.handler.SetHostFilters(pm.allHosts(rules), pm.getExcludeHosts())
}

// swapHostBoundSecret replaces a per-run dummy sitting in the header named by
// the secret's own rule (host and path prefix must match).
func (pm *ProxyManager) swapHostBoundSecret(req *http.Request, hostOnly, reqPath string) bool {
	pm.userMu.RLock()
	defer pm.userMu.RUnlock()
	swapped := false
	for dummy, us := range pm.userSecrets {
		if us.Host == "" || !strings.EqualFold(hostOnly, stripPort(us.Host)) || !pathPrefixMatches(reqPath, us.PathPrefix) {
			continue
		}
		hk := us.HeaderKey
		if hk == "" {
			hk = "Authorization"
		}
		prev := req.Header.Get(hk)
		if prev == "" || stripBearerPrefix(prev) != dummy {
			continue
		}
		real := us.Real
		if hasBearerPrefix(prev) && !hasBearerPrefix(real) {
			real = "Bearer " + strings.TrimSpace(real)
		}
		req.Header.Set(hk, real)
		swapped = true
	}
	return swapped
}

func stripPort(h string) string {
	if hostPart, _, err := net.SplitHostPort(h); err == nil {
		return hostPart
	}
	return h
}

// swapUserSecret replaces a per-run dummy of env found in any request header
// (Authorization, x-api-key, x-goog-api-key, ...) or query parameter with the
// user's real secret, whatever the SDK chose to send it in. It reports whether
// anything was replaced.
func (pm *ProxyManager) swapUserSecret(req *http.Request, env string) bool {
	swapped := false
	for name, vals := range req.Header {
		for i, v := range vals {
			if real, ok := pm.userSecretFor(env, v); ok {
				if hasBearerPrefix(v) && !hasBearerPrefix(real) {
					real = "Bearer " + strings.TrimSpace(real)
				}
				vals[i] = real
				swapped = true
			}
		}
		req.Header[name] = vals
	}
	if req.URL != nil && req.URL.RawQuery != "" {
		q := req.URL.Query()
		changed := false
		for k, vals := range q {
			for i, v := range vals {
				if real, ok := pm.userSecretFor(env, v); ok {
					vals[i] = real
					changed = true
				}
			}
			q[k] = vals
		}
		if changed {
			req.URL.RawQuery = q.Encode()
			swapped = true
		}
	}
	return swapped
}

// userSecretFor returns the real secret behind a dummy header value for env.
func (pm *ProxyManager) userSecretFor(env, headerVal string) (string, bool) {
	if env == "" {
		return "", false
	}
	pm.userMu.RLock()
	defer pm.userMu.RUnlock()
	if len(pm.userSecrets) == 0 {
		return "", false
	}
	us, ok := pm.userSecrets[stripBearerPrefix(headerVal)]
	if !ok || us.Env != env || us.Host != "" {
		return "", false
	}
	return us.Real, true
}

// Addr returns the actually bound listening address of the proxy server.
// It falls back to the configured address before the listener is established.
func (pm *ProxyManager) Addr() string {
	if pm == nil {
		return ""
	}
	if addr := pm.listenAddr.Load(); addr != nil {
		return *addr
	}
	if pm.server == nil {
		return ""
	}
	return pm.server.Addr
}

// Running reports whether the proxy server is currently listening.
func (pm *ProxyManager) Running() bool {
	return pm != nil && pm.running.Load() && pm.listenAddr.Load() != nil
}

// SetExcludeHosts records exclude-hosts to be preserved across reloads.
func (pm *ProxyManager) SetExcludeHosts(excludes []string) {
	if pm == nil {
		return
	}
	pm.excludeMu.Lock()
	defer pm.excludeMu.Unlock()
	pm.excludeHosts = append([]string(nil), excludes...)
}

func (pm *ProxyManager) getExcludeHosts() []string {
	pm.excludeMu.Lock()
	defer pm.excludeMu.Unlock()
	return append([]string(nil), pm.excludeHosts...)
}

// CACertPath returns the CA certificate path used by the proxy manager.
func (pm *ProxyManager) CACertPath() string {
	if pm == nil {
		return ""
	}
	return pm.caCertPath
}

// ConfigPath returns the standalone configuration path used by the proxy manager.
func (pm *ProxyManager) ConfigPath() string {
	if pm == nil {
		return ""
	}
	return pm.configPath
}

// CAKeyPath returns the CA private key path used by the proxy manager.
func (pm *ProxyManager) CAKeyPath() string {
	if pm == nil {
		return ""
	}
	return pm.caKeyPath
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

	// Validation parity with Reload: programmatic callers must not bypass
	// defaults/validation (e.g. rules with empty HeaderKey/RealSecret).
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		// Keep the canonical fail-closed message for empty rules/hosts
		if len(extractHosts(cfg.Rules)) == 0 {
			return nil, fmt.Errorf("cannot initialize proxy manager: rules list or host targets must not be empty")
		}
		return nil, fmt.Errorf("invalid proxy config: %w", err)
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

// bind opens the listener for a start. running and the bound address are only
// published after the bind succeeded.
func (pm *ProxyManager) bind() (net.Listener, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.stopped.Load() {
		return nil, fmt.Errorf("proxy manager has been stopped and cannot be restarted")
	}
	if pm.running.Load() {
		return nil, fmt.Errorf("proxy manager is already running")
	}
	ln, err := net.Listen("tcp", pm.server.Addr)
	if err != nil {
		return nil, fmt.Errorf("proxy server listen error: %w", err)
	}
	bound := ln.Addr().String()
	pm.listenAddr.Store(&bound)
	pm.running.Store(true)
	return ln, nil
}

// serve runs the HTTP server on ln until it stops, then marks the manager as
// not running.
func (pm *ProxyManager) serve(ln net.Listener) error {
	log.Info().Str("addr", pm.server.Addr).Msg("proxy manager starting HTTP server")
	err := pm.server.Serve(ln)
	pm.running.Store(false)
	pm.listenAddr.Store(nil)
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("proxy server ListenAndServe error: %w", err)
	}
	return nil
}

// Start starts the proxy HTTP server and blocks until it stops.
func (pm *ProxyManager) Start() error {
	ln, err := pm.bind()
	if err != nil {
		return err
	}
	return pm.serve(ln)
}

// StartBackground binds the listener synchronously and serves in the
// background. A bind failure is returned; onExit (may be nil) is called when
// the server later stops, with a non-nil error if it failed.
func (pm *ProxyManager) StartBackground(onExit func(error)) error {
	ln, err := pm.bind()
	if err != nil {
		return err
	}
	go func() {
		err := pm.serve(ln)
		if onExit != nil {
			onExit(err)
		}
	}()
	return nil
}

// ServeListener starts the proxy HTTP server with a pre-configured listener.
func (pm *ProxyManager) ServeListener(ln net.Listener) error {
	pm.mu.Lock()
	if pm.stopped.Load() {
		pm.mu.Unlock()
		return fmt.Errorf("proxy manager has been stopped and cannot be restarted")
	}
	if pm.running.Load() {
		pm.mu.Unlock()
		return fmt.Errorf("proxy manager is already running")
	}
	pm.running.Store(true)
	pm.mu.Unlock()

	bound := ln.Addr().String()
	pm.listenAddr.Store(&bound)

	err := pm.server.Serve(ln)
	pm.running.Store(false)
	pm.listenAddr.Store(nil)
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("proxy server Serve error: %w", err)
	}
	return nil
}

// Shutdown gracefully shuts down the HTTP server and cleans up mitmproxy handler resources.
func (pm *ProxyManager) Shutdown(ctx context.Context) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.stopped.Store(true)
	if !pm.running.Load() {
		if pm.handler != nil {
			pm.handler.Cleanup()
		}
		return nil
	}

	pm.running.Store(false)
	pm.listenAddr.Store(nil)
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

	if len(extractHosts(newCfg.Rules)) == 0 {
		return fmt.Errorf("cannot reload proxy: rules list or host targets must not be empty")
	}
	hosts := pm.allHosts(newCfg.Rules)

	rulesCopy := make([]Rule, len(newCfg.Rules))
	copy(rulesCopy, newCfg.Rules)
	pm.rulesPtr.Store(&rulesCopy)

	debugCopy := newCfg.Debug
	pm.debugPtr.Store(&debugCopy)

	// Dynamically update mitmproxy include hosts filters, preserving tracked exclude-hosts
	pm.handler.SetHostFilters(hosts, pm.getExcludeHosts())

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
				if pathPrefixMatches(reqPath, r.PathPrefix) {
					matchedRule = r
					break
				}
			}
		}
	}

	// A user key bound to this host by its own rule wins over admin rules.
	userSwapped := pm.swapHostBoundSecret(req, hostOnly, reqPath)

	if userSwapped {
		matchedRule = nil
	}

	if matchedRule != nil {
		prevVal := req.Header.Get(matchedRule.HeaderKey)
		shouldReplace := false

		if pm.swapUserSecret(req, strings.TrimSpace(matchedRule.Env)) {
			// A user's per-run dummy was replaced by their real key; the rule's
			// shared secret must not overwrite it.
		} else if matchedRule.DummySecret == "" {
			shouldReplace = true
		} else if prevVal == matchedRule.DummySecret {
			shouldReplace = true
		} else if strings.EqualFold(matchedRule.HeaderKey, "Authorization") || hasBearerPrefix(prevVal) || hasBearerPrefix(matchedRule.DummySecret) {
			// Bearer normalization is intentionally scoped to Authorization headers and Bearer-carrying values;
			// other headers preserve strict exact-match semantics.
			if stripBearerPrefix(prevVal) == stripBearerPrefix(matchedRule.DummySecret) {
				shouldReplace = true
			}
		}

		if shouldReplace {
			newSecret := matchedRule.RealSecret
			if hasBearerPrefix(prevVal) && !hasBearerPrefix(matchedRule.RealSecret) {
				newSecret = "Bearer " + strings.TrimSpace(matchedRule.RealSecret)
			}
			req.Header.Set(matchedRule.HeaderKey, newSecret)
			if debugCfg.Enable {
				log.Debug().
					Str("host", hostOnly).
					Str("path", reqPath).
					Str("header", matchedRule.HeaderKey).
					Str("dummy", MaskSecret(prevVal)).
					Str("real", MaskSecret(newSecret)).
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
		for _, key := range defaultSensitiveHeaderDenylist {
			if v := safeHeaders.Get(key); v != "" {
				safeHeaders.Set(key, MaskSecret(v))
			}
		}
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

// pathPrefixMatches performs boundary-aware path prefix matching: the request
// path must equal the prefix or the next character must be "/" so that
// "/v1/chat" does not match a "/v1/chatfoo"-style prefix overlap.
func pathPrefixMatches(reqPath, prefix string) bool {
	if prefix == "" {
		return true
	}
	if reqPath == prefix {
		return true
	}
	return strings.HasPrefix(reqPath, prefix) && reqPath[len(prefix)] == '/'
}

// defaultSensitiveHeaderDenylist lists headers always masked in header dumps,
// independent of rule-defined header keys.
var defaultSensitiveHeaderDenylist = []string{
	"Authorization",
	"Proxy-Authorization",
	"Cookie",
	"X-API-Key",
}
