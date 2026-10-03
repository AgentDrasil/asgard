// Package authtest provides an in-process OIDC provider for tests.
//
// It serves a discovery document, a JWKS endpoint and a token endpoint, signs
// RS256 tokens with a freshly generated key and can be driven into failure
// modes (unreachable JWKS, failing token exchange) to exercise the auth
// service's error paths. It mirrors the spirit of go-oidc's oidctest package.
package authtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// ClientID is the client id and default audience every test provider uses.
const ClientID = "client-1"

// Kid is the key id advertised by the provider's JWKS.
const Kid = "test-key-1"

// Provider is an in-process OIDC provider.
type Provider struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	mu            sync.Mutex
	tokenResponse map[string]any
	tokenStatus   int
	jwksStatus    int
	tokenCalls    int
	jwksCalls     int
	lastTokenForm url.Values
}

// NewProvider starts a mock OIDC provider. It is closed automatically when the
// test finishes.
func NewProvider(t testing.TB) *Provider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("authtest: generate key: %v", err)
	}
	p := &Provider{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                p.URL(),
			"authorization_endpoint":                p.URL() + "/authorize",
			"token_endpoint":                        p.URL() + "/token",
			"jwks_uri":                              p.URL() + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
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
		signed := p.Sign(t, p.Claims())
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  signed,
			"id_token":      signed,
			"refresh_token": "refresh-1",
			"token_type":    "Bearer",
			"expires_in":    300,
		})
	})

	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

// URL is the provider's base URL, which doubles as its issuer.
func (p *Provider) URL() string { return p.server.URL }

// Close stops the provider, simulating an outage.
func (p *Provider) Close() { p.server.Close() }

// Claims returns a base claim set valid for this provider (issuer, audience,
// subject and a future expiry).
func (p *Provider) Claims() map[string]any {
	return map[string]any{
		"iss": p.URL(),
		"aud": ClientID,
		"sub": "user-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
}

// Sign returns an RS256-signed JWT carrying claims.
func (p *Provider) Sign(t testing.TB, claims map[string]any) string {
	t.Helper()
	return SignWithKey(t, p.key, claims)
}

// SignWithKey signs claims with an arbitrary key, which lets tests forge a
// token the provider's JWKS cannot verify.
func SignWithKey(t testing.TB, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()

	header, err := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": Kid})
	if err != nil {
		t.Fatalf("authtest: marshal header: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("authtest: marshal claims: %v", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("authtest: sign: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// SetTokenResponse overrides the body returned by the token endpoint.
func (p *Provider) SetTokenResponse(resp map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenResponse = resp
}

// SetTokenStatus makes the token endpoint fail with status.
func (p *Provider) SetTokenStatus(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenStatus = status
}

// SetJWKSStatus makes the JWKS endpoint fail with status.
func (p *Provider) SetJWKSStatus(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jwksStatus = status
}

// TokenCalls reports how many token endpoint requests were served.
func (p *Provider) TokenCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tokenCalls
}

// JWKSCalls reports how many JWKS requests were served.
func (p *Provider) JWKSCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jwksCalls
}

// LastTokenForm returns the form of the most recent token request.
func (p *Provider) LastTokenForm() url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastTokenForm
}

func jwksJSON(t testing.TB, pub *rsa.PublicKey) []byte {
	t.Helper()

	doc := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"kid": Kid,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("authtest: marshal jwks: %v", err)
	}
	return b
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
