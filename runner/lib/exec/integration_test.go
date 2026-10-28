package exec

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
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/runner/lib/conn"
)

const testNode = "runner-1"

// fakeBwrapScript stands in for bubblewrap. It prints its arguments. The agent
// sandbox (the only one whose arguments carry the prompt) posts a status
// update to the socket found behind the /fakebash bind mount. A prompt with
// HOLD makes it block until killed.
const fakeBwrapScript = `#!/bin/sh
prev=""
sock=""
for arg in "$@"; do
  if [ "$arg" = "/fakebash" ]; then sock="$prev/asgard.sock"; fi
  prev="$arg"
done
case "$*" in
  *HOLD*) exec sleep 60 ;;
  *ASKME*)
    reply=$(curl -sf --unix-socket "$sock" -H 'Content-Type: application/json' \
      -d '{"chat_id":"evil","question":"proceed?"}' http://asgard/ask-user) || reply="ask failed"
    printf "ask-reply: %s\n" "$reply"
    ;;
  *PEERME*)
    reply=$(curl -sf --unix-socket "$sock" -H 'Content-Type: application/json' \
      -d '{"chat_id":"evil","agent_id":"peer","prompt":"hello peer"}' http://asgard/call-peer) || reply="peer failed"
    printf "peer-reply: %s\n" "$reply"
    ;;
  *STATUSME*)
    curl -sf --unix-socket "$sock" -H 'Content-Type: application/json' \
      -d '{"chat_id":"evil","step_index":2,"source":"aw","entry_type":"tool_call","content":"working","metadata":{"target_files":["/tmp/out.md"]}}' \
      http://asgard/agent-status || echo "status post failed"
    ;;
esac
printf "%s\n" "$@"
echo "fake agent output"
`

// setupFakeSandbox points HOME and PATH at a throwaway environment with the fake bwrap.
func setupFakeSandbox(t *testing.T) {
	t.Helper()
	// A short HOME keeps the status socket path within sun_path for curl.
	home, err := os.MkdirTemp("", "h")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	for _, d := range []string{".gemini", ".cache", ".config", ".local"} {
		require.NoError(t, os.MkdirAll(filepath.Join(home, d), 0o755))
	}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "bwrap"), []byte(fakeBwrapScript), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Go toolchain caches must survive the HOME switch.
	if h := os.Getenv("HOME"); h != "" {
		if os.Getenv("GOCACHE") == "" {
			t.Setenv("GOCACHE", filepath.Join(h, ".cache", "go-build"))
		}
		if os.Getenv("GOPATH") == "" {
			t.Setenv("GOPATH", filepath.Join(h, "go"))
		}
	}
	t.Setenv("HOME", home)

	agentwrapper.SetClients(map[string]types.CLIClient{
		"opencode": &agentwrapper.FakeClient{
			UsageFunc: func(context.Context, types.UsageOptions) ([]types.ModelUsage, error) {
				return []types.ModelUsage{{Model: "m1", Remaining: 0.8}, {Model: "m0", Remaining: 0}}, nil
			},
		},
	})
	t.Cleanup(func() { agentwrapper.SetClients(nil) })
}

type pki struct {
	t     *testing.T
	ca    *x509.Certificate
	caKey *ecdsa.PrivateKey
	pool  *x509.CertPool
	n     int64
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &pki{t: t, ca: ca, caKey: key, pool: pool}
}

