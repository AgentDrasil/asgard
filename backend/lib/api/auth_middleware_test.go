package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/auth"
	"github.com/AgentDrasil/asgard/backend/lib/auth/authtest"
	"github.com/AgentDrasil/asgard/backend/lib/config"
)

// newAuthServer builds a Server guarded by a real auth.Service backed by the
// mock provider, with the public mux already assembled.
func newAuthServer(t *testing.T, p *authtest.Provider, mutate func(*auth.Config)) *Server {
	t.Helper()

	authCfg := &auth.Config{
		Issuer:       p.URL(),
		ClientID:     authtest.ClientID,
		ClientSecret: "secret-1",
	}
	if mutate != nil {
		mutate(authCfg)
	}
	svc, err := auth.New(context.Background(), authCfg, p.URL())
	require.NoError(t, err)

	conf := &config.Config{Host: p.URL()}
	srv := &Server{conf: conf, auth: svc}
	srv.mux = srv.buildMuxLocked()
	return srv
}

func serve(srv *Server, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// protectedPaths are the routes that must require authentication. /team and
// /api/compression-metrics are listed explicitly because they are not under
// /api/ in the router's mental model even though metrics happens to be.
var protectedPaths = []string{
	"/api/agents",
	"/api/sessions",
	"/api/config",
	"/api/manage/config",
	"/api/compression-metrics",
	"/api/ttyd/sidebar/ws",
	"/api/sessions/abc/events",
	"/team?chat_id=abc",
}

func TestAuthStatusDisabledByDefault(t *testing.T) {
	srv := &Server{conf: &config.Config{}}
	srv.mux = srv.buildMuxLocked()

	rec := serve(srv, http.MethodGet, authStatusPath, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var body authStatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.False(t, body.Enabled)
}

func TestAuthStatusEnabledAndPublic(t *testing.T) {
	p := authtest.NewProvider(t)
	srv := newAuthServer(t, p, nil)

	// No credentials: the probe must still answer, otherwise the frontend can
	// never discover that auth is on.
	rec := serve(srv, http.MethodGet, authStatusPath, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var body authStatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.True(t, body.Enabled)
}

func TestAuthMiddlewareBlocksProtectedPathsWithoutToken(t *testing.T) {
	p := authtest.NewProvider(t)
	srv := newAuthServer(t, p, nil)

	for _, path := range protectedPaths {
		t.Run(path, func(t *testing.T) {
			rec := serve(srv, http.MethodGet, path, nil)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
		})
	}
}

func TestAuthMiddlewareAllowsValidToken(t *testing.T) {
	p := authtest.NewProvider(t)
	srv := newAuthServer(t, p, nil)

	token := p.Sign(t, p.Claims())
	rec := serve(srv, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + token})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddlewareRejectsInvalidToken(t *testing.T) {
	p := authtest.NewProvider(t)
	srv := newAuthServer(t, p, nil)

	rec := serve(srv, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer not-a-token"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthMiddlewareForbiddenWithoutRole(t *testing.T) {
	p := authtest.NewProvider(t)
	srv := newAuthServer(t, p, func(c *auth.Config) { c.RequiredRole = "asgard-user" })

	// Valid token, missing role.
	token := p.Sign(t, p.Claims())
	rec := serve(srv, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + token})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Valid token carrying the role.
	claims := p.Claims()
	claims["roles"] = "asgard-user"
	rec = serve(srv, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + p.Sign(t, claims)})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddlewareUnavailableProviderIs503(t *testing.T) {
	p := authtest.NewProvider(t)
	srv := newAuthServer(t, p, nil)
	token := p.Sign(t, p.Claims())

	p.Close()

	rec := serve(srv, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + token})
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestAuthMiddlewareNeverRejectsPreflight(t *testing.T) {
	p := authtest.NewProvider(t)
	srv := newAuthServer(t, p, nil)

	for _, path := range protectedPaths {
		rec := serve(srv, http.MethodOptions, path, nil)
		assert.Equal(t, http.StatusNoContent, rec.Code, "OPTIONS %s must not be authenticated", path)
	}
}

func TestAuthMiddlewareQueryTokenForReadOnlyChannels(t *testing.T) {
	p := authtest.NewProvider(t)
	srv := newAuthServer(t, p, nil)
	token := p.Sign(t, p.Claims())

	t.Run("GET accepts access_token in the query", func(t *testing.T) {
		rec := serve(srv, http.MethodGet, "/api/agents?access_token="+url.QueryEscape(token), nil)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST ignores access_token in the query", func(t *testing.T) {
		rec := serve(srv, http.MethodPost, "/api/sessions?access_token="+url.QueryEscape(token), nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestAuthMiddlewareLeavesAuthEndpointsPublic(t *testing.T) {
	p := authtest.NewProvider(t)
	srv := newAuthServer(t, p, nil)

	// /auth/login must issue a redirect without credentials.
	rec := serve(srv, http.MethodGet, "/auth/login?redirect=/dashboard", nil)
	assert.Equal(t, http.StatusFound, rec.Code)

	// The static denial helpers are public too.
	for _, path := range []string{"/auth/callback.js", "/auth/denied.js", "/auth/denied"} {
		rec := serve(srv, http.MethodGet, path, nil)
		assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "path %s must be public", path)
	}
}

func TestAuthMiddlewareLeavesStaticAssetsPublic(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>app"), 0o644))

	p := authtest.NewProvider(t)
	srv := newAuthServer(t, p, nil)
	srv.conf.WebUIPath = dir
	srv.mux = srv.buildMuxLocked()

	// The SPA shell must load so the browser can run the login flow itself.
	rec := serve(srv, http.MethodGet, "/dashboard", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "app")
}

func TestAuthMiddlewareDisabledLeavesEverythingOpen(t *testing.T) {
	srv := &Server{conf: &config.Config{}}
	srv.mux = srv.buildMuxLocked()

	for _, path := range protectedPaths {
		rec := serve(srv, http.MethodGet, path, nil)
		assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "path %s must stay open without auth", path)
	}
}

func TestInternalMuxBypassesAuthMiddleware(t *testing.T) {
	p := authtest.NewProvider(t)
	srv := newAuthServer(t, p, nil)

	internal := srv.buildInternalMux()

	// /team is reachable over loopback without credentials: a missing chat_id
	// yields 400, not 401.
	rec := httptest.NewRecorder()
	internal.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/team", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// The agent message route is registered as well.
	rec = httptest.NewRecorder()
	internal.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agents/ghost/message", nil))
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

func TestCORSHeaders(t *testing.T) {
	t.Run("permissive without auth", func(t *testing.T) {
		srv := &Server{conf: &config.Config{}}
		srv.mux = srv.buildMuxLocked()

		rec := serve(srv, http.MethodGet, "/api/agents", map[string]string{"Origin": "https://elsewhere.example.com"})
		assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("restricted and origin-specific with auth", func(t *testing.T) {
		p := authtest.NewProvider(t)
		srv := newAuthServer(t, p, nil)

		req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
		req.Host = "asgard.example.com"
		req.Header.Set("Origin", "https://asgard.example.com")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Equal(t, "https://asgard.example.com", rec.Header().Get("Access-Control-Allow-Origin"))
		assert.Contains(t, rec.Header().Values("Vary"), "Origin")
	})

	t.Run("cross-origin is not reflected with auth", func(t *testing.T) {
		p := authtest.NewProvider(t)
		srv := newAuthServer(t, p, nil)

		req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
		req.Host = "asgard.example.com"
		req.Header.Set("Origin", "https://evil.example.com")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	})
}

func TestWithAuthServiceOption(t *testing.T) {
	p := authtest.NewProvider(t)
	authCfg := &auth.Config{Issuer: p.URL(), ClientID: authtest.ClientID, ClientSecret: "secret-1"}
	svc, err := auth.New(context.Background(), authCfg, p.URL())
	require.NoError(t, err)

	srv := &Server{}
	WithAuthService(svc)(srv)
	require.Same(t, svc, srv.AuthService())
}

func TestSecurityHeaders(t *testing.T) {
	srv := &Server{conf: &config.Config{}}
	srv.mux = srv.buildMuxLocked()

	rec := serve(srv, http.MethodGet, "/api/agents", nil)
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))

	// Assets and API responses are not documents, so they carry no CSP.
	assert.Empty(t, rec.Header().Get("Content-Security-Policy"))
}

func TestDocumentCSP(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>app"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "assets"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644))

	srv := &Server{conf: &config.Config{WebUIPath: dir}}
	srv.mux = srv.buildMuxLocked()

	t.Run("SPA fallback serves the shell with the policy", func(t *testing.T) {
		rec := serve(srv, http.MethodGet, "/dashboard", nil)
		require.Equal(t, http.StatusOK, rec.Code)

		csp := rec.Header().Get("Content-Security-Policy")
		require.NotEmpty(t, csp)
		// Inline scripts stay forbidden; they are the whole point of the policy.
		assert.Contains(t, csp, "script-src 'self'")
		assert.NotContains(t, csp, "script-src 'self' 'unsafe-inline'")
		assert.Contains(t, csp, "frame-ancestors 'none'")
		assert.Contains(t, csp, "object-src 'none'")
		assert.Contains(t, csp, "base-uri 'self'")
		assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	})

	t.Run("static assets get no policy", func(t *testing.T) {
		rec := serve(srv, http.MethodGet, "/assets/app.js", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Header().Get("Content-Security-Policy"))
	})
}
