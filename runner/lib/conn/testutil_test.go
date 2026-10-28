package conn

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// testPKI is a throwaway CA that signs the backend and runner certificates.
type testPKI struct {
	t       *testing.T
	ca      *x509.Certificate
	caKey   *ecdsa.PrivateKey
	pool    *x509.CertPool
	mu      sync.Mutex
	counter int64
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &testPKI{t: t, ca: ca, caKey: key, pool: pool}
}

// issue signs a leaf certificate with the given CN.
func (p *testPKI) issue(cn string, usage x509.ExtKeyUsage) tls.Certificate {
	p.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(p.t, err)
	p.mu.Lock()
	p.counter++
	serial := big.NewInt(100 + p.counter)
	p.mu.Unlock()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.caKey)
	require.NoError(p.t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (p *testPKI) clientTLS(cn string) func() (*tls.Config, error) {
	return func() (*tls.Config, error) {
		return &tls.Config{
			Certificates: []tls.Certificate{p.issue(cn, x509.ExtKeyUsageClientAuth)},
			RootCAs:      p.pool,
			ServerName:   "127.0.0.1",
			MinVersion:   tls.VersionTLS13,
		}, nil
	}
}

// hubEnv is a real runnerhub.Hub on a loopback listener.
type hubEnv struct {
	t       *testing.T
	pki     *testPKI
	opts    runnerhub.Options
	hub     *runnerhub.Hub
	mu      sync.Mutex
	allowed []string
}

func (e *hubEnv) allowedNodes() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.allowed...)
}

func (e *hubEnv) setAllowed(ids ...string) {
	e.mu.Lock()
	e.allowed = ids
	e.mu.Unlock()
	e.hub.RefreshAllowed()
}

