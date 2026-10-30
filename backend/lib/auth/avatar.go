package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"
)

const (
	// maxAvatarBytes caps a downloaded avatar so a hostile picture URL cannot
	// fill the disk.
	maxAvatarBytes = 2 << 20
	// avatarFetchTimeout bounds one avatar download including redirects.
	avatarFetchTimeout = 5 * time.Second
	// maxAvatarRedirects bounds redirect chains followed while fetching.
	maxAvatarRedirects = 3
	// avatarFailureTTL suppresses refetching a picture URL that just failed,
	// so a dead host is not retried (and logged) on every /me request.
	avatarFailureTTL = 5 * time.Minute

	// AvatarPath is the local endpoint serving the current user's cached avatar.
	AvatarPath = "/api/auth/avatar"
)

// profileResponse is the body of GET /api/auth/me.
type profileResponse struct {
	Subject   string `json:"subject"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
	AvatarURL string `json:"avatarUrl,omitempty"`
	// IsAdmin tells the WebUI to show the admin pages; the server enforces it.
	IsAdmin bool `json:"isAdmin"`
}

// avatarMeta is the sidecar describing a cached avatar file.
type avatarMeta struct {
	Source      string `json:"source"`
	ContentType string `json:"contentType"`
}

// avatarStore caches IdP profile pictures on local disk so the browser never
// contacts the IdP. Each subject owns <dir>/<sha256(sub)>.img plus a .json
// sidecar recording the source URL, so a changed picture URL triggers a refetch.
type avatarStore struct {
	dir    string
	client *http.Client

	// group serializes fetches per subject without blocking other users.
	group singleflight.Group

	failMu   sync.Mutex
	failures map[string]avatarFailure
}

type avatarFailure struct {
	source string
	at     time.Time
}

func newAvatarStore(dir string) *avatarStore {
	return &avatarStore{dir: dir, client: newAvatarClient(), failures: map[string]avatarFailure{}}
}

// newAvatarClient returns a client that refuses to connect to non-public
// addresses. The check runs on the resolved IP at dial time, so it also covers
// DNS rebinding and every redirect hop.
func newAvatarClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: avatarFetchTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !isPublicIP(ip.Unmap()) {
				return fmt.Errorf("avatar: refusing to connect to non-public address %q", host)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: avatarFetchTimeout,
		// No proxy: the dial check must see the real destination.
		Transport: &http.Transport{DialContext: dialer.DialContext, IdleConnTimeout: 30 * time.Second},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxAvatarRedirects {
				return errors.New("avatar: too many redirects")
			}
			return checkAvatarURL(req.URL)
		},
	}
}

// nonPublicPrefixes lists special-purpose ranges that the netip predicates
// (IsPrivate, IsLoopback, ...) do not cover.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved, incl. 255.255.255.255
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64
	netip.MustParsePrefix("100::/64"),      // discard-only
}

func isPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func checkAvatarURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("avatar: unsupported scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("avatar: missing host")
	}
	return nil
}

func (a *avatarStore) paths(subject string) (img, meta string) {
	sum := sha256.Sum256([]byte(subject))
	base := filepath.Join(a.dir, hex.EncodeToString(sum[:]))
	return base + ".img", base + ".json"
}

// Ensure makes the cached avatar match pictureURL, downloading it when absent
// or when the source changed. It reports whether a usable avatar is cached.
func (a *avatarStore) Ensure(ctx context.Context, subject, pictureURL string) bool {
	if a == nil || subject == "" {
		return false
	}
	img, metaPath := a.paths(subject)

	if pictureURL == "" {
		_ = os.Remove(img)
		_ = os.Remove(metaPath)
		return false
	}
	cached := func() bool {
		if m, ok := readAvatarMeta(metaPath); ok && m.Source == pictureURL {
			_, err := os.Stat(img)
			return err == nil
		}
		return false
	}
	if cached() {
		return true
	}

	a.failMu.Lock()
	f, failed := a.failures[subject]
	a.failMu.Unlock()
	if !failed || f.source != pictureURL || time.Since(f.at) >= avatarFailureTTL {
		_, _, _ = a.group.Do(subject, func() (any, error) {
			if cached() {
				return nil, nil
			}
			err := a.fetch(ctx, pictureURL, img, metaPath)
			a.failMu.Lock()
			defer a.failMu.Unlock()
			if err != nil {
				log.Warn().Err(err).Msg("auth: failed to cache user avatar")
				a.failures[subject] = avatarFailure{source: pictureURL, at: time.Now()}
			} else {
				delete(a.failures, subject)
			}
			return nil, nil
		})
		if cached() {
			return true
		}
	}
	// Keep serving a previous avatar if one exists.
	_, err := os.Stat(img)
	return err == nil
}

func (a *avatarStore) fetch(ctx context.Context, pictureURL, img, metaPath string) error {
	u, err := url.Parse(pictureURL)
	if err != nil {
		return err
	}
	if err := checkAvatarURL(u); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pictureURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "image/*")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("avatar: unexpected status %d", resp.StatusCode)
	}
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]))
	// SVG can carry script, so only raster formats are accepted.
	if !strings.HasPrefix(ct, "image/") || strings.Contains(ct, "svg") {
		return fmt.Errorf("avatar: unsupported content type %q", ct)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAvatarBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxAvatarBytes {
		return errors.New("avatar: image too large")
	}
	if err := os.MkdirAll(a.dir, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(img, data); err != nil {
		return err
	}
	meta, _ := json.Marshal(avatarMeta{Source: pictureURL, ContentType: ct})
	return writeFileAtomic(metaPath, meta)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readAvatarMeta(path string) (avatarMeta, bool) {
	var m avatarMeta
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &m) != nil {
		return m, false
	}
	return m, true
}

// extractProfile reads display name, email and picture from ID token claims,
// falling back to preferred_username and then email when name is absent.
func extractProfile(claims map[string]any) (name, email, picture string) {
	str := func(k string) string {
		v, _ := claims[k].(string)
		return strings.TrimSpace(v)
	}
	email = str("email")
	picture = str("picture")
	name = str("name")
	if name == "" {
		name = str("preferred_username")
	}
	if name == "" {
		name = email
	}
	return name, email, picture
}

// HandleMe returns the authenticated user's profile and caches their avatar.
// It responds 404 when auth is disabled or the request carries no user.
func (s *Service) HandleMe(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		writeJSONError(w, http.StatusNotFound, "authentication disabled")
		return
	}
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no user profile")
		return
	}
	resp := profileResponse{Subject: u.Subject, Name: u.Name, Email: u.Email, IsAdmin: containsRole(u.Roles, AdminRole)}
	if s.avatars.Ensure(r.Context(), u.Subject, u.Picture) {
		resp.AvatarURL = AvatarPath
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

// HandleAvatar serves the authenticated user's locally cached avatar.
func (s *Service) HandleAvatar(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		writeJSONError(w, http.StatusNotFound, "authentication disabled")
		return
	}
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no avatar")
		return
	}
	if s.avatars == nil {
		writeJSONError(w, http.StatusNotFound, "no avatar")
		return
	}
	img, metaPath := s.avatars.paths(u.Subject)
	m, metaOK := readAvatarMeta(metaPath)
	f, err := os.Open(img)
	if !metaOK || err != nil {
		writeJSONError(w, http.StatusNotFound, "no avatar")
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = io.Copy(w, f)
}
