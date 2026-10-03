// Package auth implements an optional, stateless OIDC/OAuth 2.0 authentication
// layer for Asgard.
//
// The design is deliberately stateless: the backend keeps no user table, no
// session table and no token database. Browser-held tokens live in
// localStorage and are verified against the provider's JWKS on every request;
// the only server-side state is a short-lived in-memory map of CSRF state
// values. A nil *Service disables authentication entirely and every method is
// nil-safe, so callers can always invoke them.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"
)

const (
	// stateLifetime bounds how long a login may take between /auth/login and
	// /auth/callback.
	stateLifetime = 10 * time.Minute
	// maxPendingStates caps the in-memory state map so automated login attempts
	// cannot grow it without bound.
	maxPendingStates = 1000
	// maxRedirectLength bounds the attacker-supplied post-login target. The
	// count cap alone would not bound memory: a single state could otherwise
	// carry a redirect as large as the request URL allows.
	maxRedirectLength = 2048
	// discoveryRetryInterval throttles OIDC discovery retries while the
	// provider is down, so an outage does not turn every request into a
	// discovery call.
	discoveryRetryInterval = 30 * time.Second
	// stateCookieName binds the pending state to the browser that started the
	// login (login CSRF protection).
	stateCookieName = "asgard_auth_state"
	// scopes requested at login. offline_access is needed to obtain a refresh
	// token from most providers; providers that require different parameters
	// (e.g. Google's access_type=offline) are covered by AuthCodeOptions.
	scopes = "openid profile email offline_access"

	// probeTimeout bounds a single provider reachability probe.
	probeTimeout = 5 * time.Second
	// probeBodyLimit caps how much of the JWKS response the probe reads.
	probeBodyLimit = 1 << 20
	// probeSuccessTTL bounds how often a successful probe may be repeated. It
	// caps probe load under a flood of bogus tokens while keeping the "provider
	// just went down" misjudgement window at ~1s.
	probeSuccessTTL = 1 * time.Second
	// probeFailureTTL caches an unreachable provider so an outage costs at most
	// one probe per interval instead of one per rejected request.
	probeFailureTTL = 30 * time.Second
)

// Service provides the optional OAuth/OIDC login flow and bearer-token
// verification. A nil *Service disables auth.
type Service struct {
	cfg     *Config
	baseURL string
	// httpClient is used for discovery, token exchange and probes so every
	// provider interaction shares one timeout.
	httpClient *http.Client
	// cookieSecure mirrors whether baseURL is served over HTTPS.
	cookieSecure bool

	// provider is discovered lazily so the app still starts when the provider
	// is temporarily unreachable.
	providerMu    sync.Mutex
	provider      *provider
	providerErr   error
	providerErrAt time.Time

	// probe caches the outcome of provider reachability probes (see
	// providerReachable).
	probeMu    sync.Mutex
	probeErr   error
	probeAt    time.Time
	probeGroup singleflight.Group

	statesMu sync.Mutex
	states   map[string]stateEntry
}

// provider bundles what OIDC discovery and the oauth2 config yield.
type provider struct {
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
	jwksURL  string
}

// errProbeCached marks a probe result that was served from cache rather than
// re-checked; only its nil-ness matters.
var errProbeCached = errors.New("auth: provider reachability served from cache")

type stateEntry struct {
	redirect string
	exp      time.Time
}

// tokenStatus is the outcome of verifying a bearer token.
type tokenStatus int

const (
	// tokenValid: the token is authentic and satisfies the role requirement.
	tokenValid tokenStatus = iota
	// tokenInvalid: the token is missing, malformed or expired. The client
	// recovers by logging in again.
	tokenInvalid
	// tokenForbidden: the token is authentic but does not carry the required
	// role. Logging in again cannot help, so the client must not be pushed back
	// through the login flow.
	tokenForbidden
)

