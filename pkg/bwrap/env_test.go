package bwrap

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func setRunnerSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("ETCD_ENDPOINTS", "http://etcd.internal:2379")
	t.Setenv("ETCD_PREFIX", "/secret-prefix")
	t.Setenv("ASGARD_RUNNER_TOKEN", "runner-secret-token")
	t.Setenv("FAKE_RUNNER_SECRET", "s3cr3t-value")
	t.Setenv("ASGARD_ENCRYPTION_KEY", "enc-secret")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("USER", "runneruser")
}

func assertCleanSandboxCmd(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	require.NotNil(t, cmd.Env, "bwrap parent env must be set explicitly")
	sepIdx := slices.Index(bwrapArgsOf(cmd), "--")
	require.Greater(t, sepIdx, 0)
	assert.Contains(t, bwrapArgsOf(cmd)[:sepIdx], "--clearenv")
	assert.Equal(t, "--clearenv", bwrapArgsOf(cmd)[1], "--clearenv must come first")

	all := strings.Join(bwrapArgsOf(cmd), "\x00") + "\x00" + strings.Join(cmd.Env, "\x00")
	for _, leaked := range []string{
		"ETCD_ENDPOINTS", "etcd.internal", "ETCD_PREFIX", "ASGARD_RUNNER_TOKEN",
		"runner-secret-token", "FAKE_RUNNER_SECRET", "s3cr3t-value", "ASGARD_ENCRYPTION_KEY", "enc-secret",
	} {
		assert.NotContains(t, all, leaked)
	}
	for _, kv := range cmd.Env {
		k, _, _ := strings.Cut(kv, "=")
		assert.Contains(t, []string{"PATH", "HOME"}, k, "unexpected parent env entry %q", kv)
	}

	joined := strings.Join(bwrapArgsOf(cmd)[:sepIdx], " ")
	assert.Contains(t, joined, "--setenv LANG en_US.UTF-8")
	assert.Contains(t, joined, "--setenv LC_ALL C.UTF-8")
	assert.Contains(t, joined, "--setenv TERM xterm-256color")
	assert.Contains(t, joined, "--setenv USER runneruser")
	assert.Contains(t, joined, "--setenv HOME ")
}

func TestCommandForAgent_ClearsRunnerEnv(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	setRunnerSecrets(t)

	target := agentspec.CLITarget{CLI: "claude", Model: "sonnet"}
	cmd, err := CommandForAgent(nil, "", target, "prompt", optional.None[string](), tmpDir, "sock", "chat-env", "", "")
	require.NoError(t, err)
	assertCleanSandboxCmd(t, cmd)
	// per-CLI ExtraEnv still arrives
	assert.Contains(t, strings.Join(bwrapArgsOf(cmd), " "), "--setenv DISABLE_AUTOUPDATER 1")
}

func TestCommandForCommandExec_ClearsRunnerEnv(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	setRunnerSecrets(t)

	runDir := filepath.Join(tmpDir, "rundir")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	cmd, err := CommandForCommandExec(runDir, "sock", "chat-env", "", false)
	require.NoError(t, err)
	assertCleanSandboxCmd(t, cmd)
}

// lastSetenv returns the value bwrap ends up with for key (last --setenv wins).
func lastSetenv(args []string, key string) (string, bool) {
	val, found := "", false
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--setenv" && args[i+1] == key {
			val, found = args[i+2], true
		}
	}
	return val, found
}

