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

// noHostsSentinel is the MITM include list while no user key is registered.
// An empty include list would make mitmproxy decrypt every host, so a host
// that never resolves keeps the proxy a plain tunnel.
const noHostsSentinel = "asgard-no-hosts.invalid"

// ProxyManager runs the runner's credential-swapping MITM proxy. It starts
// without any rule; every run registers its user's keys (UserSecret), which
// add their hosts to the MITM set for the duration of the run.
type ProxyManager struct {
	caCertPath string
	caKeyPath  string
	handler    mitmproxy.DynamicMitmProxyHandler
	server     *http.Server
	running    atomic.Bool
	stopped    atomic.Bool
	listenAddr atomic.Pointer[string]
	mu         sync.Mutex

	userMu      sync.RWMutex
	userSecrets map[string]UserSecret // keyed by dummy value
}

// UserSecret is a user's real key and where the proxy swaps it in: requests to
// Host (under PathPrefix, if set) carry the per-run dummy in HeaderKey.
type UserSecret struct {
	Env        string
	Real       string
	Host       string
	PathPrefix string
	HeaderKey  string // defaults to Authorization
}

// RegisterUserSecrets gives every secret a fresh random dummy (returned by env
// name, for the sandbox). A request that carries the dummy where the secret's
// rule says gets the real secret substituted. Hosts of the secrets are added
// to the MITM set until release is called. Secrets without a host are skipped.
func (pm *ProxyManager) RegisterUserSecrets(secrets []UserSecret) (dummyByEnv map[string]string, release func(), err error) {
	dummyByEnv = make(map[string]string, len(secrets))
	added := make([]string, 0, len(secrets))
	pm.userMu.Lock()
	if pm.userSecrets == nil {
		pm.userSecrets = make(map[string]UserSecret)
	}
	forgetLocked := func() {
		for _, d := range added {
			delete(pm.userSecrets, d)
		}
	}
	for _, sec := range secrets {
		sec.Host = strings.ToLower(stripPort(strings.TrimSpace(sec.Host)))
		if sec.Host == "" {
			continue
		}
		buf := make([]byte, 24)
		if _, rerr := rand.Read(buf); rerr != nil {
			forgetLocked()
			pm.userMu.Unlock()
			return nil, nil, fmt.Errorf("generating dummy secret: %w", rerr)
		}
		dummy := "asgard-dummy-" + hex.EncodeToString(buf)
		pm.userSecrets[dummy] = sec
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

// mitmHosts is the MITM host set: the hosts of the registered user secrets.
func (pm *ProxyManager) mitmHosts() []string {
	pm.userMu.RLock()
	defer pm.userMu.RUnlock()
	seen := make(map[string]struct{})
	var hosts []string
	for _, us := range pm.userSecrets {
		if _, ok := seen[us.Host]; !ok {
			seen[us.Host] = struct{}{}
			hosts = append(hosts, us.Host)
		}
	}
	if len(hosts) == 0 {
		return []string{noHostsSentinel}
	}
	return hosts
}

// refreshHostFilters re-applies the MITM host filters.
func (pm *ProxyManager) refreshHostFilters() {
	if pm.handler != nil {
		pm.handler.SetHostFilters(pm.mitmHosts(), nil)
	}
}

// swapUserSecret replaces a per-run dummy sitting in the header named by the
// secret's rule (host and path prefix must match). It reports whether
// anything was replaced.
func (pm *ProxyManager) swapUserSecret(req *http.Request, hostOnly, reqPath string) bool {
	pm.userMu.RLock()
	defer pm.userMu.RUnlock()
	swapped := false
	for dummy, us := range pm.userSecrets {
		if !strings.EqualFold(hostOnly, us.Host) || !pathPrefixMatches(reqPath, us.PathPrefix) {
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

// hasBearerPrefix reports whether s has a case-insensitive "Bearer " prefix (ignoring leading/trailing whitespace).
func hasBearerPrefix(s string) bool {
	trimmed := strings.TrimSpace(s)
	return len(trimmed) >= 7 && strings.EqualFold(trimmed[:7], "bearer ")
}

// stripBearerPrefix removes a case-insensitive "Bearer " prefix and trims surrounding whitespace.
func stripBearerPrefix(s string) string {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) >= 7 && strings.EqualFold(trimmed[:7], "bearer ") {
		return strings.TrimSpace(trimmed[7:])
	}
	return trimmed
}

// Addr returns the actually bound listening address of the proxy server
// ("" until it listens).
func (pm *ProxyManager) Addr() string {
	if pm == nil {
		return ""
	}
	if addr := pm.listenAddr.Load(); addr != nil {
		return *addr
	}
	return ""
}

// URL returns the proxy URL for sandbox HTTP(S)_PROXY, or "" until listening.
func (pm *ProxyManager) URL() string {
	if addr := pm.Addr(); addr != "" {
		return "http://" + addr
	}
	return ""
}

// Running reports whether the proxy server is currently listening.
func (pm *ProxyManager) Running() bool {
	return pm != nil && pm.running.Load() && pm.listenAddr.Load() != nil
}

// CACertPath returns the CA certificate path used by the proxy manager.
func (pm *ProxyManager) CACertPath() string {
	if pm == nil {
		return ""
	}
	return pm.caCertPath
}

// CAKeyPath returns the CA private key path used by the proxy manager.
func (pm *ProxyManager) CAKeyPath() string {
	if pm == nil {
		return ""
	}
	return pm.caKeyPath
}

// NewManager creates a ProxyManager that listens on an ephemeral loopback
// port once started. It ensures the CA certificate and key exist at the given
// paths. Optional extraOpts can be passed for test configurations (e.g.
// WithRootCAs).
func NewManager(caCert, caKey string, extraOpts ...mitmproxy.Option) (*ProxyManager, error) {
	if err := EnsureCA(caCert, caKey); err != nil {
		return nil, fmt.Errorf("failed to ensure CA: %w", err)
	}

	pm := &ProxyManager{caCertPath: caCert, caKeyPath: caKey}
	opts := []mitmproxy.Option{
		mitmproxy.WithCACertPath(caCert),
		mitmproxy.WithCAKeyPath(caKey),
		mitmproxy.WithIncludeHosts(noHostsSentinel),
		mitmproxy.WithHTTPInterceptor(pm.Interceptor),
	}
	opts = append(opts, extraOpts...)

	handler, err := mitmproxy.NewDynamicMitmProxyHandler(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic mitmproxy handler: %w", err)
	}
	pm.handler = handler
	pm.server = &http.Server{Addr: "127.0.0.1:0", Handler: handler}
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

// Interceptor is the mitmproxy HTTP interceptor: it swaps the per-run dummy of
// a registered user key for the real key on requests matching the key's rule.
func (pm *ProxyManager) Interceptor(ctx context.Context, req *http.Request, invoker mitmproxy.HTTPDelegatedInvoker) (*http.Response, error) {
	if req == nil {
		return invoker.Invoke(req)
	}
	reqHost := req.Host
	if reqHost == "" && req.URL != nil {
		reqHost = req.URL.Host
	}
	reqPath := ""
	if req.URL != nil {
		reqPath = req.URL.Path
	}
	if pm.swapUserSecret(req, stripPort(reqHost), reqPath) {
		log.Debug().Str("host", stripPort(reqHost)).Str("path", reqPath).Msg("proxy substituted user secret")
	}
	return invoker.Invoke(req)
}

func pathPrefixMatches(reqPath, prefix string) bool {
	if prefix == "" {
		return true
	}
	if reqPath == prefix {
		return true
	}
	return strings.HasPrefix(reqPath, prefix) && reqPath[len(prefix)] == '/'
}