// New creates the auth service. Discovery is attempted eagerly so obvious
// misconfiguration surfaces at startup, but a provider that is unreachable only
// logs a warning; the service retries on first use. baseURL is the externally
// reachable base URL used to build the OAuth redirect URI. The returned service
// is nil when cfg is nil.
func New(ctx context.Context, cfg *Config, baseURL string) (*Service, error) {
	if cfg == nil {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("auth base URL must be an absolute URL like https://host, got %q", baseURL)
	}

	s := &Service{
		cfg:          cfg,
		baseURL:      strings.TrimRight(baseURL, "/"),
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		cookieSecure: base.Scheme == "https",
		states:       map[string]stateEntry{},
	}

	if _, err := s.ensureProvider(ctx); err != nil {
		log.Warn().Err(err).Str("issuer", cfg.Issuer).
			Msg("auth: provider discovery failed at startup; will retry on first use")
	} else {
		log.Info().Str("issuer", cfg.Issuer).
			Str("token_source", cfg.TokenSourceOrDefault()).
			Msg("auth: enabled")
	}
	return s, nil
}

// Enabled reports whether the service is active. A nil service is disabled.
func (s *Service) Enabled() bool { return s != nil }

// ensureProvider discovers the OIDC provider once and caches the result. While
// the provider is unreachable the failure is cached for
// discoveryRetryInterval so an outage does not cause a discovery storm.
func (s *Service) ensureProvider(ctx context.Context) (*provider, error) {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()

	if s.provider != nil {
		return s.provider, nil
	}
	if s.providerErr != nil && time.Since(s.providerErrAt) < discoveryRetryInterval {
		return nil, s.providerErr
	}

	discoveryCtx := oidc.ClientContext(ctx, s.httpClient)
	p, err := oidc.NewProvider(discoveryCtx, s.cfg.Issuer)
	if err != nil {
		s.providerErr = fmt.Errorf("discover provider: %w", err)
		s.providerErrAt = time.Now()
		return nil, s.providerErr
	}

	var discovery struct {
		JWKSURL string `json:"jwks_uri"`
	}
	if err := p.Claims(&discovery); err != nil {
		s.providerErr = fmt.Errorf("read discovery document: %w", err)
		s.providerErrAt = time.Now()
		return nil, s.providerErr
	}

	// The expected audience depends on which credential is verified. For an ID
	// token the spec mandates aud=client_id; an access token may carry a
	// different audience (e.g. Keycloak's "account"), so it is configurable.
	expectedAudience := s.cfg.ClientID
	if s.cfg.TokenSourceOrDefault() == TokenSourceAccessToken {
		expectedAudience = s.cfg.Audience()
	}

	// VerifierContext pins the JWKS fetches to our httpClient (and thus its
	// timeout) so a hung provider surfaces as a verification error instead of
	// blocking indefinitely on the default client.
	verifierCtx := oidc.ClientContext(context.Background(), s.httpClient)

	s.provider = &provider{
		oauth: &oauth2.Config{
			ClientID:     s.cfg.ClientID,
			ClientSecret: s.cfg.ClientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  s.baseURL + "/auth/callback",
			Scopes:       strings.Fields(scopes),
		},
		verifier: p.VerifierContext(verifierCtx, &oidc.Config{ClientID: expectedAudience}),
		jwksURL:  discovery.JWKSURL,
	}
	s.providerErr = nil
	return s.provider, nil
}

// RegisterRoutes registers the auth endpoints on mux. It is a no-op when auth
// is disabled.
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	if s == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /auth/login", s.handleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("GET /auth/callback.js", s.handleCallbackScript)
	mux.HandleFunc("GET /auth/denied", s.handleDenied)
	mux.HandleFunc("GET /auth/denied.js", s.handleDeniedScript)
	mux.HandleFunc("POST /auth/refresh", s.handleRefresh)
}

// Authenticate verifies the request's bearer token, writing the appropriate
// error response and returning false when the request must be rejected. It
// returns true only when the request may proceed.
//
// Status mapping: a missing or invalid token is 401 (the client recovers by
// logging in again), a valid token without the required role is 403 (logging in
// again cannot help) and an unreachable provider is 503 (the client should keep
// its session and retry).
func (s *Service) Authenticate(w http.ResponseWriter, r *http.Request) bool {
	if s == nil {
		return true
	}

	token := bearerToken(r)
	if token == "" {
		token = queryToken(r)
	}
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized, "missing bearer token")
		return false
	}

	status, err := s.verifyToken(r.Context(), token)
	if err != nil {
		log.Warn().Err(err).Msg("auth: token verification unavailable")
		writeJSONError(w, http.StatusServiceUnavailable, "auth provider unavailable")
		return false
	}
	switch status {
	case tokenValid:
		return true
	case tokenForbidden:
		writeJSONError(w, http.StatusForbidden, "insufficient role")
		return false
	default:
		writeJSONError(w, http.StatusUnauthorized, "invalid or expired token")
		return false
	}
}