func (p *pki) issue(cn string, usage x509.ExtKeyUsage) tls.Certificate {
	p.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(p.t, err)
	p.n++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(100 + p.n), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.caKey)
	require.NoError(p.t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (p *pki) writePEM(dir, name string, block *pem.Block) string {
	p.t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(p.t, os.WriteFile(path, pem.EncodeToMemory(block), 0o600))
	return path
}

// startStack runs an in-process hub and a runner conn.Client wired to ex.
func startStack(t *testing.T, ex *Executor) *runnerhub.Hub {
	t.Helper()
	return startStackWith(t, ex, nil)
}

// startStackWith is startStack with the hub options adjustable by mutate. The
// executor's sandbox callbacks go over the stack's runner connection.
func startStackWith(t *testing.T, ex *Executor, mutate func(*runnerhub.Options)) *runnerhub.Hub {
	t.Helper()
	p := newPKI(t)
	dir := t.TempDir()
	srvCert := p.issue("backend", x509.ExtKeyUsageServerAuth)
	keyDER, err := x509.MarshalECPrivateKey(srvCert.PrivateKey.(*ecdsa.PrivateKey))
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	hubOpts := runnerhub.Options{
		TLSCertFile:  p.writePEM(dir, "b.crt", &pem.Block{Type: "CERTIFICATE", Bytes: srvCert.Certificate[0]}),
		TLSKeyFile:   p.writePEM(dir, "b.key", &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		ClientCAFile: p.writePEM(dir, "ca.crt", &pem.Block{Type: "CERTIFICATE", Bytes: p.ca.Raw}),
		AllowedNodes: func() []string { return []string{testNode} },
		Listener:     ln,
	}
	if mutate != nil {
		mutate(&hubOpts)
	}
	hub := runnerhub.New(hubOpts)
	require.NoError(t, hub.Start())
	t.Cleanup(hub.Stop)

	var client *conn.Client
	if ex.opts.Callbacks == nil {
		ex.opts.Callbacks = func() (runnerpb.AgentCallbackServiceClient, error) { return client.Callbacks() }
	}
	client = conn.New(conn.Options{
		Addr:   ln.Addr().String(),
		NodeID: testNode,
		TLSConfig: func() (*tls.Config, error) {
			return &tls.Config{
				Certificates: []tls.Certificate{p.issue(testNode, x509.ExtKeyUsageClientAuth)},
				RootCAs:      p.pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13,
			}, nil
		},
		SupportedCLIs:     func() map[string][]string { return map[string][]string{"opencode": {"m1"}} },
		Executor:          ex,
		HeartbeatInterval: 50 * time.Millisecond,
		BackoffMin:        20 * time.Millisecond,
		BackoffMax:        100 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = client.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	require.Eventually(t, func() bool {
		for _, n := range hub.Nodes() {
			if n.NodeID == testNode && n.Online {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	return hub
}

func integrationStartRun(t *testing.T, runID, prompt string) *runnerpb.StartRun {
	t.Helper()
	tmp := paths.SessionTmpDir("chat-int")
	require.NoError(t, os.MkdirAll(tmp, 0o755))
	return &runnerpb.StartRun{
		RunId:      runID,
		ChatId:     "chat-int",
		AgentId:    "a",
		AgentHash:  "h1",
		Attempt:    1,
		Prompt:     prompt,
		Candidates: []*runnerpb.Target{{Cli: "opencode", Model: "m1"}},
		RunDir:     &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP},
		RunDirs:    []string{tmp},
	}
}

func newIntegrationExecutor(t *testing.T, extra ...*agentspec.Agent) *Executor {
	t.Helper()
	agent := testAgent("a", "h1", "")
	agent.Config.CLI = []agentspec.CLITarget{{CLI: "opencode", Model: "m1"}}
	return New(Options{
		Agents:    &fakeAgents{agents: append([]*agentspec.Agent{agent}, extra...)},
		AgentWait: time.Second,
		Conf:      func() *config.Config { return &config.Config{ConfigPath: paths.ConfigFile()} },
	})
}

func TestIntegrationRunThroughHub(t *testing.T) {
	setupFakeSandbox(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required by the fake sandbox")
	}
	require.NoError(t, os.MkdirAll(paths.ConfigDir(), 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigFile(), []byte("secret"), 0o600))
	hub := startStack(t, newIntegrationExecutor(t))

	r, err := hub.Dispatch(context.Background(), testNode, integrationStartRun(t, "run-int-1", "do STATUSME please"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := r.Wait(ctx)
	require.NoError(t, err)

	rr := res.RunResult
	assert.Empty(t, rr.GetError())
	assert.Contains(t, string(res.Output), "fake agent output")
	assert.NotContains(t, string(res.Output), "status post failed")
	// The config dir/file is masked in the sandbox, as on the backend.
	assert.Contains(t, string(res.Output), paths.ConfigFile())
	assert.Equal(t, uint64(len(res.Output)), rr.GetOutputSize())
	assert.Equal(t, "opencode", rr.GetSelectedTarget().GetCli())
	assert.Equal(t, "m1", rr.GetSelectedTarget().GetModel())
	assert.Equal(t, "chat-int", rr.GetChatId())
	assert.Equal(t, uint32(1), rr.GetAttempt())
	assert.Equal(t, runnerpb.FileScope_FILE_SCOPE_TMP, rr.GetRunDirRef().GetScope())
	assert.DirExists(t, rr.GetRunDir())

	ev, err := r.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), ev.GetEventIndex())
	assert.Equal(t, "chat-int", ev.GetChatId(), "runner stamps the run's identity")
	assert.Equal(t, "working", ev.GetContent())
	require.Len(t, ev.GetArtifacts(), 1)
	assert.Equal(t, "/tmp/out.md", ev.GetArtifacts()[0].GetViewerPath())
}

func TestIntegrationCancelKillsSandbox(t *testing.T) {
	setupFakeSandbox(t)
	hub := startStack(t, newIntegrationExecutor(t))

	r, err := hub.Dispatch(context.Background(), testNode, integrationStartRun(t, "run-int-2", "HOLD"))
	require.NoError(t, err)

	// The run is reported active while it executes.
	require.Eventually(t, func() bool {
		for _, n := range hub.Nodes() {
			if n.NodeID == testNode && n.ActiveRuns > 0 {
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond)

	require.NoError(t, hub.Cancel(testNode, "run-int-2"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := r.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, ErrCancelled, res.RunResult.GetError())
}
