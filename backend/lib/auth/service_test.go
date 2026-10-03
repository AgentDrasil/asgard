package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock OIDC provider
// ---------------------------------------------------------------------------

const testKID = "test-key-1"

type mockProvider struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	mu sync.Mutex
	// tokenResponse is returned by /token; when nil a default token pair is built.
	tokenResponse map[string]any
	tokenStatus   int
	// jwksStatus overrides the /jwks status when non-zero.
	jwksStatus int
	tokenCalls int
	jwksCalls  int
	// lastTokenForm records the decoded form of the most recent /token request.
	lastTokenForm url.Values
}

func newMockProvider(t *testing.T) *mockProvider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	p := &mockProvider{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                p.server.URL,
			"authorization_endpoint":                p.server.URL + "/authorize",
			"token_endpoint":                        p.server.URL + "/token",
			"jwks_uri":                              p.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.jwksCalls++
		status := p.jwksStatus
		p.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksJSON(t, &key.PublicKey))
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		p.mu.Lock()
		p.tokenCalls++
		p.lastTokenForm = r.PostForm
		status := p.tokenStatus
		resp := p.tokenResponse
		p.mu.Unlock()

		if status != 0 {
			writeJSON(w, status, map[string]string{"error": "invalid_grant"})
			return
		}
		if resp != nil {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  p.sign(t, p.claims(t)),
			"id_token":      p.sign(t, p.claims(t)),
			"refresh_token": "refresh-1",
			"token_type":    "Bearer",
			"expires_in":    300,
		})
	})
	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

// claims returns a base claim set valid for p.server as issuer.
func (p *mockProvider) claims(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"iss": p.server.URL,
		"aud": "client-1",
		"sub": "user-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
}

func (p *mockProvider) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	return signJWT(t, p.key, testKID, claims)
}

// setTokenResponse overrides the /token body.
func (p *mockProvider) setTokenResponse(resp map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenResponse = resp
}

// setTokenStatus makes /token fail with the given status.
func (p *mockProvider) setTokenStatus(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenStatus = status
}

// setJWKSStatus makes /jwks fail with the given status, simulating an outage.
func (p *mockProvider) setJWKSStatus(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jwksStatus = status
}

func (p *mockProvider) tokenCallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tokenCalls
}

func (p *mockProvider) jwksCallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jwksCalls
}

func signJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()

	header, err := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid})
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func jwksJSON(t *testing.T, pub *rsa.PublicKey) []byte {
	t.Helper()

	doc := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"kid": testKID,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	return b
}

// newTestService builds a Service pointed at the mock provider.
func newTestService(t *testing.T, p *mockProvider, mutate func(*Config)) *Service {
	t.Helper()

	cfg := &Config{
		Issuer:       p.server.URL,
		ClientID:     "client-1",
		ClientSecret: "secret-1",
	}
	if mutate != nil {
		mutate(cfg)
	}
	s, err := New(context.Background(), cfg, p.server.URL)
	require.NoError(t, err)
	require.NotNil(t, s)
	return s
}