func newHubEnv(t *testing.T, allowed ...string) *hubEnv {
	t.Helper()
	e := &hubEnv{t: t, pki: newTestPKI(t), allowed: allowed}
	dir := t.TempDir()
	certPEM, keyPEM := pemPair(t, e.pki.issue("backend", x509.ExtKeyUsageServerAuth))
	caPEM := pemCert(e.pki.ca.Raw)
	e.opts = runnerhub.Options{
		TLSCertFile:  writeFile(t, dir, "backend.crt", certPEM),
		TLSKeyFile:   writeFile(t, dir, "backend.key", keyPEM),
		ClientCAFile: writeFile(t, dir, "ca.crt", caPEM),
		AllowedNodes: e.allowedNodes,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	e.hub = e.startHub(ln)
	return e
}

func (e *hubEnv) startHub(ln net.Listener) *runnerhub.Hub {
	e.t.Helper()
	opts := e.opts
	opts.Listener = ln
	h := runnerhub.New(opts)
	require.NoError(e.t, h.Start())
	e.t.Cleanup(h.Stop)
	return h
}

// restartHub stops the hub and starts a new one (new boot id, empty state) on
// the same address.
func (e *hubEnv) restartHub() {
	e.t.Helper()
	addr := e.hub.Addr().String()
	e.hub.Stop()
	var ln net.Listener
	require.Eventually(e.t, func() bool {
		var err error
		ln, err = net.Listen("tcp", addr)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	e.hub = e.startHub(ln)
}

func (e *hubEnv) node(id string) (runnerhub.NodeInfo, bool) {
	for _, n := range e.hub.Nodes() {
		if n.NodeID == id {
			return n, true
		}
	}
	return runnerhub.NodeInfo{}, false
}

func (e *hubEnv) waitOnline(id string, online bool) {
	e.t.Helper()
	require.Eventually(e.t, func() bool {
		n, ok := e.node(id)
		return ok && n.Online == online
	}, 5*time.Second, 10*time.Millisecond)
}

// startClient runs a Client against addr until the test ends.
func startClient(t *testing.T, pki *testPKI, addr, cn string, mod func(*Options)) *Client {
	t.Helper()
	opts := Options{
		Addr:              addr,
		NodeID:            cn,
		TLSConfig:         pki.clientTLS(cn),
		Version:           "v-test",
		SupportedCLIs:     func() map[string][]string { return map[string][]string{"opencode": {"m1"}} },
		HeartbeatInterval: 30 * time.Millisecond,
		BackoffMin:        20 * time.Millisecond,
		BackoffMax:        100 * time.Millisecond,
		RegisterTimeout:   2 * time.Second,
	}
	if mod != nil {
		mod(&opts)
	}
	c := New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return c
}

// fakeBackend is a minimal NodeControlService host with a scripted Connect.
type fakeBackend struct {
	t       *testing.T
	pki     *testPKI
	ln      net.Listener
	connect func(attempt int, reg *runnerpb.Register, stream grpc.BidiStreamingServer[runnerpb.RunnerMessage, runnerpb.BackendMessage]) error

	mu       sync.Mutex
	attempts int
	regs     []*runnerpb.Register
}

type fakeNodeControl struct {
	runnerpb.UnimplementedNodeControlServiceServer
	b *fakeBackend
}

func (f fakeNodeControl) Connect(stream grpc.BidiStreamingServer[runnerpb.RunnerMessage, runnerpb.BackendMessage]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	f.b.mu.Lock()
	f.b.attempts++
	n := f.b.attempts
	f.b.regs = append(f.b.regs, first.GetRegister())
	f.b.mu.Unlock()
	return f.b.connect(n, first.GetRegister(), stream)
}

func newFakeBackend(t *testing.T, connect func(int, *runnerpb.Register, grpc.BidiStreamingServer[runnerpb.RunnerMessage, runnerpb.BackendMessage]) error) *fakeBackend {
	t.Helper()
	b := &fakeBackend{t: t, pki: newTestPKI(t), connect: connect}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{b.pki.issue("backend", x509.ExtKeyUsageServerAuth)},
		ClientCAs:    b.pki.pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	})
	require.NoError(t, err)
	b.ln = ln
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			cfg := yamux.DefaultConfig()
			cfg.LogOutput = io.Discard
			sess, err := yamux.Server(raw, cfg)
			if err != nil {
				_ = raw.Close()
				continue
			}
			srv := grpc.NewServer()
			runnerpb.RegisterNodeControlServiceServer(srv, fakeNodeControl{b: b})
			go func() {
				_ = srv.Serve(sess)
				srv.Stop()
			}()
		}
	}()
	return b
}

func (b *fakeBackend) attemptCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attempts
}

func (b *fakeBackend) register(i int) *runnerpb.Register {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.regs[i]
}

// recordingExecutor records started runs and completes them with Error "done:<id>"
// unless hold is set.
type recordingExecutor struct {
	mu        sync.Mutex
	started   []string
	cancelled []string
	hold      chan struct{} // when non-nil, results wait for it to close
	startedCh chan string
}

func newRecordingExecutor(hold chan struct{}) *recordingExecutor {
	return &recordingExecutor{hold: hold, startedCh: make(chan string, 64)}
}

func (e *recordingExecutor) StartRun(ctx context.Context, sr *runnerpb.StartRun, sink Sink) {
	e.mu.Lock()
	e.started = append(e.started, sr.GetRunId())
	e.mu.Unlock()
	e.startedCh <- sr.GetRunId()
	go func() {
		if e.hold != nil {
			<-e.hold
		}
		_ = sink.Result(ctx, &runnerpb.RunResult{RunId: sr.GetRunId(), ChatId: sr.GetChatId(), Error: "done:" + sr.GetRunId()})
	}()
}

func (e *recordingExecutor) CancelRun(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cancelled = append(e.cancelled, id)
}

func (e *recordingExecutor) startedRuns() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.started...)
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, data, 0o600))
	return p
}

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func pemPair(t *testing.T, c tls.Certificate) (certPEM, keyPEM []byte) {
	t.Helper()
	keyDER, err := x509.MarshalECPrivateKey(c.PrivateKey.(*ecdsa.PrivateKey))
	require.NoError(t, err)
	return pemCert(c.Certificate[0]), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
