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
	"sync/atomic"
	"testing"
	"time"

	"github.com/josexy/mitmproxy-go"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyManager_NewManager_EmptyRulesReject(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "ca.crt")
	keyPath := filepath.Join(tmpDir, "ca.key")

	tests := []struct {
		name        string
		cfg         *Config
		errContains string
	}{
		{
			name:        "nil config",
			cfg:         nil,
			errContains: "config cannot be nil",
		},
		{
			name: "empty rules",
			cfg: &Config{
				Enable: true,
				Server: ServerConfig{
					Addr:   "127.0.0.1:0",
					CACert: certPath,
					CAKey:  keyPath,
				},
				Rules: []Rule{},
			},
			errContains: "cannot initialize proxy manager: rules list or host targets must not be empty",
		},
		{
			name: "rules with empty host",
			cfg: &Config{
				Enable: true,
				Server: ServerConfig{
					Addr:   "127.0.0.1:0",
					CACert: certPath,
					CAKey:  keyPath,
				},
				Rules: []Rule{
					{Host: "   ", HeaderKey: "Authorization", RealSecret: "secret"},
				},
			},
			errContains: "cannot initialize proxy manager: rules list or host targets must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pm, err := NewManager(tt.cfg, "")
			require.Error(t, err)
			assert.Nil(t, pm)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

type countingReader struct {
	data       []byte
	pos        int
	readCounts atomic.Int32
}

func (c *countingReader) Read(p []byte) (n int, err error) {
	c.readCounts.Add(1)
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	n = copy(p, c.data[c.pos:])
	c.pos += n
	return n, nil
}

func (c *countingReader) Close() error {
	return nil
}

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

func TestProxyManager_InterceptorSecretReplacement(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "ca.crt")
	keyPath := filepath.Join(tmpDir, "ca.key")

	cfg := &Config{
		Enable: true,
		Server: ServerConfig{
			Addr:   "127.0.0.1:0",
			CACert: certPath,
			CAKey:  keyPath,
		},
		Rules: []Rule{
			{
				Host:        "api.openai.com",
				PathPrefix:  "/v1/chat",
				HeaderKey:   "Authorization",
				RealSecret:  "Bearer sk-real-12345678",
				DummySecret: "Bearer fake-token",
			},
			{
				Host:        "api.anthropic.com",
				PathPrefix:  "",
				HeaderKey:   "x-api-key",
				RealSecret:  "real-anthropic-key-12345",
				DummySecret: "", // empty dummy secret means always replace
			},
		},
	}

	pm, err := NewManager(cfg, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pm.Shutdown(context.Background())
	})

	tests := []struct {
		name          string
		reqHost       string
		reqPath       string
		headerKey     string
		headerVal     string
		wantHeaderVal string
	}{
		{
			name:          "rule match and dummy secret match -> replaced",
			reqHost:       "api.openai.com",
			reqPath:       "/v1/chat/completions",
			headerKey:     "Authorization",
			headerVal:     "Bearer fake-token",
			wantHeaderVal: "Bearer sk-real-12345678",
		},
		{
			name:          "rule match with host:port -> replaced",
			reqHost:       "api.openai.com:443",
			reqPath:       "/v1/chat/models",
			headerKey:     "Authorization",
			headerVal:     "Bearer fake-token",
			wantHeaderVal: "Bearer sk-real-12345678",
		},
		{
			name:          "empty dummy secret -> always replaced",
			reqHost:       "api.anthropic.com",
			reqPath:       "/v1/messages",
			headerKey:     "x-api-key",
			headerVal:     "any-client-token",
			wantHeaderVal: "real-anthropic-key-12345",
		},
		{
			name:          "dummy secret does not match -> keep original",
			reqHost:       "api.openai.com",
			reqPath:       "/v1/chat/completions",
			headerKey:     "Authorization",
			headerVal:     "Bearer some-other-secret",
			wantHeaderVal: "Bearer some-other-secret",
		},
		{
			name:          "path prefix does not match -> keep original",
			reqHost:       "api.openai.com",
			reqPath:       "/v1/embeddings",
			headerKey:     "Authorization",
			headerVal:     "Bearer fake-token",
			wantHeaderVal: "Bearer fake-token",
		},
		{
			name:          "host does not match -> keep original",
			reqHost:       "api.gemini.google.com",
			reqPath:       "/v1/chat",
			headerKey:     "Authorization",
			headerVal:     "Bearer fake-token",
			wantHeaderVal: "Bearer fake-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequest(http.MethodPost, "https://"+tt.reqHost+tt.reqPath, nil)
			require.NoError(t, err)
			req.Host = tt.reqHost
			if tt.headerKey != "" {
				req.Header.Set(tt.headerKey, tt.headerVal)
			}

			invoker := &fakeInvoker{}
			resp, err := pm.Interceptor(context.Background(), req, invoker)
			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, tt.wantHeaderVal, req.Header.Get(tt.headerKey))
		})
	}
}