func doRequest(s *Service, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	if !s.Authenticate(rec, req) {
		return rec
	}
	rec.WriteHeader(http.StatusOK)
	return rec
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	valid := func() *Config {
		return &Config{
			Issuer:       "https://idp.example.com",
			ClientID:     "cid",
			ClientSecret: "secret",
		}
	}

	tests := []struct {
		name        string
		mutate      func(*Config)
		wantErr     string
		skipNilCase bool
	}{
		{name: "valid", mutate: func(c *Config) {}},
		{name: "valid id_token source", mutate: func(c *Config) { c.TokenSource = TokenSourceIDToken }},
		{name: "valid access_token source", mutate: func(c *Config) { c.TokenSource = TokenSourceAccessToken }},
		{name: "missing issuer", mutate: func(c *Config) { c.Issuer = "" }, wantErr: "auth issuer is required"},
		{name: "relative issuer", mutate: func(c *Config) { c.Issuer = "/oidc" }, wantErr: "must be an absolute URL"},
		{name: "issuer without host", mutate: func(c *Config) { c.Issuer = "https://" }, wantErr: "must be an absolute URL"},
		{name: "missing client id", mutate: func(c *Config) { c.ClientID = "" }, wantErr: "auth client_id is required"},
		{name: "missing client secret", mutate: func(c *Config) { c.ClientSecret = "" }, wantErr: "auth client_secret is required"},
		{name: "invalid token source", mutate: func(c *Config) { c.TokenSource = "cookie" }, wantErr: "auth token_source must be"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := valid()
			tc.mutate(cfg)
			err := cfg.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	t.Run("nil config", func(t *testing.T) {
		t.Parallel()
		var cfg *Config
		require.NoError(t, cfg.Validate())
		require.False(t, cfg.TokenSourceOrDefault() == "")
		assert.Equal(t, TokenSourceAccessToken, cfg.TokenSourceOrDefault())
		assert.Equal(t, DefaultRoleClaim, cfg.Claim())
		assert.Equal(t, "", cfg.Audience())
		assert.Equal(t, "", cfg.RequiredRoleTrimmed())
	})
}

func TestConfig_Helpers(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		ClientID:      "cid",
		TokenSource:   "",
		TokenAudience: "",
		RoleClaim:     "",
		RequiredRole:  "  admin  ",
	}
	assert.Equal(t, TokenSourceAccessToken, cfg.TokenSourceOrDefault())
	assert.Equal(t, "cid", cfg.Audience())
	assert.Equal(t, DefaultRoleClaim, cfg.Claim())
	assert.Equal(t, "admin", cfg.RequiredRoleTrimmed())

	cfg.TokenSource = TokenSourceIDToken
	cfg.TokenAudience = "account"
	cfg.RoleClaim = "realm_access.roles"
	assert.Equal(t, TokenSourceIDToken, cfg.TokenSourceOrDefault())
	assert.Equal(t, "account", cfg.Audience())
	assert.Equal(t, "realm_access.roles", cfg.Claim())
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNew_NilConfigDisablesAuth(t *testing.T) {
	t.Parallel()

	s, err := New(context.Background(), nil, "https://asgard.example.com")
	require.NoError(t, err)
	assert.Nil(t, s)
	assert.False(t, s.Enabled())

	// A nil service authenticates everything.
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	assert.True(t, s.Authenticate(httptest.NewRecorder(), req))

	// RegisterRoutes on a nil service must not panic.
	s.RegisterRoutes(nil)
}

func TestNew_RejectsNonAbsoluteBaseURL(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	_, err := New(context.Background(), &Config{
		Issuer:       p.server.URL,
		ClientID:     "cid",
		ClientSecret: "secret",
	}, "asgard.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute URL")
}

func TestNew_InvalidConfig(t *testing.T) {
	t.Parallel()

	_, err := New(context.Background(), &Config{Issuer: "not-a-url"}, "https://asgard.example.com")
	require.Error(t, err)
}

func TestNew_ProviderDownAtStartupStillStarts(t *testing.T) {
	t.Parallel()

	// Point at a closed server: discovery fails, but construction must succeed
	// so a temporary outage does not prevent the app from starting.
	missing := httptest.NewServer(http.NotFoundHandler())
	issuer := missing.URL
	missing.Close()

	s, err := New(context.Background(), &Config{
		Issuer:       issuer,
		ClientID:     "cid",
		ClientSecret: "secret",
	}, "https://asgard.example.com")
	require.NoError(t, err)
	require.NotNil(t, s)

	// A request cannot be verified while the provider is unreachable.
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	assert.False(t, s.Authenticate(httptest.NewRecorder(), req))
}

// ---------------------------------------------------------------------------
// Middleware / Authenticate
// ---------------------------------------------------------------------------

func TestAuthenticate_Success(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)
	token := p.sign(t, p.claims(t))

	rec := doRequest(s, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + token})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthenticate_MissingToken(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	rec := doRequest(s, http.MethodGet, "/api/agents", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticate_InvalidToken(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)
	// A syntactically valid JWT signed with a different key.
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	forged := signJWT(t, other, testKID, p.claims(t))

	rec := doRequest(s, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + forged})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticate_ExpiredTokenIs401WithoutProbing(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	claims := p.claims(t)
	claims["exp"] = time.Now().Add(-time.Minute).Unix()
	token := p.sign(t, claims)

	rec := doRequest(s, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + token})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticate_WrongAudienceIs401(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	claims := p.claims(t)
	claims["aud"] = "some-other-client"
	token := p.sign(t, claims)

	rec := doRequest(s, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + token})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticate_CustomTokenAudience(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, func(c *Config) {
		c.TokenSource = TokenSourceAccessToken
		c.TokenAudience = "account"
	})

	claims := p.claims(t)
	claims["aud"] = "account"
	token := p.sign(t, claims)

	rec := doRequest(s, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + token})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthenticate_ProviderDownReturns503(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)
	token := p.sign(t, p.claims(t))

	// The provider becomes unreachable after discovery succeeded: the JWKS
	// fetch fails and the probe confirms the outage, so the client must be told
	// to keep its session (503) rather than to log in again (401).
	p.server.Close()

	rec := doRequest(s, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + token})
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// ---------------------------------------------------------------------------
// Role verification
// ---------------------------------------------------------------------------

