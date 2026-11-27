package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/josexy/mitmproxy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeInvoker struct {
	invokedReq *http.Request
}

func (f *fakeInvoker) Invoke(req *http.Request) (*http.Response, error) {
	f.invokedReq = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString("ok")),
	}, nil
}

func TestProxyManager_EndToEndHTTPSTraffic(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "ca.crt")
	keyPath := filepath.Join(tmpDir, "ca.key")

	// 1. Generate test CA and server certificate for the upstream mock server
	upstreamCAKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	upstreamCATemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1001),
		Subject: pkix.Name{
			CommonName: "Upstream Test CA",
		},
		NotBefore:             time.Now().Add(-10 * time.Minute),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	upstreamCADER, err := x509.CreateCertificate(rand.Reader, upstreamCATemplate, upstreamCATemplate, &upstreamCAKey.PublicKey, upstreamCAKey)
	require.NoError(t, err)

	upstreamServerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	upstreamServerTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1002),
		Subject: pkix.Name{
			CommonName: "localhost",
		},
		NotBefore:             time.Now().Add(-10 * time.Minute),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		IsCA:                  false, // Non-CA server certificate!
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		BasicConstraintsValid: true,
	}
	upstreamServerDER, err := x509.CreateCertificate(rand.Reader, upstreamServerTemplate, upstreamCATemplate, &upstreamServerKey.PublicKey, upstreamCAKey)
	require.NoError(t, err)

	upstreamServerCert := tls.Certificate{
		Certificate: [][]byte{upstreamServerDER},
		PrivateKey:  upstreamServerKey,
	}

	upstreamServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "real-secret-12345" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("authenticated upstream response"))
		} else {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized: " + auth))
		}
	}))
	upstreamServer.TLS = &tls.Config{
		Certificates: []tls.Certificate{upstreamServerCert},
	}
	upstreamServer.StartTLS()
	t.Cleanup(upstreamServer.Close)

	upstreamURL, err := url.Parse(upstreamServer.URL)
	require.NoError(t, err)
	upstreamHost, _, err := net.SplitHostPort(upstreamURL.Host)
	require.NoError(t, err)

	// 2. Start ProxyManager; the upstream host gets a user key bound to it
	upstreamCertPath := filepath.Join(tmpDir, "upstream_ca.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstreamCADER})
	require.NoError(t, os.WriteFile(upstreamCertPath, caPEM, 0644))

	pm, err := NewManager(certPath, keyPath, mitmproxy.WithRootCAs(upstreamCertPath), mitmproxy.WithErrorHandler(func(ec mitmproxy.ErrorContext) {
		t.Logf("mitmproxy error: %v, hostport: %s, remote: %s", ec.Error, ec.Hostport, ec.RemoteAddr)
	}))
	require.NoError(t, err)

	dummies, release, err := pm.RegisterUserSecrets([]UserSecret{{Env: "UP_KEY", Real: "real-secret-12345", Host: upstreamHost}})
	require.NoError(t, err)
	t.Cleanup(release)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyAddr := ln.Addr().String()

	go func() {
		_ = pm.ServeListener(ln)
	}()
	t.Cleanup(func() {
		_ = pm.Shutdown(context.Background())
	})

	// 3. Setup client trusting the Asgard Root CA
	caCertPEM, err := os.ReadFile(certPath)
	require.NoError(t, err)

	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM(caCertPEM)

	proxyURL, err := url.Parse("http://" + proxyAddr)
	require.NoError(t, err)

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: certPool,
			},
		},
		Timeout: 5 * time.Second,
	}

	// 4. Send request carrying the per-run dummy through proxy
	req, err := http.NewRequest(http.MethodGet, upstreamServer.URL+"/test", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", dummies["UP_KEY"])

	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})

	respBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "authenticated upstream response", string(respBytes))
}