// verifyToken validates raw and maps the outcome onto a tokenStatus. A non-nil
// error means the provider could not be reached, which callers translate to 503
// so clients keep their session instead of being logged out.
func (s *Service) verifyToken(ctx context.Context, raw string) (tokenStatus, error) {
	p, err := s.ensureProvider(ctx)
	if err != nil {
		return tokenInvalid, err
	}

	idToken, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		var expired *oidc.TokenExpiredError
		if errors.As(err, &expired) {
			// Deterministic: the token is genuinely expired, no probe needed.
			return tokenInvalid, nil
		}
		// Ambiguous. A signature failure may mean the token is bogus, or that
		// the JWKS could not be fetched because the provider is down. go-oidc
		// wraps JWKS fetch failures so that errors.As/Is cannot distinguish
		// them (the inner %w is dropped by an outer %v), so the provider is
		// probed directly. Reachable => bad token (401); unreachable => 503.
		if s.providerReachable() {
			return tokenInvalid, nil
		}
		return tokenInvalid, fmt.Errorf("verify token: provider unreachable: %w", err)
	}

	requiredRole := s.cfg.RequiredRoleTrimmed()
	if requiredRole == "" {
		return tokenValid, nil
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		log.Warn().Err(err).Msg("auth: failed to decode token claims")
		return tokenForbidden, nil
	}
	if !containsRole(extractRoles(claims, s.cfg.Claim()), requiredRole) {
		log.Warn().Str("role_claim", s.cfg.Claim()).Str("required_role", requiredRole).
			Msg("auth: token does not carry the required role")
		return tokenForbidden, nil
	}
	return tokenValid, nil
}

// providerReachable reports whether the provider's JWKS endpoint is currently
// serving keys. Results are cached (briefly for success, longer for failure)
// and probes are single-flighted, so an unreachable provider costs at most one
// probe per probeFailureTTL and a flood of bogus tokens cannot amplify into
// probe traffic.
func (s *Service) providerReachable() bool {
	if reachable, ok := s.cachedProbe(); ok {
		return reachable
	}

	_, err, _ := s.probeGroup.Do("provider-probe", func() (any, error) {
		// A concurrent probe may have refreshed the verdict while we queued.
		if reachable, ok := s.cachedProbe(); ok {
			if reachable {
				return nil, nil
			}
			return nil, errProbeCached
		}

		probeErr := s.fetchJWKS()
		s.probeMu.Lock()
		s.probeErr = probeErr
		s.probeAt = time.Now()
		s.probeMu.Unlock()
		return nil, probeErr
	})
	return err == nil
}

// cachedProbe returns the cached reachability verdict when it is still fresh.
func (s *Service) cachedProbe() (reachable bool, ok bool) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()

	if s.probeAt.IsZero() {
		return false, false
	}
	ttl := probeSuccessTTL
	if s.probeErr != nil {
		ttl = probeFailureTTL
	}
	if time.Since(s.probeAt) >= ttl {
		return false, false
	}
	return s.probeErr == nil, true
}

// fetchJWKS performs a single reachability probe against the provider's JWKS
// endpoint. It is detached from any request context so one client disconnecting
// cannot fail the probe for everyone, and it is bounded by probeTimeout.
func (s *Service) fetchJWKS() error {
	p, err := s.ensureProvider(context.Background())
	if err != nil {
		return err
	}
	if p.jwksURL == "" {
		return errors.New("auth: discovery document has no jwks_uri")
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: jwks endpoint returned %s", resp.Status)
	}
	var keys struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, probeBodyLimit)).Decode(&keys); err != nil {
		return fmt.Errorf("auth: decode jwks: %w", err)
	}
	if len(keys.Keys) == 0 {
		return errors.New("auth: jwks endpoint returned no keys")
	}
	return nil
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	p, err := s.ensureProvider(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("auth: provider unavailable at login")
		http.Error(w, "auth provider unavailable", http.StatusServiceUnavailable)
		return
	}

	state, err := randomToken()
	if err != nil {
		log.Error().Err(err).Msg("auth: generate state failed")
		http.Error(w, "login failed", http.StatusInternalServerError)
		return
	}
	s.addState(state, sanitizeRedirect(r.URL.Query().Get("redirect")))

	// Bind the state to this browser: a callback URL captured by an attacker
	// cannot then be replayed against another user's session (login CSRF).
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    state,
		Path:     "/auth",
		MaxAge:   int(stateLifetime.Seconds()),
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cookieSecure,
		HttpOnly: true,
	})

	http.Redirect(w, r, p.oauth.AuthCodeURL(state, s.authCodeOptions()...), http.StatusFound)
}