func TestAuthenticate_RoleClaimShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		claim    string
		claims   map[string]any
		required string
		want     int
	}{
		{
			name:     "space separated string match",
			claim:    "roles",
			claims:   map[string]any{"roles": "viewer asgard-user admin"},
			required: "asgard-user",
			want:     http.StatusOK,
		},
		{
			name:     "comma separated string match",
			claim:    "roles",
			claims:   map[string]any{"roles": "viewer,asgard-user"},
			required: "asgard-user",
			want:     http.StatusOK,
		},
		{
			name:     "array match",
			claim:    "groups",
			claims:   map[string]any{"groups": []any{"viewer", "asgard-user"}},
			required: "asgard-user",
			want:     http.StatusOK,
		},
		{
			name:     "nested path match",
			claim:    "realm_access.roles",
			claims:   map[string]any{"realm_access": map[string]any{"roles": []any{"asgard-user"}}},
			required: "asgard-user",
			want:     http.StatusOK,
		},
		{
			name:     "string without required role is forbidden",
			claim:    "roles",
			claims:   map[string]any{"roles": "viewer"},
			required: "asgard-user",
			want:     http.StatusForbidden,
		},
		{
			name:     "missing claim is forbidden",
			claim:    "roles",
			claims:   map[string]any{},
			required: "asgard-user",
			want:     http.StatusForbidden,
		},
		{
			name:     "unsupported claim type is forbidden",
			claim:    "roles",
			claims:   map[string]any{"roles": map[string]any{"a": "b"}},
			required: "asgard-user",
			want:     http.StatusForbidden,
		},
		{
			name:     "wrong array type is forbidden",
			claim:    "roles",
			claims:   map[string]any{"roles": []any{42}},
			required: "asgard-user",
			want:     http.StatusForbidden,
		},
		{
			name:     "empty required role skips the check",
			claim:    "roles",
			claims:   map[string]any{},
			required: "",
			want:     http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := newMockProvider(t)
			required := tc.required
			claim := tc.claim
			s := newTestService(t, p, func(c *Config) {
				c.RoleClaim = claim
				c.RequiredRole = required
			})

			claims := p.claims(t)
			for k, v := range tc.claims {
				claims[k] = v
			}
			token := p.sign(t, claims)

			rec := doRequest(s, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + token})
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// Token extraction
// ---------------------------------------------------------------------------

func TestQueryToken_OnlyForSafeMethods(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)
	token := p.sign(t, p.claims(t))

	t.Run("GET uses the query token", func(t *testing.T) {
		rec := doRequest(s, http.MethodGet, "/api/sessions/x/events?access_token="+url.QueryEscape(token), nil)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HEAD uses the query token", func(t *testing.T) {
		rec := doRequest(s, http.MethodHead, "/api/sessions/x/events?access_token="+url.QueryEscape(token), nil)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST ignores the query token", func(t *testing.T) {
		rec := doRequest(s, http.MethodPost, "/api/agents/x/message?access_token="+url.QueryEscape(token), nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("POST with header still works", func(t *testing.T) {
		rec := doRequest(s, http.MethodPost, "/api/agents/x/message", map[string]string{"Authorization": "Bearer " + token})
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestBearerToken_Parsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		header string
		want   string
	}{
		{header: "", want: ""},
		{header: "Bearer", want: ""},
		{header: "Bearer ", want: ""},
		{header: "Basic abc", want: ""},
		{header: "Bearer abc", want: "abc"},
		{header: "bearer abc", want: "abc"},
		{header: "BEARER abc", want: "abc"},
		{header: "Bearer  abc ", want: "abc"},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		assert.Equal(t, tc.want, bearerToken(req), "header=%q", tc.header)
	}
}

// ---------------------------------------------------------------------------
// State management
// ---------------------------------------------------------------------------

func TestState_Lifecycle(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	s.addState("abc", "/dashboard")

	redirect, ok := s.consumeState("abc")
	require.True(t, ok)
	assert.Equal(t, "/dashboard", redirect)

	// A state is single-use.
	_, ok = s.consumeState("abc")
	assert.False(t, ok)

	_, ok = s.consumeState("never-issued")
	assert.False(t, ok)
}

func TestState_Expired(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	s.statesMu.Lock()
	s.states["stale"] = stateEntry{redirect: "/x", exp: time.Now().Add(-time.Second)}
	s.statesMu.Unlock()

	_, ok := s.consumeState("stale")
	assert.False(t, ok)
}

func TestState_EvictsOldestWhenFull(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	first := "state-0000"
	s.addState(first, "/first")
	for i := 1; i < maxPendingStates; i++ {
		s.addState(fmt.Sprintf("state-%04d", i), "/other")
	}
	// One more than the cap: the oldest entry must be evicted.
	s.addState("state-overflow", "/last")

	s.statesMu.Lock()
	size := len(s.states)
	s.statesMu.Unlock()
	assert.Equal(t, maxPendingStates, size)

	_, ok := s.consumeState(first)
	assert.False(t, ok, "oldest state should have been evicted")

	_, ok = s.consumeState("state-overflow")
	assert.True(t, ok, "newest state must survive")
}

func TestSanitizeRedirect(t *testing.T) {
	t.Parallel()

	long := "/" + strings.Repeat("a", maxRedirectLength+1)

	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: "/"},
		{in: "/dashboard", want: "/dashboard"},
		{in: "/chat/1?x=1", want: "/chat/1?x=1"},
		{in: "https://evil.example.com", want: "/"},
		{in: "//evil.example.com", want: "/"},
		{in: "dashboard", want: "/"},
		{in: long, want: "/"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, sanitizeRedirect(tc.in))
	}
}

// ---------------------------------------------------------------------------
// Login / callback / refresh
// ---------------------------------------------------------------------------

// beginLogin drives /auth/login and returns the issued state and cookie.
func beginLogin(t *testing.T, s *Service, redirect string) (state string, cookie *http.Cookie) {
	t.Helper()

	target := "/auth/login"
	if redirect != "" {
		target += "?redirect=" + url.QueryEscape(redirect)
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	s.handleLogin(rec, req)

	require.Equal(t, http.StatusFound, rec.Code)
	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	state = location.Query().Get("state")
	require.NotEmpty(t, state)

	for _, c := range rec.Result().Cookies() {
		if c.Name == stateCookieName {
			cookie = c
		}
	}
	require.NotNil(t, cookie, "login must set the state cookie")
	return state, cookie
}

func TestLogin_RedirectsToProvider(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, func(c *Config) {
		c.AuthCodeOptions = map[string]string{"access_type": "offline", "prompt": "consent"}
	})

	state, cookie := beginLogin(t, s, "/dashboard")

	req := httptest.NewRequest(http.MethodGet, "/auth/login?redirect=/dashboard", nil)
	rec := httptest.NewRecorder()
	s.handleLogin(rec, req)

	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, p.server.URL+"/authorize", location.Scheme+"://"+location.Host+location.Path)
	assert.Equal(t, "client-1", location.Query().Get("client_id"))
	assert.Equal(t, "code", location.Query().Get("response_type"))
	assert.Contains(t, location.Query().Get("scope"), "openid")
	assert.Contains(t, location.Query().Get("scope"), "offline_access")
	assert.Equal(t, "offline", location.Query().Get("access_type"))
	assert.Equal(t, "consent", location.Query().Get("prompt"))
	assert.Equal(t, p.server.URL+"/auth/callback", location.Query().Get("redirect_uri"))
	assert.NotEmpty(t, location.Query().Get("state"))
	assert.NotEqual(t, state, location.Query().Get("state"))

	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	assert.True(t, cookie.HttpOnly)
}

func TestCallback_Success(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	state, cookie := beginLogin(t, s, "/dashboard")

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state="+state, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.handleCallback(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	body := rec.Body.String()
	assert.Contains(t, body, `data-redirect="/dashboard"`)
	assert.Contains(t, body, `data-tokens="`)
	assert.Contains(t, body, "refresh-1")
	// The state cookie must be cleared.
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == stateCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	assert.True(t, cleared, "state cookie must be cleared")
}

func TestCallback_StateMismatch(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	_, cookie := beginLogin(t, s, "/")

	tests := []struct {
		name   string
		target string
		cookie *http.Cookie
	}{
		{name: "state does not match cookie", target: "/auth/callback?code=abc&state=wrong", cookie: cookie},
		{name: "missing cookie", target: "/auth/callback?code=abc&state=" + cookie.Value, cookie: nil},
		{name: "state not issued", target: "/auth/callback?code=abc&state=deadbeef", cookie: cookie},
		{name: "missing state", target: "/auth/callback?code=abc", cookie: cookie},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			rec := httptest.NewRecorder()
			s.handleCallback(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestCallback_StateIsSingleUse(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	state, cookie := beginLogin(t, s, "/")

	first := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state="+state, nil)
	first.AddCookie(cookie)
	firstRec := httptest.NewRecorder()
	s.handleCallback(firstRec, first)
	require.Equal(t, http.StatusOK, firstRec.Code)

	second := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state="+state, nil)
	second.AddCookie(cookie)
	secondRec := httptest.NewRecorder()
	s.handleCallback(secondRec, second)
	assert.Equal(t, http.StatusBadRequest, secondRec.Code)
}

func TestCallback_ProviderError(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?error=access_denied", nil)
	rec := httptest.NewRecorder()
	s.handleCallback(rec, req)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestCallback_ExchangeFailure(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	p.setTokenStatus(http.StatusBadRequest)
	s := newTestService(t, p, nil)

	state, cookie := beginLogin(t, s, "/")
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state="+state, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.handleCallback(rec, req)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestCallback_RoleMissingRendersDenied(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, func(c *Config) {
		c.RequiredRole = "asgard-user"
	})

	// The default token response signs p.claims, which carries no roles claim.
	state, cookie := beginLogin(t, s, "/")
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state="+state, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.handleCallback(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Access denied")
	assert.Contains(t, rec.Body.String(), "asgard-user")
}

func TestCallback_RolePresentSucceeds(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	claims := p.claims(t)
	claims["roles"] = "asgard-user"
	tok := p.sign(t, claims)
	p.setTokenResponse(map[string]any{
		"access_token":  tok,
		"id_token":      tok,
		"refresh_token": "refresh-1",
		"token_type":    "Bearer",
		"expires_in":    300,
	})

	s := newTestService(t, p, func(c *Config) {
		c.RequiredRole = "asgard-user"
	})

	state, cookie := beginLogin(t, s, "/")
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state="+state, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.handleCallback(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "data-tokens=")
}

func TestRefresh_Success(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	before := p.tokenCallCount()
	body := strings.NewReader(`{"refresh_token":"refresh-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", body)
	rec := httptest.NewRecorder()
	s.handleRefresh(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, before+1, p.tokenCallCount())
	var resp tokenResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.AccessToken)
	assert.Equal(t, "refresh-1", resp.RefreshToken)
	assert.Positive(t, resp.ExpiresIn)

	p.mu.Lock()
	form := p.lastTokenForm
	p.mu.Unlock()
	assert.Equal(t, "refresh_token", form.Get("grant_type"))
	assert.Equal(t, "refresh-1", form.Get("refresh_token"))
}

func TestRefresh_MissingToken(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	for _, payload := range []string{`{}`, `{"refresh_token":""}`, `not json`} {
		req := httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		s.handleRefresh(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "payload=%q", payload)
	}
}

func TestRefresh_InvalidGrantIs401(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	p.setTokenStatus(http.StatusBadRequest)
	s := newTestService(t, p, nil)

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(`{"refresh_token":"stale"}`))
	rec := httptest.NewRecorder()
	s.handleRefresh(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRefresh_ProviderOutageIs503(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	// A closed server: the token endpoint is unreachable, so the client should
	// keep its session rather than be logged out.
	p.server.Close()

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(`{"refresh_token":"refresh-1"}`))
	rec := httptest.NewRecorder()
	s.handleRefresh(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// ---------------------------------------------------------------------------
// Token source modes
// ---------------------------------------------------------------------------

func TestIDTokenMode_UsesIDTokenAsBearer(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, func(c *Config) {
		c.TokenSource = TokenSourceIDToken
	})

	claims := p.claims(t)
	idToken := p.sign(t, claims)
	// The access token is deliberately not a usable JWT (opaque), as with Google.
	p.setTokenResponse(map[string]any{
		"access_token":  "opaque-access-token",
		"id_token":      idToken,
		"refresh_token": "refresh-1",
		"token_type":    "Bearer",
		"expires_in":    300,
	})

	// The callback must hand the browser an id_token as the bearer credential.
	state, cookie := beginLogin(t, s, "/")
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state="+state, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.handleCallback(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "data-tokens=")

	var resp tokenResponse
	start := strings.Index(rec.Body.String(), "data-tokens=\"") + len("data-tokens=\"")
	end := strings.Index(rec.Body.String()[start:], "\"")
	raw := htmlUnescape(rec.Body.String()[start : start+end])
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))
	assert.Equal(t, idToken, resp.AccessToken)

	// And that id_token must authenticate requests.
	authRec := doRequest(s, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer " + resp.AccessToken})
	assert.Equal(t, http.StatusOK, authRec.Code)

	// The opaque access token must not.
	badRec := doRequest(s, http.MethodGet, "/api/agents", map[string]string{"Authorization": "Bearer opaque-access-token"})
	assert.Equal(t, http.StatusUnauthorized, badRec.Code)
}

func TestIDTokenMode_RefreshWithoutIDTokenIs401(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, func(c *Config) {
		c.TokenSource = TokenSourceIDToken
	})

	// A refresh response that lacks an id_token cannot extend the session.
	p.setTokenResponse(map[string]any{
		"access_token":  "opaque-access-token",
		"refresh_token": "refresh-2",
		"token_type":    "Bearer",
		"expires_in":    300,
	})

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(`{"refresh_token":"refresh-1"}`))
	rec := httptest.NewRecorder()
	s.handleRefresh(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestIDTokenMode_RefreshWithNewIDTokenSucceeds(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, func(c *Config) {
		c.TokenSource = TokenSourceIDToken
	})

	claims := p.claims(t)
	idToken := p.sign(t, claims)
	p.setTokenResponse(map[string]any{
		"access_token":  "opaque-access-token",
		"id_token":      idToken,
		"refresh_token": "refresh-2",
		"token_type":    "Bearer",
		"expires_in":    300,
	})

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(`{"refresh_token":"refresh-1"}`))
	rec := httptest.NewRecorder()
	s.handleRefresh(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp tokenResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, idToken, resp.AccessToken)
}

// ---------------------------------------------------------------------------
// Probe behaviour
// ---------------------------------------------------------------------------

func TestProviderReachable_NegativeCacheBoundsProbes(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)
	base := p.jwksCallCount()

	p.setJWKSStatus(http.StatusInternalServerError)

	// Drive the probe through the public path a few times.
	for i := 0; i < 5; i++ {
		assert.False(t, s.providerReachable())
	}
	// The failure is cached, so an outage costs one JWKS round trip, not one
	// per rejected request.
	assert.Equal(t, base+1, p.jwksCallCount())
}

func TestProviderReachable_RecoversAfterSuccess(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	p.setJWKSStatus(http.StatusInternalServerError)
	assert.False(t, s.providerReachable())

	// The provider recovers; the failure cache must expire.
	s.probeMu.Lock()
	s.probeAt = time.Now().Add(-probeFailureTTL - time.Second)
	s.probeMu.Unlock()

	p.setJWKSStatus(0)
	assert.True(t, s.providerReachable())
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

func TestRegisterRoutes(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	for _, target := range []string{"/auth/login", "/auth/callback.js", "/auth/denied.js", "/auth/denied"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.NotEqual(t, http.StatusNotFound, rec.Code, "route %s should be registered", target)
	}
}

func TestCallbackScriptServed(t *testing.T) {
	t.Parallel()

	p := newMockProvider(t)
	s := newTestService(t, p, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/callback.js", nil)
	rec := httptest.NewRecorder()
	s.handleCallbackScript(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	assert.Contains(t, rec.Body.String(), "asgard_auth")
}

// htmlUnescape undoes the HTML escaping that html/template applies to the
// data-tokens attribute so the embedded JSON can be parsed back.
func htmlUnescape(s string) string {
	r := strings.NewReplacer(
		"&#34;", `"`,
		"&#39;", `'`,
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
	)
	return r.Replace(s)
}