func TestProxyManager_NonRuleHostPassthrough(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "ca.crt")
	keyPath := filepath.Join(tmpDir, "ca.key")

	// 1. Upstream server that checks for direct un-intercepted traffic
	upstreamServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("passthrough ok: " + r.Header.Get("Authorization")))
	}))
	t.Cleanup(upstreamServer.Close)

	// 2. A key is registered for an unrelated host only
	pm, err := NewManager(certPath, keyPath)
	require.NoError(t, err)
	_, release, err := pm.RegisterUserSecrets([]UserSecret{{Env: "K", Real: "real-secret", Host: "some.other.domain.com"}})
	require.NoError(t, err)
	t.Cleanup(release)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyAddr := ln.Addr().String()

	go func() {
		_ = pm.ServeListener(ln)
	}()
	t.Cleanup(func() {
		_ = pm.Shutdown(context.Background())
	})

	// Client only trusts the upstream certificate (does NOT trust Asgard CA)
	// Because no registered key names this host, mitmproxy should transparently CONNECT tunnel it.
	// If it attempted TLS interception, client handshake would fail because client does not trust Asgard CA!
	certPool := x509.NewCertPool()
	certPool.AddCert(upstreamServer.Certificate())

	proxyURL, err := url.Parse("http://" + proxyAddr)
	require.NoError(t, err)

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: certPool,
			},
		},
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest(http.MethodGet, upstreamServer.URL+"/passthrough", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "client-original-header")

	resp, err := client.Do(req)
	require.NoError(t, err, "host without a registered key should pass through transparent CONNECT tunnel without TLS interception")
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})

	respBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "passthrough ok: client-original-header", string(respBytes))
}

func newTestManager(t *testing.T) *ProxyManager {
	t.Helper()
	dir := t.TempDir()
	pm, err := NewManager(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
	require.NoError(t, err)
	return pm
}

func TestProxyManager_StartBackgroundReportsBindSynchronously(t *testing.T) {
	t.Parallel()

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })
	failing := newTestManager(t)
	failing.server.Addr = busy.Addr().String()
	require.Error(t, failing.StartBackground(nil))
	assert.False(t, failing.Running(), "a failed bind is never running")

	pm := newTestManager(t)
	assert.Empty(t, pm.URL(), "no URL before listening")
	exited := make(chan error, 1)
	require.NoError(t, pm.StartBackground(func(err error) { exited <- err }))
	assert.True(t, pm.Running(), "running as soon as StartBackground returns")
	assert.True(t, strings.HasPrefix(pm.URL(), "http://127.0.0.1:"), pm.URL())
	assert.NotEqual(t, "127.0.0.1:0", pm.Addr(), "an ephemeral port is bound")
	require.NoError(t, pm.Shutdown(context.Background()))
	assert.NoError(t, <-exited)
	assert.False(t, pm.Running())
}

func TestProxyManager_HostBoundUserSecret(t *testing.T) {
	t.Parallel()

	pm := newTestManager(t)
	assert.Equal(t, []string{noHostsSentinel}, pm.mitmHosts(), "no key, no MITM")

	dummies, release, err := pm.RegisterUserSecrets([]UserSecret{
		{Env: "GEMINI_API_KEY", Real: "gem-real", Host: "Generativelanguage.googleapis.com", PathPrefix: "/v1beta", HeaderKey: "x-goog-api-key"},
		{Env: "BEARER_KEY", Real: "bear-real", Host: "bearer.example.com:443"},
		{Env: "NO_HOST", Real: "ignored"},
	})
	require.NoError(t, err)
	assert.NotContains(t, dummies, "NO_HOST", "a key without a host is never served")
	assert.ElementsMatch(t, []string{"generativelanguage.googleapis.com", "bearer.example.com"}, pm.mitmHosts())

	do := func(host, path, header, val string) string {
		req, err := http.NewRequest(http.MethodPost, "https://"+host+path, nil)
		require.NoError(t, err)
		req.Host = host
		req.Header.Set(header, val)
		_, err = pm.Interceptor(context.Background(), req, &fakeInvoker{})
		require.NoError(t, err)
		return req.Header.Get(header)
	}

	g := dummies["GEMINI_API_KEY"]
	assert.NotContains(t, g, "gem-real")
	assert.Equal(t, "gem-real", do("generativelanguage.googleapis.com", "/v1beta/models/x", "x-goog-api-key", g))
	// Wrong path prefix, wrong header, wrong host: the dummy is left alone.
	assert.Equal(t, g, do("generativelanguage.googleapis.com", "/v2/x", "x-goog-api-key", g))
	assert.Equal(t, g, do("generativelanguage.googleapis.com", "/v1beta/x", "Authorization", g))
	assert.Equal(t, g, do("other.example.com", "/v1beta/x", "x-goog-api-key", g))
	// Header defaults to Authorization and keeps the Bearer scheme.
	assert.Equal(t, "Bearer bear-real", do("bearer.example.com", "/x", "Authorization", "Bearer "+dummies["BEARER_KEY"]))
	// A value that is not a registered dummy is never touched.
	assert.Equal(t, "Bearer user-own", do("bearer.example.com", "/x", "Authorization", "Bearer user-own"))

	release()
	assert.Equal(t, []string{noHostsSentinel}, pm.mitmHosts())
	assert.Equal(t, g, do("generativelanguage.googleapis.com", "/v1beta/x", "x-goog-api-key", g), "released dummies stop working")
}