// authCodeOptions renders the configured provider-specific authorization
// parameters. Keys are sorted so the generated URL is deterministic.
func (s *Service) authCodeOptions() []oauth2.AuthCodeOption {
	if len(s.cfg.AuthCodeOptions) == 0 {
		return nil
	}
	keys := make([]string, 0, len(s.cfg.AuthCodeOptions))
	for k := range s.cfg.AuthCodeOptions {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	opts := make([]oauth2.AuthCodeOption, 0, len(keys))
	for _, k := range keys {
		opts = append(opts, oauth2.SetAuthURLParam(k, s.cfg.AuthCodeOptions[k]))
	}
	return opts
}

func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if errDesc := query.Get("error"); errDesc != "" {
		log.Warn().Str("error", errDesc).Msg("auth: provider returned an error on callback")
		http.Error(w, "auth error: "+errDesc, http.StatusBadGateway)
		return
	}

	state := query.Get("state")
	cookie, err := r.Cookie(stateCookieName)
	if err != nil || cookie.Value == "" ||
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	redirect, ok := s.consumeState(state)
	if !ok {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	// Consume the state cookie so the same callback URL cannot be replayed.
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/auth",
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cookieSecure,
		HttpOnly: true,
	})

	p, err := s.ensureProvider(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("auth: provider unavailable at callback")
		http.Error(w, "auth provider unavailable", http.StatusServiceUnavailable)
		return
	}

	token, err := p.oauth.Exchange(r.Context(), query.Get("code"))
	if err != nil {
		log.Error().Err(err).Msg("auth: code exchange failed")
		http.Error(w, "code exchange failed", http.StatusBadGateway)
		return
	}

	// Refuse the session up front when the credential cannot satisfy the role
	// requirement: handing out tokens anyway would only make every API call fail
	// and bounce the browser through the login flow again.
	if s.cfg.RequiredRoleTrimmed() != "" {
		status, err := s.verifyToken(r.Context(), s.bearerCredential(token))
		if err != nil {
			log.Error().Err(err).Msg("auth: token verification unavailable at callback")
			http.Error(w, "auth provider unavailable", http.StatusServiceUnavailable)
			return
		}
		switch status {
		case tokenValid:
			// ok
		case tokenForbidden:
			log.Warn().Str("required_role", s.cfg.RequiredRoleTrimmed()).
				Msg("auth: login denied, required role missing")
			s.renderDenied(w)
			return
		default:
			// The provider just issued a credential this server cannot verify,
			// which is a configuration problem (wrong audience, wrong token
			// source) rather than a missing role. Fail loudly instead of sending
			// the user through the login flow again.
			log.Error().Str("token_source", s.cfg.TokenSourceOrDefault()).
				Msg("auth: freshly issued token failed verification; check token_source/token_audience")
			http.Error(w, "issued token failed verification", http.StatusBadGateway)
			return
		}
	}

	data, err := json.Marshal(s.newTokenResponse(token))
	if err != nil {
		log.Error().Err(err).Msg("auth: marshal tokens failed")
		http.Error(w, "login failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := callbackTemplate.Execute(w, callbackPage{Redirect: redirect, Tokens: string(data)}); err != nil {
		log.Error().Err(err).Msg("auth: render callback page failed")
	}
}

func (s *Service) handleCallbackScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write([]byte(callbackScriptJS))
}

