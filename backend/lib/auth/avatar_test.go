package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/auth/authtest"
)

func TestExtractProfile_Fallbacks(t *testing.T) {
	name, email, pic := extractProfile(map[string]any{
		"name": " Ada ", "preferred_username": "ada", "email": "a@x.io", "picture": "https://x/p.png",
	})
	assert.Equal(t, "Ada", name)
	assert.Equal(t, "a@x.io", email)
	assert.Equal(t, "https://x/p.png", pic)

	name, _, _ = extractProfile(map[string]any{"preferred_username": "ada", "email": "a@x.io"})
	assert.Equal(t, "ada", name)

	name, _, _ = extractProfile(map[string]any{"email": "a@x.io"})
	assert.Equal(t, "a@x.io", name)

	name, _, _ = extractProfile(map[string]any{"name": 42})
	assert.Empty(t, name)
}

func TestVerifyToken_PopulatesProfile(t *testing.T) {
	p := authtest.NewProvider(t)
	s := newTestService(t, p, nil)
	claims := p.Claims()
	claims["name"] = "Ada Lovelace"
	claims["email"] = "ada@example.com"
	claims["picture"] = "https://idp.example/ada.png"

	status, user, err := s.verifyToken(context.Background(), p.Sign(t, claims))
	require.NoError(t, err)
	require.Equal(t, tokenValid, status)
	assert.Equal(t, "Ada Lovelace", user.Name)
	assert.Equal(t, "ada@example.com", user.Email)
	assert.Equal(t, "https://idp.example/ada.png", user.Picture)
}