func TestProxyManager_InterceptorDumpHeadersRedaction(t *testing.T) {
	// Not marked t.Parallel() because it swaps global zerolog.DefaultContextLogger / log.Logger
	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "ca.crt")
	keyPath := filepath.Join(tmpDir, "ca.key")

	realSecret := "sk-real-super-secret-12345678"
	dummySecret := "dummy-placeholder"

	cfg := &Config{
		Enable: true,
		Server: ServerConfig{
			Addr:   "127.0.0.1:0",
			CACert: certPath,
			CAKey:  keyPath,
		},
		Debug: DebugConfig{
			Enable:      true,
			DumpHeaders: true,
		},
		Rules: []Rule{
			{
				Host:        "api.openai.com",
				HeaderKey:   "Authorization",
				RealSecret:  realSecret,
				DummySecret: dummySecret,
			},
		},
	}

	pm, err := NewManager(cfg, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pm.Shutdown(context.Background())
	})

	var buf bytes.Buffer
	origLogger := log.Logger
	log.Logger = zerolog.New(&buf).Level(zerolog.DebugLevel)
	t.Cleanup(func() {
		log.Logger = origLogger
	})

	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/models", nil)
	require.NoError(t, err)
	req.Host = "api.openai.com"
	req.Header.Set("Authorization", dummySecret)

	invoker := &fakeInvoker{}
	_, err = pm.Interceptor(context.Background(), req, invoker)
	require.NoError(t, err)

	logOutput := buf.String()
	// Real secret MUST NOT appear in plaintext in log output
	assert.False(t, strings.Contains(logOutput, realSecret), "plain real secret must not appear in logs: %s", logOutput)
	// Masked real secret should appear in substitution log and header dump
	maskedSecret := MaskSecret(realSecret)
	assert.True(t, strings.Contains(logOutput, maskedSecret), "masked secret should appear in logs: %s", logOutput)
	assert.True(t, strings.Contains(logOutput, "proxy dumped request headers"), "dump headers message should be logged")
}

