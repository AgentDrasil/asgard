package runnerhub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
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
	"google.golang.org/grpc/credentials/insecure"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// testPKI is a throwaway CA that signs the backend and runner certificates.
type testPKI struct {
	t       *testing.T
	ca      *x509.Certificate
	caKey   *ecdsa.PrivateKey
	caPEM   []byte
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
	return &testPKI{
		t:     t,
		ca:    ca,
		caKey: key,
		caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// issue signs a leaf certificate with the given CN and returns cert/key PEM.
func (p *testPKI) issue(cn string, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	p.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(p.t, err)
	p.counter++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(100 + p.counter),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.caKey)
	require.NoError(p.t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(p.t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// testEnv is a running Hub on a loopback listener plus its PKI.
type testEnv struct {
	t       *testing.T
	pki     *testPKI
	hub     *Hub
	opts    Options
	allowed []string
	mu      sync.Mutex
}

func (e *testEnv) allowedNodes() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.allowed...)
}

func (e *testEnv) setAllowed(ids ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.allowed = ids
}

func newTestEnv(t *testing.T, store FinalizedStore, cb runnerpb.AgentCallbackServiceServer, allowed ...string) *testEnv {
	t.Helper()
	e := &testEnv{t: t, pki: newTestPKI(t), allowed: allowed}
	dir := t.TempDir()
	certPEM, keyPEM := e.pki.issue("backend", x509.ExtKeyUsageServerAuth)
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, data, 0o600))
		return p
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	e.opts = Options{
		TLSCertFile:  write("backend.crt", certPEM),
		TLSKeyFile:   write("backend.key", keyPEM),
		ClientCAFile: write("ca.crt", e.pki.caPEM),
		Listener:     ln,
		AllowedNodes: e.allowedNodes,
		Store:        store,
		Callbacks:    cb,
	}
	e.hub = e.startHub(ln)
	return e
}

// withRegisterTimeout restarts the hub with a short register timeout.
func (e *testEnv) withRegisterTimeout(d time.Duration) {
	e.t.Helper()
	e.opts.RegisterTimeout = d
	e.restartHub(e.opts.Store)
}

// startHub starts a hub with the env options on ln, stopping it on cleanup.
func (e *testEnv) startHub(ln net.Listener) *Hub {
	e.t.Helper()
	opts := e.opts
	opts.Listener = ln
	h := New(opts)
	require.NoError(e.t, h.Start())
	e.t.Cleanup(h.Stop)
	return h
}

