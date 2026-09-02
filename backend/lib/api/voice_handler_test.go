package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
)

func newVoiceTestServer(t *testing.T, opts ...ServerOption) *Server {
	t.Helper()
	cfg := &config.Config{
		AgentDir: t.TempDir(),
	}
	srv, err := New(cfg, nil, opts...)
	require.NoError(t, err)
	return srv
}

func TestCreateVoiceToken_Success(t *testing.T) {
	t.Parallel()

	var receivedKey atomic.Pointer[string]
	var receivedBody atomic.Pointer[map[string]any]

	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-goog-api-key")
		receivedKey.Store(&key)

		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			var m map[string]any
			if json.Unmarshal(bodyBytes, &m) == nil {
				receivedBody.Store(&m)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":       "auth_tokens/test-token-12345",
			"token":      "test-token-12345",
			"expireTime": "2026-09-02T18:00:00Z",
		})
	}))
	t.Cleanup(mockUpstream.Close)

	server := newVoiceTestServer(
		t,
		WithVoiceAPIKey("secret-test-key"),
		WithVoiceAuthURL(mockUpstream.URL),
		WithVoiceHTTPClient(mockUpstream.Client()),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/voice/token", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp VoiceTokenResponse
	err := json.Unmarshal(rr.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, "test-token-12345", resp.Token)
	assert.Equal(t, "2026-09-02T18:00:00Z", resp.ExpireTime)
	assert.Equal(t, TranscribeLiveModel, resp.Model)

	storedKey := receivedKey.Load()
	require.NotNil(t, storedKey)
	assert.Equal(t, "secret-test-key", *storedKey)

	storedBody := receivedBody.Load()
	require.NotNil(t, storedBody)
	m := *storedBody
	assert.Equal(t, float64(1), m["uses"])
	require.NotNil(t, m["live_connect_constraints"])
	constraints, ok := m["live_connect_constraints"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, TranscribeLiveModel, constraints["model"])
}

func TestCreateVoiceToken_DefensiveFieldParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		upstreamJSON string
		expectedTok  string
	}{
		{
			name:         "token field present",
			upstreamJSON: `{"token": "token-direct", "expireTime": "2026-09-02T19:00:00Z"}`,
			expectedTok:  "token-direct",
		},
		{
			name:         "name field present fallback",
			upstreamJSON: `{"name": "auth_tokens/name-fallback", "expireTime": "2026-09-02T19:00:00Z"}`,
			expectedTok:  "auth_tokens/name-fallback",
		},
		{
			name:         "both token and name present prefers token",
			upstreamJSON: `{"name": "auth_tokens/from-name", "token": "from-token", "expireTime": "2026-09-02T19:00:00Z"}`,
			expectedTok:  "from-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.upstreamJSON))
			}))
			t.Cleanup(mockUpstream.Close)

			server := newVoiceTestServer(
				t,
				WithVoiceAPIKey("secret-test-key"),
				WithVoiceAuthURL(mockUpstream.URL),
				WithVoiceHTTPClient(mockUpstream.Client()),
			)

			req := httptest.NewRequest(http.MethodPost, "/api/voice/token", nil)
			rr := httptest.NewRecorder()
			server.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)

			var resp VoiceTokenResponse
			err := json.Unmarshal(rr.Body.Bytes(), &resp)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedTok, resp.Token)
			assert.Equal(t, TranscribeLiveModel, resp.Model)
		})
	}
}

func TestCreateVoiceToken_MissingAPIKey(t *testing.T) {
	t.Parallel()

	server := newVoiceTestServer(
		t,
		WithVoiceAPIKey(""),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/voice/token", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var errResp map[string]string
	err := json.Unmarshal(rr.Body.Bytes(), &errResp)
	require.NoError(t, err)
	assert.Contains(t, errResp["error"], "Gemini API key is not configured")
}

func TestCreateVoiceToken_UpstreamError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		upstreamCode int
		upstreamBody string
	}{
		{
			name:         "upstream 403 forbidden",
			upstreamCode: http.StatusForbidden,
			upstreamBody: `{"error":{"code":403,"message":"API key not valid"}}`,
		},
		{
			name:         "upstream 500 internal error",
			upstreamCode: http.StatusInternalServerError,
			upstreamBody: `{"error":{"code":500,"message":"internal error"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.upstreamCode)
				_, _ = w.Write([]byte(tt.upstreamBody))
			}))
			t.Cleanup(mockUpstream.Close)

			server := newVoiceTestServer(
				t,
				WithVoiceAPIKey("secret-test-key"),
				WithVoiceAuthURL(mockUpstream.URL),
				WithVoiceHTTPClient(mockUpstream.Client()),
			)

			req := httptest.NewRequest(http.MethodPost, "/api/voice/token", nil)
			rr := httptest.NewRecorder()
			server.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusBadGateway, rr.Code)

			var errResp map[string]string
			err := json.Unmarshal(rr.Body.Bytes(), &errResp)
			require.NoError(t, err)
			assert.Contains(t, errResp["error"], "upstream voice auth error")
		})
	}
}

func TestCreateVoiceToken_MalformedUpstreamResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		upstreamBody string
		errSubstring string
	}{
		{
			name:         "non-json response",
			upstreamBody: `bad gateway html`,
			errSubstring: "invalid response from upstream voice auth",
		},
		{
			name:         "empty token response",
			upstreamBody: `{"name":"","token":""}`,
			errSubstring: "upstream voice auth response missing token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = strings.NewReader(tt.upstreamBody).WriteTo(w)
			}))
			t.Cleanup(mockUpstream.Close)

			server := newVoiceTestServer(
				t,
				WithVoiceAPIKey("secret-test-key"),
				WithVoiceAuthURL(mockUpstream.URL),
				WithVoiceHTTPClient(mockUpstream.Client()),
			)

			req := httptest.NewRequest(http.MethodPost, "/api/voice/token", nil)
			rr := httptest.NewRecorder()
			server.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusBadGateway, rr.Code)

			var errResp map[string]string
			err := json.Unmarshal(rr.Body.Bytes(), &errResp)
			require.NoError(t, err)
			assert.Contains(t, errResp["error"], tt.errSubstring)
		})
	}
}