func TestExtraEnv_ReachesSandboxAndCannotOverrideProtected(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HTTP_PROXY", "http://corp-proxy:3128")

	runDir := filepath.Join(tmpDir, "rundir")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	proxyCfg := ProxySandboxConfig{
		Enabled:   true,
		ProxyAddr: "http://127.0.0.1:8082",
		CACert:    filepath.Join(tmpDir, "ca.pem"),
		ExtraEnv: []string{
			"ASGARD_CHAT_ID=chat-1", "LANG=--", "HTTP_PROXY=http://evil:1", "SSL_CERT_FILE=/evil",
			"ASGARD_SANDBOXED=0", "PATH=/evil", "HOME=/evil", "bogus", "=x",
		},
	}
	require.NoError(t, os.WriteFile(proxyCfg.CACert, []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"), 0o600))

	cmd, err := CommandForCommandExec(runDir, "sock", "chat-extra", "", false, proxyCfg)
	require.NoError(t, err)
	require.Equal(t, "/bin/fakebashd", bwrapArgsOf(cmd)[len(bwrapArgsOf(cmd))-1])
	sepIdx := len(bwrapArgsOf(cmd)) - 2
	require.Equal(t, "--", bwrapArgsOf(cmd)[sepIdx])
	args := bwrapArgsOf(cmd)[:sepIdx]

	v, ok := lastSetenv(args, "ASGARD_CHAT_ID")
	assert.True(t, ok)
	assert.Equal(t, "chat-1", v)
	v, _ = lastSetenv(args, "LANG")
	assert.Equal(t, "--", v, "a value equal to the separator must not confuse the builder")

	v, _ = lastSetenv(args, "HTTP_PROXY")
	assert.Equal(t, "http://127.0.0.1:8082", v, "builder proxy value must win")
	v, _ = lastSetenv(args, "SSL_CERT_FILE")
	assert.Equal(t, "/etc/ssl/certs/ca-certificates.crt", v)
	v, _ = lastSetenv(args, SandboxedEnv)
	assert.Equal(t, "1", v)
	v, _ = lastSetenv(args, "PATH")
	assert.Equal(t, "/usr/bin:/bin", v)
	v, _ = lastSetenv(args, "HOME")
	assert.Equal(t, tmpDir, v)
	_, ok = lastSetenv(args, "bogus")
	assert.False(t, ok)
}

func TestUpstreamProxyEnvInherited(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("HTTPS_PROXY", "http://corp-proxy:3128")
	t.Setenv("no_proxy", "internal.example")
	t.Setenv("SSL_CERT_FILE", "/corp/ca.pem")
	t.Setenv("SSL_CERT_DIR", "/corp/certs")
	t.Setenv("NODE_EXTRA_CA_CERTS", "/corp/node-ca.pem")
	t.Setenv("REQUESTS_CA_BUNDLE", "/corp/req-ca.pem")
	setRunnerSecrets(t)

	cmd, err := CommandForAgent(nil, "", agentspec.CLITarget{CLI: "claude", Model: "sonnet"}, "p", optional.None[string](), tmpDir, "sock", "chat-up", "", "")
	require.NoError(t, err)
	args := bwrapArgsOf(cmd)[:slices.Index(bwrapArgsOf(cmd), "--")]
	for k, want := range map[string]string{
		"HTTPS_PROXY": "http://corp-proxy:3128", "no_proxy": "internal.example",
		"SSL_CERT_FILE": "/corp/ca.pem", "SSL_CERT_DIR": "/corp/certs",
		"NODE_EXTRA_CA_CERTS": "/corp/node-ca.pem", "REQUESTS_CA_BUNDLE": "/corp/req-ca.pem",
	} {
		got, ok := lastSetenv(args, k)
		assert.True(t, ok, k)
		assert.Equal(t, want, got, k)
	}
}

func TestCommandForConstructors_MinimalParentEnv(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("PATH", "/usr/bin:/bin")
	setRunnerSecrets(t)
	runDir := filepath.Join(tmpDir, "rundir")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	repo := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))

	build := map[string]func() (*exec.Cmd, error){
		"agent": func() (*exec.Cmd, error) {
			return CommandForAgent(nil, "", agentspec.CLITarget{CLI: "claude", Model: "sonnet"}, "p", optional.None[string](), runDir, "sock", "chat-m1", "", "")
		},
		"command-exec": func() (*exec.Cmd, error) {
			return CommandForCommandExec(runDir, "sock", "chat-m2", "", false)
		},
		"command-exec-at": func() (*exec.Cmd, error) {
			return CommandForCommandExecAt(runDir, "", "sock", "chat-m3", "", false, nil)
		},
		"agent-command-exec": func() (*exec.Cmd, error) {
			return CommandForAgentCommandExec(nil, runDir, "", "sock", "chat-m4", "", false)
		},
		"query": func() (*exec.Cmd, error) {
			cmd, cleanup, err := CommandForQuery(context.Background(), "models")
			if cleanup != nil {
				t.Cleanup(cleanup)
			}
			return cmd, err
		},
		"git": func() (*exec.Cmd, error) {
			return CommandForGit(context.Background(), GitSandboxSpec{ChatID: "chat-m5", Git: []string{"status"}})
		},
	}
	for name, mk := range build {
		t.Run(name, func(t *testing.T) {
			cmd, err := mk()
			require.NoError(t, err)
			require.NotNil(t, cmd.Env)
			assert.True(t, isBwrapArgsCmd(cmd), "unexpected argv prefix: %v", cmd.Args)
			assert.ElementsMatch(t, []string{"PATH=/usr/bin:/bin", "HOME=" + tmpDir}, cmd.Env)
		})
	}
}