// restartHub stops the hub and starts a new one (new boot id, empty
// in-memory state) on the same address with the given store.
func (e *testEnv) restartHub(store FinalizedStore) {
	e.t.Helper()
	addr := e.hub.Addr().String()
	e.hub.Stop()
	var ln net.Listener
	require.Eventually(e.t, func() bool {
		var err error
		ln, err = net.Listen("tcp", addr)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	e.opts.Store = store
	e.hub = e.startHub(ln)
}

func (e *testEnv) waitOnline(id string, online bool) {
	e.t.Helper()
	require.Eventually(e.t, func() bool {
		for _, n := range e.hub.Nodes() {
			if n.NodeID == id {
				return n.Online == online
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
}

// fakeRunner speaks the runner side of the protocol. Its upstream seq counter
// and boot id persist across reconnects, like a real runner process.
type fakeRunner struct {
	t   *testing.T
	env *testEnv
	cn  string

	bootID          string
	version         string
	protocol        uint32
	skipRegister    bool
	registerNode    string // overrides Register.node_id when set
	active          []*runnerpb.RunRef
	lastBackendSeq  uint64
	lastBackendBoot string

	seq       uint64
	sess      *yamux.Session
	cc        *grpc.ClientConn
	stream    grpc.BidiStreamingClient[runnerpb.RunnerMessage, runnerpb.BackendMessage]
	cancel    context.CancelFunc
	msgs      chan *runnerpb.BackendMessage
	regResult *runnerpb.RegisterResult
	svc       *grpc.Server
}

func (e *testEnv) newRunner(cn string) *fakeRunner {
	return &fakeRunner{
		t:        e.t,
		env:      e,
		cn:       cn,
		bootID:   "runner-boot-1",
		version:  "v-test",
		protocol: runnerpb.ProtocolVersion,
	}
}

// fakeRunnerService is the RunnerService hosted by the fake runner.
type fakeRunnerService struct {
	runnerpb.UnimplementedRunnerServiceServer
}

func (fakeRunnerService) Tree(_ context.Context, req *runnerpb.TreeRequest) (*runnerpb.TreeResponse, error) {
	return &runnerpb.TreeResponse{Path: "tree:" + req.GetPath()}, nil
}

// connect dials the hub, serves RunnerService on accepted streams and
// registers. It returns an error if the connection or handshake fails; a
// rejected registration is not an error (inspect regResult).
func (f *fakeRunner) connect() error {
	f.t.Helper()
	certPEM, keyPEM := f.env.pki.issue(f.cn, x509.ExtKeyUsageClientAuth)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(f.t, err)
	pool := x509.NewCertPool()
	require.True(f.t, pool.AppendCertsFromPEM(f.env.pki.caPEM))
	conn, err := tls.Dial("tcp", f.env.hub.Addr().String(), &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	})
	if err != nil {
		return err
	}
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = discard{}
	sess, err := yamux.Client(conn, cfg)
	if err != nil {
		return err
	}
	f.sess = sess

	f.svc = grpc.NewServer()
	runnerpb.RegisterRunnerServiceServer(f.svc, fakeRunnerService{})
	go func() { _ = f.svc.Serve(sess) }()

	f.cc, err = grpc.NewClient("passthrough:///backend",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return sess.Open() }))
	require.NoError(f.t, err)
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.t.Cleanup(f.close)

	f.stream, err = runnerpb.NewNodeControlServiceClient(f.cc).Connect(ctx)
	if err != nil {
		return err
	}
	if f.skipRegister {
		return nil
	}
	if f.skipRegister {
		return nil
	}
	nodeID := f.cn
	if f.registerNode != "" {
		nodeID = f.registerNode
	}
	err = f.stream.Send(&runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_Register{Register: &runnerpb.Register{
		NodeId:                nodeID,
		Version:               f.version,
		ProtocolVersion:       f.protocol,
		SupportedClis:         map[string]*runnerpb.ModelList{"opencode": {Models: []string{"m1"}}},
		ActiveRuns:            f.active,
		LastAckedBackendSeq:   f.lastBackendSeq,
		RunnerBootId:          f.bootID,
		LastSeenBackendBootId: f.lastBackendBoot,
	}}})
	if err != nil {
		return err
	}
	first, err := f.stream.Recv()
	if err != nil {
		return err
	}
	f.regResult = first.GetRegisterResult()
	require.NotNil(f.t, f.regResult)
	if !f.regResult.GetAccepted() {
		return nil
	}
	f.lastBackendBoot = f.regResult.GetBackendBootId()
	f.msgs = make(chan *runnerpb.BackendMessage, 256)
	go func() {
		for {
			m, err := f.stream.Recv()
			if err != nil {
				close(f.msgs)
				return
			}
			f.msgs <- m
		}
	}()
	return nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func (f *fakeRunner) close() {
	if f.cancel != nil {
		f.cancel()
	}
	if f.svc != nil {
		f.svc.Stop()
	}
	if f.cc != nil {
		_ = f.cc.Close()
	}
	if f.sess != nil {
		_ = f.sess.Close()
	}
}

func (f *fakeRunner) mustConnect() {
	f.t.Helper()
	require.NoError(f.t, f.connect())
	require.True(f.t, f.regResult.GetAccepted(), f.regResult.GetRejectReason())
	f.env.waitOnline(f.cn, true)
}

// disconnect drops the connection and waits for the hub to notice.
func (f *fakeRunner) disconnect() {
	f.t.Helper()
	f.close()
	f.env.waitOnline(f.cn, false)
}

// nextBackend returns the next non-Ack message from the backend.
func (f *fakeRunner) nextBackend() *runnerpb.BackendMessage {
	f.t.Helper()
	for {
		select {
		case m, ok := <-f.msgs:
			require.True(f.t, ok, "backend stream closed")
			if m.GetAck() == nil {
				return m
			}
		case <-time.After(5 * time.Second):
			require.FailNow(f.t, "timed out waiting for backend message")
		}
	}
}

// expectNoBackend asserts no non-Ack message arrives shortly.
func (f *fakeRunner) expectNoBackend() {
	f.t.Helper()
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case m, ok := <-f.msgs:
			if !ok {
				return
			}
			require.NotNil(f.t, m.GetAck(), "unexpected backend message %v", m)
		case <-deadline:
			return
		}
	}
}

// waitAck blocks until the backend acked runner seq >= seq.
func (f *fakeRunner) waitAck(seq uint64) {
	f.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-f.msgs:
			require.True(f.t, ok, "backend stream closed")
			if a := m.GetAck(); a != nil && a.GetAckSeq() >= seq {
				return
			}
		case <-timeout:
			require.FailNow(f.t, "timed out waiting for ack")
		}
	}
}

// send sends payload with the next upstream seq and returns it.
func (f *fakeRunner) send(msg *runnerpb.RunnerMessage) uint64 {
	f.t.Helper()
	f.seq++
	msg.Seq = f.seq
	require.NoError(f.t, f.stream.Send(msg))
	return f.seq
}

func (f *fakeRunner) sendEvent(runID string, idx uint64, content string) uint64 {
	f.t.Helper()
	return f.send(&runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_RunEvent{RunEvent: &runnerpb.RunEvent{RunId: runID, EventIndex: idx, Content: content}}})
}

func (f *fakeRunner) sendChunk(runID string, off uint64, data string) uint64 {
	f.t.Helper()
	return f.send(&runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_RunOutput{RunOutput: &runnerpb.RunOutputChunk{RunId: runID, Offset: off, Data: []byte(data)}}})
}

func (f *fakeRunner) sendResult(res *runnerpb.RunResult) uint64 {
	f.t.Helper()
	return f.send(&runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_RunResult{RunResult: res}})
}

func mustWait(t *testing.T, r *Run) *Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := r.Wait(ctx)
	require.NoError(t, err)
	return res
}