func (s *Service) handleDenied(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RequiredRoleTrimmed() == "" {
		// Without a role requirement this page is unreachable through normal
		// use, so send the visitor home instead of showing a misleading denial.
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	s.renderDenied(w)
}

func (s *Service) handleDeniedScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write([]byte(deniedScriptJS))
}

// handleRefresh exchanges a refresh token for new tokens, keeping the client
// secret server-side.
func (s *Service) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.RefreshToken == "" {
		writeJSONError(w, http.StatusBadRequest, "refresh_token is required")
		return
	}

	ctx := r.Context()
	p, err := s.ensureProvider(ctx)
	if err != nil {
		log.Error().Err(err).Msg("auth: provider unavailable at refresh")
		writeJSONError(w, http.StatusServiceUnavailable, "auth unavailable")
		return
	}

	token, err := p.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: body.RefreshToken}).Token()
	if err != nil {
		var retrieveErr *oauth2.RetrieveError
		if errors.As(err, &retrieveErr) && retrieveErr.ErrorCode == "invalid_grant" {
			// The grant is gone (expired, revoked or already used); the user
			// must authenticate again.
			log.Debug().Err(err).Msg("auth: refresh token rejected")
			writeJSONError(w, http.StatusUnauthorized, "refresh failed")
			return
		}
		// Provider outage or misconfiguration: keep the client's session and
		// let it retry instead of forcing a re-login.
		log.Warn().Err(err).Msg("auth: token refresh failed")
		writeJSONError(w, http.StatusServiceUnavailable, "auth unavailable")
		return
	}

	if s.cfg.TokenSourceOrDefault() == TokenSourceIDToken {
		if idToken, _ := token.Extra("id_token").(string); idToken == "" {
			// The API credential is the ID token and the provider did not
			// re-issue one, so the session cannot be extended. Reporting this
			// as an auth failure sends the user through the login flow instead
			// of looping on 401s with a stale credential.
			log.Debug().Msg("auth: refresh did not re-issue an id_token; session cannot be extended")
			writeJSONError(w, http.StatusUnauthorized, "session cannot be extended")
			return
		}
	}

	writeJSON(w, http.StatusOK, s.newTokenResponse(token))
}

// renderDenied shows the access-denied page. It is served instead of the SPA so
// an unprivileged session never reaches the app, which would otherwise fail
// every API call.
func (s *Service) renderDenied(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	if err := deniedTemplate.Execute(w, struct{ Role string }{Role: s.cfg.RequiredRoleTrimmed()}); err != nil {
		log.Error().Err(err).Msg("auth: render denied page failed")
	}
}

// bearerCredential returns the token that API requests must present as a
// bearer. It is the provider access token, except in id_token mode where the
// ID token is the credential the API verifies. Callers (the frontend) never
// talk to the provider directly, so normalising the credential here keeps the
// browser's contract uniform: always send "access_token".
func (s *Service) bearerCredential(token *oauth2.Token) string {
	if s.cfg.TokenSourceOrDefault() == TokenSourceIDToken {
		idToken, _ := token.Extra("id_token").(string)
		return idToken
	}
	return token.AccessToken
}

// newTokenResponse maps an oauth2 token onto the wire format handed to the
// browser, converting the absolute expiry into a lifetime in seconds
// (RFC 6749 §5.1).
func (s *Service) newTokenResponse(token *oauth2.Token) tokenResponse {
	var expiresIn int64
	if !token.Expiry.IsZero() {
		if remaining := int64(time.Until(token.Expiry).Seconds()); remaining > 0 {
			expiresIn = remaining
		}
	}
	idToken, _ := token.Extra("id_token").(string)
	return tokenResponse{
		AccessToken:  s.bearerCredential(token),
		RefreshToken: token.RefreshToken,
		IDToken:      idToken,
		ExpiresIn:    expiresIn,
		TokenType:    token.TokenType,
	}
}

func (s *Service) addState(state, redirect string) {
	s.statesMu.Lock()
	defer s.statesMu.Unlock()

	now := time.Now()
	// Opportunistic cleanup of expired states.
	for k, e := range s.states {
		if now.After(e.exp) {
			delete(s.states, k)
		}
	}
	// Guard against unbounded growth from automated login attempts.
	for len(s.states) >= maxPendingStates {
		s.deleteOldestStateLocked()
	}
	s.states[state] = stateEntry{redirect: redirect, exp: now.Add(stateLifetime)}
}