func TestFilterSandboxEnv(t *testing.T) {
	got := FilterSandboxEnv([]string{
		"PATH=/bin", "ETCD_ENDPOINTS=x", "GEMINI_API_KEY=k", "TYPESAFE_API_KEY=k",
		"ASGARD_RUNNER_FOO=1", "ASGARD_ENCRYPTION_KEY=k", "LANG=C", "LC_CTYPE=C",
		"ASGARD_CHAT_ID=c", "ASGARD_TMP_DIR=/tmp", "ASGARD_RUN_TOKEN=t", "noequals",
		"LC_FOO=x", "LC_TELEPHONE=1", "HTTPS_PROXY=http://p", "SSL_CERT_DIR=/d",
	})
	assert.ElementsMatch(t, []string{"LANG=C", "LC_CTYPE=C", "ASGARD_CHAT_ID=c", "ASGARD_TMP_DIR=/tmp", "ASGARD_RUN_TOKEN=t", "LC_TELEPHONE=1", "HTTPS_PROXY=http://p", "SSL_CERT_DIR=/d"}, got)
}

func TestSetenvValuesAreNotOnTheCommandLine(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	cfg := ProxySandboxConfig{ExtraEnv: []string{"ASGARD_RUN_TOKEN=tok-s3cret", "ASGARD_CHAT_ID=chat-a"}}
	cmd, err := CommandForAgent(nil, "", agentspec.CLITarget{CLI: "claude", Model: "sonnet"}, "p", optional.None[string](), tmpDir, "sock", "chat-a", "", "", cfg)
	require.NoError(t, err)

	assert.NotContains(t, strings.Join(cmd.Args, "\x00"), "tok-s3cret")
	assert.NotContains(t, cmd.Args, "--setenv")
	assert.Equal(t, []string{"--clearenv", "--args", "3"}, cmd.Args[1:4])
	require.Len(t, cmd.ExtraFiles, 1)
	v, ok := lastSetenv(bwrapArgsOf(cmd), "ASGARD_RUN_TOKEN")
	assert.True(t, ok)
	assert.Equal(t, "tok-s3cret", v)
}

func TestAWPathOverridesInherited(t *testing.T) {
	for _, k := range []string{"AW_CONFIG", "OPENCODE_AW_CONFIG", "SIMPLEST_KEY_PATH", "SIMPLEST_CONFIG_PATH", "SIMPLEST_MODELS_PATH"} {
		assert.Contains(t, FilterSandboxEnv([]string{k + "=/x"}), k+"=/x", k)
	}
}