// newPictureServer serves body with contentType and counts hits.
func newPictureServer(t *testing.T, contentType, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// testAvatarStore returns a store whose client may reach the loopback test server.
func testAvatarStore(t *testing.T) *avatarStore {
	t.Helper()
	a := newAvatarStore(t.TempDir())
	a.client = &http.Client{}
	return a
}

func TestAvatarStore_CachesAndRefetchesOnURLChange(t *testing.T) {
	srv, hits := newPictureServer(t, "image/png", "PNGDATA")
	a := testAvatarStore(t)
	ctx := context.Background()

	require.True(t, a.Ensure(ctx, "u1", srv.URL+"/a.png"))
	require.True(t, a.Ensure(ctx, "u1", srv.URL+"/a.png"))
	assert.EqualValues(t, 1, hits.Load(), "unchanged URL must be served from cache")

	require.True(t, a.Ensure(ctx, "u1", srv.URL+"/b.png"))
	assert.EqualValues(t, 2, hits.Load(), "changed URL must refetch")

	assert.False(t, a.Ensure(ctx, "u1", ""), "cleared picture drops the cache")
}

func TestAvatarStore_Rejects(t *testing.T) {
	ctx := context.Background()

	html, _ := newPictureServer(t, "text/html", "<html>")
	assert.False(t, testAvatarStore(t).Ensure(ctx, "u", html.URL))

	svg, _ := newPictureServer(t, "image/svg+xml", "<svg/>")
	assert.False(t, testAvatarStore(t).Ensure(ctx, "u", svg.URL))

	big, _ := newPictureServer(t, "image/png", strings.Repeat("x", maxAvatarBytes+1))
	assert.False(t, testAvatarStore(t).Ensure(ctx, "u", big.URL))

	assert.False(t, testAvatarStore(t).Ensure(ctx, "u", "file:///etc/passwd"))
	assert.False(t, testAvatarStore(t).Ensure(ctx, "u", "ftp://example.com/a.png"))
}

func TestAvatarStore_DefaultClientBlocksPrivateAddresses(t *testing.T) {
	srv, hits := newPictureServer(t, "image/png", "PNGDATA")
	a := newAvatarStore(t.TempDir()) // production client
	assert.False(t, a.Ensure(context.Background(), "u", srv.URL))
	assert.EqualValues(t, 0, hits.Load())
}

func TestHandleMeAndAvatar(t *testing.T) {
	p := authtest.NewProvider(t)
	s := newTestService(t, p, nil)
	s.avatars = testAvatarStore(t)
	pic, _ := newPictureServer(t, "image/png", "PNGDATA")

	user := &User{Subject: "sub-1", Name: "Ada", Email: "ada@example.com", Picture: pic.URL}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil).WithContext(WithUser(context.Background(), user))
	rec := httptest.NewRecorder()
	s.HandleMe(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"subject":"sub-1","name":"Ada","email":"ada@example.com","avatarUrl":"/api/auth/avatar"}`, rec.Body.String())

	req = httptest.NewRequest(http.MethodGet, AvatarPath, nil).WithContext(WithUser(context.Background(), user))
	rec = httptest.NewRecorder()
	s.HandleAvatar(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, "PNGDATA", rec.Body.String())

	// A user with no cached avatar gets 404 rather than someone else's.
	other := &User{Subject: "sub-2"}
	req = httptest.NewRequest(http.MethodGet, AvatarPath, nil).WithContext(WithUser(context.Background(), other))
	rec = httptest.NewRecorder()
	s.HandleAvatar(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestIsPublicIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "::1": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false,
		"10.1.2.3": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "192.0.0.1": false, "198.18.0.1": false,
		"240.0.0.1": false, "255.255.255.255": false, "0.0.0.0": false,
	} {
		assert.Equal(t, want, isPublicIP(netip.MustParseAddr(ip)), ip)
	}
}

func TestAvatarClient_RedirectChecks(t *testing.T) {
	c := newAvatarClient()
	mk := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	assert.Error(t, c.CheckRedirect(mk("file:///etc/passwd"), nil))
	assert.Error(t, c.CheckRedirect(mk("https://example.com/a"), make([]*http.Request, maxAvatarRedirects+1)))
	assert.NoError(t, c.CheckRedirect(mk("https://example.com/a"), nil))
}

func TestAvatarStore_RedirectToPrivateAddressBlocked(t *testing.T) {
	target, hits := newPictureServer(t, "image/png", "PNGDATA")
	redir := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	t.Cleanup(redir.Close)
	a := newAvatarStore(t.TempDir()) // production client
	assert.False(t, a.Ensure(context.Background(), "u", redir.URL))
	assert.EqualValues(t, 0, hits.Load())
}

func TestAvatarStore_FailureBackoff(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/ok.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("PNG"))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	a := testAvatarStore(t)
	ctx := context.Background()
	assert.False(t, a.Ensure(ctx, "u", srv.URL+"/bad.png"))
	assert.False(t, a.Ensure(ctx, "u", srv.URL+"/bad.png"))
	assert.EqualValues(t, 1, hits.Load(), "failed URL must not be retried within the TTL")
	assert.True(t, a.Ensure(ctx, "u", srv.URL+"/ok.png"), "a changed URL is fetched immediately")
	assert.EqualValues(t, 2, hits.Load())
}

func TestHandleAvatar_HeadersAndZeroValueService(t *testing.T) {
	p := authtest.NewProvider(t)
	s := newTestService(t, p, nil)
	s.avatars = testAvatarStore(t)
	pic, _ := newPictureServer(t, "image/png", "PNGDATA")
	user := &User{Subject: "sub-1", Picture: pic.URL}
	ctx := WithUser(context.Background(), user)
	require.True(t, s.avatars.Ensure(ctx, user.Subject, user.Picture))

	rec := httptest.NewRecorder()
	s.HandleAvatar(rec, httptest.NewRequest(http.MethodGet, AvatarPath, nil).WithContext(ctx))
	assert.Equal(t, "private, max-age=3600", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "default-src 'none'; sandbox", rec.Header().Get("Content-Security-Policy"))

	rec = httptest.NewRecorder()
	(&Service{}).HandleAvatar(rec, httptest.NewRequest(http.MethodGet, AvatarPath, nil).WithContext(ctx))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleMe_NilServiceAndNoUser(t *testing.T) {
	var nilSvc *Service
	rec := httptest.NewRecorder()
	nilSvc.HandleMe(rec, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	p := authtest.NewProvider(t)
	s := newTestService(t, p, nil)
	rec = httptest.NewRecorder()
	s.HandleMe(rec, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