func TestProxyManager_InterceptorBodyUntouchedWhenDumpBodyFalse(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "ca.crt")
	keyPath := filepath.Join(tmpDir, "ca.key")

	cfg := &Config{
		Enable: true,
		Server: ServerConfig{
			Addr:   "127.0.0.1:0",
			CACert: certPath,
			CAKey:  keyPath,
		},
		Debug: DebugConfig{
			Enable:   true,
			DumpBody: false, // Body must NOT be touched
		},
		Rules: []Rule{
			{
				Host:        "api.openai.com",
				HeaderKey:   "Authorization",
				RealSecret:  "Bearer real-secret",
				DummySecret: "Bearer fake-token",
			},
		},
	}

	pm, err := NewManager(cfg, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pm.Shutdown(context.Background())
	})

	// 1. When DumpBody is false, req.Body.Read should not be called by interceptor
	reader1 := &countingReader{data: []byte("test body payload 1")}
	req1, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/test", reader1)
	require.NoError(t, err)

	invoker1 := &fakeInvoker{}
	_, err = pm.Interceptor(context.Background(), req1, invoker1)
	require.NoError(t, err)
	assert.Equal(t, int32(0), reader1.readCounts.Load(), "req.Body must remain untouched when DumpBody=false")

	// 2. Enable DumpBody, req.Body is read and restored, downstream can read full content
	cfg.Debug.DumpBody = true
	err = pm.Reload(cfg)
	require.NoError(t, err)

	bodyPayload := "test body payload 2"
	reader2 := &countingReader{data: []byte(bodyPayload)}
	req2, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/test", reader2)
	require.NoError(t, err)

	invoker2 := &fakeInvoker{}
	_, err = pm.Interceptor(context.Background(), req2, invoker2)
	require.NoError(t, err)
	assert.Greater(t, reader2.readCounts.Load(), int32(0), "req.Body should have been read for dump")

	// Downstream read must get complete payload
	readAfter, err := io.ReadAll(req2.Body)
	require.NoError(t, err)
	assert.Equal(t, bodyPayload, string(readAfter))
}

func TestProxyManager_GetMaskedRules(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "ca.crt")
	keyPath := filepath.Join(tmpDir, "ca.key")

	cfg := &Config{
		Enable: true,
		Server: ServerConfig{
			Addr:   "127.0.0.1:0",
			CACert: certPath,
			CAKey:  keyPath,
		},
		Rules: []Rule{
			{
				Host:        "api.openai.com",
				HeaderKey:   "Authorization",
				RealSecret:  "Bearer sk-proj-1234567890abcdef",
				DummySecret: "Bearer fake-token-12345",
			},
		},
	}

	pm, err := NewManager(cfg, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pm.Shutdown(context.Background())
	})

	masked := pm.GetMaskedRules()
	require.Len(t, masked, 1)
	assert.Equal(t, "api.openai.com", masked[0].Host)
	assert.Equal(t, "Bearer sk-p****cdef", masked[0].RealSecret)
	assert.Equal(t, "Bearer fake****2345", masked[0].DummySecret)

	// Verify original rules were not modified
	original := *pm.rulesPtr.Load()
	assert.Equal(t, "Bearer sk-proj-1234567890abcdef", original[0].RealSecret)
	assert.Equal(t, "Bearer fake-token-12345", original[0].DummySecret)
}