func TestEnvPassthrough(t *testing.T) {
	t.Cleanup(func() { SetEnvPassthrough(nil) })
	SetEnvPassthrough([]string{"GOPROXY", " JAVA_HOME ", "ETCD_ENDPOINTS", "ASGARD_RUNNER_X", "GEMINI_API_KEY", "HOME", "PATH", "HTTPS_PROXY", "BAD=KEY", ""})
	env := []string{"GOPROXY=https://p", "JAVA_HOME=/jdk", "ETCD_ENDPOINTS=e", "ASGARD_RUNNER_X=1", "GEMINI_API_KEY=k", "OTHER=1"}
	assert.Equal(t, []string{"GOPROXY=https://p", "JAVA_HOME=/jdk"}, FilterSandboxEnv(env))

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("GOPROXY", "https://p")
	t.Setenv("OTHER", "1")
	t.Setenv("ETCD_ENDPOINTS", "e")
	cmd, err := CommandForCommandExec(tmpDir, "sock", "chat-pt", "", false)
	require.NoError(t, err)
	args := bwrapArgsOf(cmd)
	v, ok := lastSetenv(args, "GOPROXY")
	assert.True(t, ok)
	assert.Equal(t, "https://p", v)
	_, ok = lastSetenv(args, "OTHER")
	assert.False(t, ok)
	_, ok = lastSetenv(args, "ETCD_ENDPOINTS")
	assert.False(t, ok)

	SetEnvPassthrough(nil)
	_, ok = lastSetenv(bwrapArgsOf(mustCmd(t, tmpDir)), "GOPROXY")
	assert.False(t, ok)
}

func mustCmd(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	cmd, err := CommandForCommandExec(dir, "sock", "chat-pt2", "", false)
	require.NoError(t, err)
	return cmd
}

// TestArgsFileReachesRealBwrap runs bwrap for real: the --setenv values read
// from the --args fd must show up in the sandbox environment.
func TestArgsFileReachesRealBwrap(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap is not installed")
	}
	if err := exec.Command("bwrap", "--ro-bind", "/", "/", "true").Run(); err != nil {
		t.Skipf("bwrap cannot create a sandbox here: %v", err)
	}
	args := []string{"--clearenv", "--ro-bind", "/", "/", "--setenv", "ASGARD_RUN_TOKEN", "tok-s3cret", "--setenv", "A", "b c", "--", "env"}
	cmd, err := newBwrapCmd(t.Context(), args)
	require.NoError(t, err)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "ASGARD_RUN_TOKEN=tok-s3cret")
	assert.Contains(t, string(out), "A=b c")
}

// TestStartCmdClosesArgsFile: the parent's memfd must not outlive the start,
// whether the start succeeds or fails, and CloseArgs is idempotent.
func TestStartCmdClosesArgsFile(t *testing.T) {
	args := []string{"--clearenv", "--setenv", "K", "v", "--", "true"}
	closed := func(f *os.File) bool {
		_, err := f.Stat()
		return errors.Is(err, os.ErrClosed)
	}

	t.Run("started", func(t *testing.T) {
		cmd, err := newBwrapCmd(t.Context(), args)
		require.NoError(t, err)
		f := cmd.ExtraFiles[0]
		assert.False(t, closed(f))
		cmd.Path = "/bin/true" // do not need a working bwrap here
		require.NoError(t, StartCmd(cmd))
		assert.True(t, closed(f))
		require.NoError(t, cmd.Wait())
	})
	t.Run("start fails", func(t *testing.T) {
		cmd, err := newBwrapCmd(t.Context(), args)
		require.NoError(t, err)
		f := cmd.ExtraFiles[0]
		cmd.Path = "/nonexistent/bwrap"
		require.Error(t, StartCmd(cmd))
		assert.True(t, closed(f))
	})
	t.Run("builder error path", func(t *testing.T) {
		cmd, err := newBwrapCmd(t.Context(), args)
		require.NoError(t, err)
		f := cmd.ExtraFiles[0]
		CloseArgs(cmd)
		CloseArgs(cmd) // idempotent
		assert.True(t, closed(f))
	})
	t.Run("foreign command untouched", func(t *testing.T) {
		f, err := os.Open("/dev/null")
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		cmd := exec.Command("git", "status")
		cmd.ExtraFiles = []*os.File{f}
		CloseArgs(cmd)
		assert.False(t, closed(f))
	})
	t.Run("foreign command with --args 3 in its arguments untouched", func(t *testing.T) {
		f, err := os.Open("/dev/null")
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		cmd := exec.Command("git", "log", "--args", "3")
		cmd.ExtraFiles = []*os.File{f}
		CloseArgs(cmd)
		assert.False(t, closed(f))
		cmd = exec.Command("bwrap", "--unshare-all", "--args", "3")
		cmd.ExtraFiles = []*os.File{f}
		CloseArgs(cmd)
		assert.False(t, closed(f))
	})
}