// deleteOldestStateLocked removes the pending state closest to expiry, which is
// the one created first. Evicting by age rather than at map-iteration random
// keeps a freshly created state (a user mid-login) from being dropped instead.
// The caller must hold statesMu.
func (s *Service) deleteOldestStateLocked() {
	var oldestKey string
	var oldestExp time.Time
	found := false
	for k, e := range s.states {
		if !found || e.exp.Before(oldestExp) {
			oldestKey, oldestExp, found = k, e.exp, true
		}
	}
	delete(s.states, oldestKey)
}

// consumeState consumes the state value, returning the stored redirect target
// and whether the state was valid and unexpired.
func (s *Service) consumeState(v string) (string, bool) {
	s.statesMu.Lock()
	defer s.statesMu.Unlock()

	e, ok := s.states[v]
	delete(s.states, v)
	return e.redirect, ok && time.Now().Before(e.exp)
}

// bearerToken extracts a bearer token from the Authorization header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// queryToken extracts a bearer token from the access_token query parameter.
// Browser-native channels (EventSource, WebSocket upgrades, resource loads such
// as <img>/<iframe>/<a href>) cannot set request headers, so they pass the
// token in the URL instead. Only safe, read-only methods are accepted: a query
// parameter leaks into proxy logs, browser history and the Referer header, so
// mutating requests must always authenticate with the header.
func queryToken(r *http.Request) string {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	default:
		return ""
	}
	return r.URL.Query().Get("access_token")
}

// sanitizeRedirect only allows same-origin relative paths of a bounded length,
// which prevents open redirects and keeps the pending-state entry small.
func sanitizeRedirect(v string) string {
	if v == "" || len(v) > maxRedirectLength || v[0] != '/' || (len(v) > 1 && v[1] == '/') {
		return "/"
	}
	return v
}

// randomToken returns a 128-bit random hex string.
func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// extractRoles reads a role list from a claims map at the given dot path. Both
// space/comma separated strings and arrays of strings are accepted because
// providers differ (Keycloak uses an array, Authentik and Authelia a string).
func extractRoles(claims map[string]any, path string) []string {
	v, ok := lookupPath(claims, path)
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		return strings.FieldsFunc(t, func(r rune) bool { return r == ' ' || r == ',' })
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// lookupPath walks a dot-separated path into nested JSON objects.
func lookupPath(claims map[string]any, path string) (any, bool) {
	var cur any = claims
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// containsRole reports whether roles contains required.
func containsRole(roles []string, required string) bool {
	for _, r := range roles {
		if r == required {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

//go:embed callback.js
var callbackScriptJS string

//go:embed denied.js
var deniedScriptJS string

var callbackTemplate = template.Must(template.New("callback").Parse(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>Logging in…</title></head>
<body>
<p>Logging in…</p>
<div id="tokens" data-redirect="{{.Redirect}}" data-tokens="{{.Tokens}}"></div>
<script src="/auth/callback.js"></script>
</body>
</html>`))

var deniedTemplate = template.Must(template.New("denied").Parse(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>Access denied</title></head>
<body>
<h1>Access denied</h1>
<p>Your account is missing the required role "{{.Role}}".</p>
<p>Ask an administrator to grant it, then log in again.</p>
<script src="/auth/denied.js"></script>
</body>
</html>`))

type callbackPage struct {
	Redirect string
	Tokens   string
}

// tokenResponse is the normalised session credential handed to the browser.
type tokenResponse struct {
	// AccessToken is the credential the browser sends as
	// "Authorization: Bearer". It is the provider access token, or the ID token
	// when TokenSource is id_token (see Service.bearerCredential).
	AccessToken string `json:"access_token"`
	// RefreshToken is exchanged at /auth/refresh for a fresh token pair.
	RefreshToken string `json:"refresh_token"`
	// IDToken is the provider ID token when one was issued.
	IDToken string `json:"id_token"`
	// ExpiresIn is the access token lifetime in seconds (RFC 6749 §5.1).
	ExpiresIn int64  `json:"expires_in"`
	TokenType string `json:"token_type"`
}