func TestProxyManager_AtomicReload(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "ca.crt")
	keyPath := filepath.Join(tmpDir, "ca.key")
	configPath := filepath.Join(tmpDir, "proxy.yaml")

	cfg1 := &Config{
		Enable: true,
		Server: ServerConfig{
			Addr:   "127.0.0.1:0",
			CACert: certPath,
			CAKey:  keyPath,
		},
		Rules: []Rule{
			{
				Host:        "api.initial.com",
				HeaderKey:   "Authorization",
				RealSecret:  "initial-real",
				DummySecret: "dummy",
			},
		},
	}

	pm, err := NewManager(cfg1, configPath)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pm.Shutdown(context.Background())
	})

	// Rule 1 matches
	req1, _ := http.NewRequest(http.MethodGet, "https://api.initial.com/v1", nil)
	req1.Header.Set("Authorization", "dummy")
	_, err = pm.Interceptor(context.Background(), req1, &fakeInvoker{})
	require.NoError(t, err)
	assert.Equal(t, "initial-real", req1.Header.Get("Authorization"))

	// Reload with Rule 2
	cfg2 := &Config{
		Enable: true,
		Server: ServerConfig{
			Addr:   "127.0.0.1:0",
			CACert: certPath,
			CAKey:  keyPath,
		},
		Rules: []Rule{
			{
				Host:        "api.updated.com",
				HeaderKey:   "Authorization",
				RealSecret:  "updated-real",
				DummySecret: "dummy",
			},
		},
	}
	err = pm.Reload(cfg2)
	require.NoError(t, err)

	// Old host no longer matches
	reqOld, _ := http.NewRequest(http.MethodGet, "https://api.initial.com/v1", nil)
	reqOld.Header.Set("Authorization", "dummy")
	_, err = pm.Interceptor(context.Background(), reqOld, &fakeInvoker{})
	require.NoError(t, err)
	assert.Equal(t, "dummy", reqOld.Header.Get("Authorization"))

	// New host matches
	reqNew, _ := http.NewRequest(http.MethodGet, "https://api.updated.com/v1", nil)
	reqNew.Header.Set("Authorization", "dummy")
	_, err = pm.Interceptor(context.Background(), reqNew, &fakeInvoker{})
	require.NoError(t, err)
	assert.Equal(t, "updated-real", reqNew.Header.Get("Authorization"))

	// Test ReloadFromFile
	configContent := `
enable: true
server:
  addr: 127.0.0.1:0
  ca_cert: ` + certPath + `
  ca_key: ` + keyPath + `
rules:
  - host: api.fromfile.com
    header_key: Authorization
    real_secret: fromfile-real
    dummy_secret: dummy
`
	err = os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	reloadedCfg, err := pm.ReloadFromFile()
	require.NoError(t, err)
	assert.Equal(t, "api.fromfile.com", reloadedCfg.Rules[0].Host)

	reqFile, _ := http.NewRequest(http.MethodGet, "https://api.fromfile.com/v1", nil)
	reqFile.Header.Set("Authorization", "dummy")
	_, err = pm.Interceptor(context.Background(), reqFile, &fakeInvoker{})
	require.NoError(t, err)
	assert.Equal(t, "fromfile-real", reqFile.Header.Get("Authorization"))
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

	// 2. Configure and start ProxyManager on a free port
	cfg := &Config{
		Enable: true,
		Server: ServerConfig{
			Addr:   "127.0.0.1:0",
			CACert: certPath,
			CAKey:  keyPath,
		},
		Rules: []Rule{
			{
				Host:        upstreamHost,
				HeaderKey:   "Authorization",
				RealSecret:  "real-secret-12345",
				DummySecret: "dummy-secret-client",
			},
		},
	}

	upstreamCertPath := filepath.Join(tmpDir, "upstream_ca.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstreamCADER})
	require.NoError(t, os.WriteFile(upstreamCertPath, caPEM, 0644))

	pm, err := NewManager(cfg, "", mitmproxy.WithRootCAs(upstreamCertPath), mitmproxy.WithErrorHandler(func(ec mitmproxy.ErrorContext) {
		t.Logf("mitmproxy error: %v, hostport: %s, remote: %s", ec.Error, ec.Hostport, ec.RemoteAddr)
	}))
	require.NoError(t, err)

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

	// 4. Send request carrying dummy-secret-client through proxy
	req, err := http.NewRequest(http.MethodGet, upstreamServer.URL+"/test", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "dummy-secret-client")

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

	// 2. Configure ProxyManager with rules for an unrelated host only
	cfg := &Config{
		Enable: true,
		Server: ServerConfig{
			Addr:   "127.0.0.1:0",
			CACert: certPath,
			CAKey:  keyPath,
		},
		Rules: []Rule{
			{
				Host:        "some.other.domain.com",
				HeaderKey:   "Authorization",
				RealSecret:  "real-secret",
				DummySecret: "dummy",
			},
		},
	}

	pm, err := NewManager(cfg, "")
	require.NoError(t, err)

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
	// Because host is not in proxy rules, mitmproxy should transparently CONNECT tunnel it.
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
	require.NoError(t, err, "non-rule host should pass through transparent CONNECT tunnel without TLS interception")
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})

	respBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "passthrough ok: client-original-header", string(respBytes))
}
