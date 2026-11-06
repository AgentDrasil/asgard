package sandbox

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/proxy"
	runnerconfig "github.com/AgentDrasil/asgard/runner/lib/config"
)

type fakeSource struct {
	conf  *config.Config
	tree  *config.AsgardTreeConfig
	err   error
	mu    sync.Mutex
	fns   []func(*config.AsgardTreeConfig)
	reads []int
}

func (f *fakeSource) SandboxConfig() (*config.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, len(f.fns)) // callbacks registered at read time
	return f.conf, f.err
}
func (f *fakeSource) OnTreeChange(fn func(*config.AsgardTreeConfig)) {
	f.mu.Lock()
	f.fns = append(f.fns, fn)
	f.mu.Unlock()
}
func (f *fakeSource) Tree() *config.AsgardTreeConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tree
}

func proxyConf(enabled bool) *config.Config {
	c := &config.Config{ConfigPath: "/cfg/config.yaml"}
	if enabled {
		c.SetProxy(&proxy.Config{Enable: true})
	}
	return c
}

func TestSetupProxyDisabled(t *testing.T) {
	t.Parallel()

	sshCalls := 0
	sb := Setup(&fakeSource{conf: proxyConf(false)}, Options{SetupSSH: func() error { sshCalls++; return nil }})
	t.Cleanup(sb.Close)

	require.NoError(t, sb.Ready())
	assert.Equal(t, 1, sshCalls)
	assert.Equal(t, "/cfg/config.yaml", sb.Conf().GetConfigPath())
	assert.False(t, sb.Conf().SandboxProxyOptions().Enabled)
}

func TestSetupRefusesRunsWhenSetupBroken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		src     *fakeSource
		newPx   func(*proxy.Config, string) (*proxy.ProxyManager, error)
		wantErr string
	}{
		{"config missing", &fakeSource{err: errors.New("not loaded")}, nil, "sandbox configuration unavailable"},
		{"proxy init fails", &fakeSource{conf: proxyConf(true)}, func(*proxy.Config, string) (*proxy.ProxyManager, error) { return nil, errors.New("no CA") }, "proxy unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sb := Setup(tt.src, Options{SkipSSH: true, NewProxy: tt.newPx})
			t.Cleanup(sb.Close)
			err := sb.Ready()
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSetupSSHFailureIsNotFatal(t *testing.T) {
	t.Parallel()

	sb := Setup(&fakeSource{conf: proxyConf(false)}, Options{SetupSSH: func() error { return errors.New("no ssh-agent") }})
	t.Cleanup(sb.Close)
	assert.NoError(t, sb.Ready())
}

func validTree(px proxy.Config) *config.AsgardTreeConfig {
	tree := &config.AsgardTreeConfig{}
	tree.Asgard.Config.Host = "127.0.0.1"
	tree.Asgard.Config.DB = "sqlite"
	tree.Asgard.Config.GeminiAPIKey = "k"
	tree.Asgard.Config.GeminiModelForChatTitle = "m"
	tree.Asgard.Proxy = px
	return tree
}

// fireRaw notifies the handlers without changing the source's config.
func (f *fakeSource) fireRaw() {
	f.mu.Lock()
	fns := slices.Clone(f.fns)
	f.mu.Unlock()
	for _, fn := range fns {
		fn(nil)
	}
}

func (f *fakeSource) fire(tree *config.AsgardTreeConfig) {
	conf, err := runnerconfig.SandboxConfigFromTree(tree)
	f.mu.Lock()
	f.conf, f.err, f.tree = conf, err, tree
	f.mu.Unlock()
	f.mu.Lock()
	fns := slices.Clone(f.fns)
	f.mu.Unlock()
	for _, fn := range fns {
		fn(tree)
	}
}

func TestProxyStartFailureSurvivesReloadAndRestarts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })
	px := proxy.Config{
		Enable: true,
		Server: proxy.ServerConfig{Addr: busy.Addr().String(), CACert: filepath.Join(dir, "ca.crt"), CAKey: filepath.Join(dir, "ca.key")},
		Rules:  []proxy.Rule{{Host: "api.example.com", HeaderKey: "Authorization", RealSecret: "real-secret-123456", DummySecret: "dummy"}},
	}
	initial, err := runnerconfig.SandboxConfigFromTree(validTree(px))
	require.NoError(t, err)
	src := &fakeSource{conf: initial}

	sb := Setup(src, Options{SkipSSH: true})
	t.Cleanup(sb.Close)
	require.Error(t, sb.Ready(), "address in use: proxy cannot listen")

	// A successful config reload must not clear the start failure.
	src.fire(validTree(px))
	assert.ErrorContains(t, sb.Ready(), "proxy server stopped")

	// Once the port is free, the next tree change restarts the proxy.
	require.NoError(t, busy.Close())
	src.fire(validTree(px))
	require.Eventually(t, func() bool { return sb.Ready() == nil }, 5*time.Second, 20*time.Millisecond)
}

func TestSetupRegistersChangeHandlerBeforeReading(t *testing.T) {
	t.Parallel()

	src := &fakeSource{conf: proxyConf(false)}
	sb := Setup(src, Options{SkipSSH: true})
	t.Cleanup(sb.Close)

	src.mu.Lock()
	defer src.mu.Unlock()
	require.NotEmpty(t, src.reads)
	assert.Equal(t, 1, src.reads[0], "the handler is registered before the initial snapshot is read")
}

func TestProxyEnableDisableEnable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	px := proxy.Config{
		Enable: true,
		Server: proxy.ServerConfig{Addr: "127.0.0.1:0", CACert: filepath.Join(dir, "ca.crt"), CAKey: filepath.Join(dir, "ca.key")},
		Rules:  []proxy.Rule{{Host: "api.example.com", HeaderKey: "Authorization", RealSecret: "real-secret-123456", DummySecret: "dummy"}},
	}
	initial, err := runnerconfig.SandboxConfigFromTree(validTree(px))
	require.NoError(t, err)
	src := &fakeSource{conf: initial}
	sb := Setup(src, Options{SkipSSH: true})
	t.Cleanup(sb.Close)
	require.NoError(t, sb.Ready())
	first := sb.pm
	require.NotNil(t, first)
	require.True(t, first.Running())

	// Disable: the proxy stops and no-proxy is a healthy state.
	src.fire(validTree(proxy.Config{}))
	require.NoError(t, sb.Ready())
	assert.False(t, first.Running())
	assert.Nil(t, sb.pm)
	assert.False(t, sb.Conf().SandboxProxyOptions().Enabled)

	// Re-enable: a fresh manager is created and started.
	src.fire(validTree(px))
	require.NoError(t, sb.Ready())
	require.NotNil(t, sb.pm)
	assert.NotSame(t, first, sb.pm)
	assert.True(t, sb.pm.Running())
}

func TestProxyInitFailureClearedByDisable(t *testing.T) {
	t.Parallel()

	src := &fakeSource{conf: proxyConf(true)}
	sb := Setup(src, Options{SkipSSH: true, NewProxy: func(*proxy.Config, string) (*proxy.ProxyManager, error) {
		return nil, errors.New("no CA")
	}})
	t.Cleanup(sb.Close)
	require.Error(t, sb.Ready())

	src.mu.Lock()
	src.conf = proxyConf(false)
	src.mu.Unlock()
	src.fireRaw()
	assert.NoError(t, sb.Ready(), "a disabled proxy forgets the earlier init failure")
}

func TestCloseIsIdempotentAndIgnoresLateChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	px := proxy.Config{
		Enable: true,
		Server: proxy.ServerConfig{Addr: "127.0.0.1:0", CACert: filepath.Join(dir, "ca.crt"), CAKey: filepath.Join(dir, "ca.key")},
		Rules:  []proxy.Rule{{Host: "api.example.com", HeaderKey: "Authorization", RealSecret: "real-secret-123456", DummySecret: "dummy"}},
	}
	initial, err := runnerconfig.SandboxConfigFromTree(validTree(px))
	require.NoError(t, err)
	src := &fakeSource{conf: initial}
	created := 0
	sb := Setup(src, Options{SkipSSH: true, NewProxy: func(cfg *proxy.Config, path string) (*proxy.ProxyManager, error) {
		created++
		return proxy.NewManager(cfg, path)
	}})
	pm := sb.pm
	require.NotNil(t, pm)
	require.True(t, pm.Running())

	sb.Close()
	sb.Close() // no double shutdown
	assert.False(t, pm.Running())
	assert.Nil(t, sb.pm)

	// A late tree change neither starts nor reloads anything.
	src.fire(validTree(px))
	assert.Nil(t, sb.pm)
	assert.Equal(t, 1, created)
}

func TestSyncCLICredentials_NilCredsAndNoAgyClaudeFiles(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	require.NoError(t, SyncCLICredentials(tempHome, nil))

	// agy/claude credentials are runner-local accounts now; sync must not
	// create or touch ~/.gemini or ~/.claude.
	require.NoError(t, SyncCLICredentials(tempHome, &config.CredentialsScope{}))
	_, err := os.Stat(filepath.Join(tempHome, ".gemini"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(tempHome, ".claude"))
	assert.True(t, os.IsNotExist(err))
}

func TestSyncCLICredentials_LeavesLegacyHostFilesAlone(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	agy := filepath.Join(tempHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	claude := filepath.Join(tempHome, ".claude", ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(agy), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Dir(claude), 0o700))
	require.NoError(t, os.WriteFile(agy, []byte("manual"), 0o600))
	require.NoError(t, os.WriteFile(claude, []byte("{}"), 0o600))

	require.NoError(t, SyncCLICredentials(tempHome, &config.CredentialsScope{}))

	b, err := os.ReadFile(agy)
	require.NoError(t, err)
	assert.Equal(t, "manual", string(b))
	_, err = os.Stat(claude)
	assert.NoError(t, err)
}

func TestSyncCLICredentials_GCPSAKey(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	saPath := filepath.Join(tempHome, ".config", "gcp-sa.json")

	// 1. Sync GCP SA key
	creds := &config.CredentialsScope{
		GCPSAKey: []byte(`{"project_id":"test-sa-proj"}`),
	}
	err := SyncCLICredentials(tempHome, creds)
	require.NoError(t, err)

	data, err := os.ReadFile(saPath)
	require.NoError(t, err)
	assert.Equal(t, `{"project_id":"test-sa-proj"}`, string(data))

	info, err := os.Stat(saPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	dirInfo, err := os.Stat(filepath.Join(tempHome, ".config"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), dirInfo.Mode().Perm())

	// 2. Permission tightening
	require.NoError(t, os.Chmod(saPath, 0644))
	err = SyncCLICredentials(tempHome, creds)
	require.NoError(t, err)
	info, err = os.Stat(saPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// 3. Revocation: empty key removes file
	emptyCreds := &config.CredentialsScope{}
	err = SyncCLICredentials(tempHome, emptyCreds)
	require.NoError(t, err)
	_, err = os.Stat(saPath)
	assert.True(t, os.IsNotExist(err), "gcp-sa.json must be removed on revocation")
}

func TestSyncOpencodeAW(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	awDir := filepath.Join(tempHome, ".config", "aw")
	awPath := filepath.Join(awDir, "opencode.aw.yaml")

	// 1. Initial write
	initialContent := []byte("env:\n  OPENCODE_ZAI_API_KEY: \"sk-test-1\"\n")
	require.NoError(t, SyncOpencodeAW(tempHome, initialContent))

	data, err := os.ReadFile(awPath)
	require.NoError(t, err)
	assert.Equal(t, initialContent, data)

	fi, err := os.Stat(awPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())

	dirFi, err := os.Stat(awDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), dirFi.Mode().Perm())

	// 2. Update content
	updatedContent := []byte("env:\n  OPENCODE_ZAI_API_KEY: \"sk-test-2\"\n")
	require.NoError(t, SyncOpencodeAW(tempHome, updatedContent))

	data, err = os.ReadFile(awPath)
	require.NoError(t, err)
	assert.Equal(t, updatedContent, data)

	// 3. Permission tightening when unchanged
	require.NoError(t, os.Chmod(awPath, 0644))
	require.NoError(t, SyncOpencodeAW(tempHome, updatedContent))

	fi, err = os.Stat(awPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())

	// 4. Revocation / removal on empty content
	require.NoError(t, SyncOpencodeAW(tempHome, nil))
	_, err = os.Stat(awPath)
	assert.True(t, os.IsNotExist(err), "opencode.aw.yaml must be removed when content is empty")

	// 5. Calling with nil when file already does not exist shouldn't fail
	require.NoError(t, SyncOpencodeAW(tempHome, []byte{}))
}

func TestSyncSimplestKey(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	simplestDir := filepath.Join(tempHome, ".config", "simplest")
	keyPath := filepath.Join(simplestDir, "key.yaml")

	// 1. Initial write
	initialContent := []byte("openai: \"sk-test-1\"\n")
	require.NoError(t, SyncSimplestKey(tempHome, initialContent))

	data, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, initialContent, data)

	fi, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())

	dirFi, err := os.Stat(simplestDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), dirFi.Mode().Perm())

	// 2. Update content
	updatedContent := []byte("openai: \"sk-test-2\"\n")
	require.NoError(t, SyncSimplestKey(tempHome, updatedContent))

	data, err = os.ReadFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, updatedContent, data)

	// 3. Permission tightening when unchanged
	require.NoError(t, os.Chmod(keyPath, 0644))
	require.NoError(t, SyncSimplestKey(tempHome, updatedContent))

	fi, err = os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())

	// 4. Revocation / removal on empty content
	require.NoError(t, SyncSimplestKey(tempHome, nil))
	_, err = os.Stat(keyPath)
	assert.True(t, os.IsNotExist(err), "simplest key.yaml must be removed when content is empty")

	// 5. Calling with nil when file already does not exist shouldn't fail
	require.NoError(t, SyncSimplestKey(tempHome, []byte{}))
}

func TestSyncSSHCredentials_OnTreeChange(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()

	tree := validTree(proxy.Config{})
	tree.Credentials.SSH = sshScopeOf(map[string]string{"key": "-----BEGIN X-----\ninitial-ssh-key", "key.pub": "initial-ssh-pub", "config": "initial-ssh-config"})

	initialConf, err := runnerconfig.SandboxConfigFromTree(tree)
	require.NoError(t, err)

	src := &fakeSource{conf: initialConf, tree: tree}

	var setupSSHCount int
	var clearSSHCount int
	var mu sync.Mutex
	sb := Setup(src, Options{
		SkipSSH: false,
		HomeDir: tempHome,
		SetupSSH: func() error {
			mu.Lock()
			setupSSHCount++
			mu.Unlock()
			return nil
		},
		ClearSSH: func() error {
			mu.Lock()
			clearSSHCount++
			mu.Unlock()
			return nil
		},
	})
	t.Cleanup(sb.Close)

	require.NoError(t, sb.Ready())

	sshDir := filepath.Join(tempHome, ".ssh")
	keyPath := filepath.Join(sshDir, "key")
	pubPath := filepath.Join(sshDir, "key.pub")
	cfgPath := filepath.Join(sshDir, "config")

	// Verify ~/.ssh directory permissions (0700)
	dirStat, err := os.Stat(sshDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), dirStat.Mode().Perm())

	// Verify key (0600)
	keyBytes, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, "-----BEGIN X-----\ninitial-ssh-key", string(keyBytes))
	keyStat, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), keyStat.Mode().Perm())

	// Verify key.pub (0644)
	pubBytes, err := os.ReadFile(pubPath)
	require.NoError(t, err)
	assert.Equal(t, "initial-ssh-pub", string(pubBytes))
	pubStat, err := os.Stat(pubPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), pubStat.Mode().Perm())

	// Verify config (0644)
	cfgBytes, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "initial-ssh-config", string(cfgBytes))
	cfgStat, err := os.Stat(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), cfgStat.Mode().Perm())

	// SetupSSH should have been called on startup and after credential sync
	mu.Lock()
	assert.Equal(t, 2, setupSSHCount, "SetupSSH should be called on startup and initial credential sync")
	assert.Equal(t, 0, clearSSHCount)
	mu.Unlock()

	// Unrelated tree change: SSH credentials did not change -> SetupSSH must NOT be called again
	unrelatedTree := validTree(proxy.Config{})
	unrelatedTree.Credentials.SSH = tree.Credentials.SSH
	src.fire(unrelatedTree)
	mu.Lock()
	assert.Equal(t, 2, setupSSHCount, "SetupSSH must not be called when SSH credentials did not change")
	assert.Equal(t, 0, clearSSHCount)
	mu.Unlock()

	// Update credentials
	updatedTree := validTree(proxy.Config{})
	updatedTree.Credentials.SSH = sshScopeOf(map[string]string{"key": "-----BEGIN X-----\nupdated-ssh-key", "key.pub": "updated-ssh-pub", "config": "updated-ssh-config"})

	src.fire(updatedTree)

	keyBytes, err = os.ReadFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, "-----BEGIN X-----\nupdated-ssh-key", string(keyBytes))

	pubBytes, err = os.ReadFile(pubPath)
	require.NoError(t, err)
	assert.Equal(t, "updated-ssh-pub", string(pubBytes))

	cfgBytes, err = os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "updated-ssh-config", string(cfgBytes))

	mu.Lock()
	assert.Equal(t, 3, setupSSHCount, "SetupSSH should be called again on credential update")
	assert.Equal(t, 0, clearSSHCount)
	mu.Unlock()

	// Clear/revoke credentials
	clearedTree := validTree(proxy.Config{})
	clearedTree.Credentials.SSH = config.SSHScope{}

	src.fire(clearedTree)

	_, err = os.Stat(keyPath)
	assert.True(t, os.IsNotExist(err), "key must be removed when revoked")

	_, err = os.Stat(pubPath)
	assert.True(t, os.IsNotExist(err), "key.pub must be removed when revoked")

	_, err = os.Stat(cfgPath)
	assert.True(t, os.IsNotExist(err), "config must be removed when revoked")

	mu.Lock()
	assert.Equal(t, 3, setupSSHCount)
	assert.Equal(t, 1, clearSSHCount, "ClearSSH must be called to evict keys on revocation")
	mu.Unlock()

	// Subsequent tree change while empty: ClearSSH must NOT be called again
	anotherClearedTree := validTree(proxy.Config{})
	anotherClearedTree.Credentials.SSH = config.SSHScope{}
	src.fire(anotherClearedTree)

	mu.Lock()
	assert.Equal(t, 3, setupSSHCount)
	assert.Equal(t, 1, clearSSHCount, "ClearSSH must not be called continuously while credentials remain empty")
	mu.Unlock()
}

func TestSyncSSHCredentials_StartupWithEmptySSHDoesNotEvict(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()

	tree := validTree(proxy.Config{})
	tree.Credentials.SSH = config.SSHScope{} // No SSH credentials

	initialConf, err := runnerconfig.SandboxConfigFromTree(tree)
	require.NoError(t, err)

	src := &fakeSource{conf: initialConf, tree: tree}

	var clearSSHCount int
	var mu sync.Mutex
	sb := Setup(src, Options{
		SkipSSH: false,
		HomeDir: tempHome,
		ClearSSH: func() error {
			mu.Lock()
			clearSSHCount++
			mu.Unlock()
			return nil
		},
	})
	t.Cleanup(sb.Close)

	require.NoError(t, sb.Ready())

	// ClearSSH should NOT be called on startup when etcd never had an SSH key
	mu.Lock()
	assert.Equal(t, 0, clearSSHCount, "ClearSSH must not be called on startup when no SSH credentials exist")
	mu.Unlock()

	// Unrelated change with no SSH credentials should not call ClearSSH
	src.fire(tree)
	mu.Lock()
	assert.Equal(t, 0, clearSSHCount, "ClearSSH must not be called on subsequent tree updates when no SSH credentials exist")
	mu.Unlock()
}

func TestSyncSSHCredentials_RevocationAndPermissionTightening(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	sshDir := filepath.Join(tempHome, ".ssh")

	// Pre-create ~/.ssh with loose permissions 0755
	require.NoError(t, os.MkdirAll(sshDir, 0755))
	require.NoError(t, os.Chmod(sshDir, 0755))

	// Write pre-existing files with different permissions
	keyPath := filepath.Join(sshDir, "key")
	pubPath := filepath.Join(sshDir, "key.pub")
	cfgPath := filepath.Join(sshDir, "config")
	require.NoError(t, os.WriteFile(keyPath, []byte("old-key"), 0644))
	require.NoError(t, os.WriteFile(pubPath, []byte("old-pub"), 0600))
	require.NoError(t, os.WriteFile(cfgPath, []byte("old-cfg"), 0600))

	// Sync with updated credentials
	sshv := sshScopeOf(map[string]string{"key": "-----BEGIN X-----\nnew-key", "key.pub": "new-pub", "config": "new-cfg"})
	ssh := &sshv
	require.NoError(t, SyncSSHCredentials(tempHome, ssh))

	// Verify permissions tightened to 0700 on ~/.ssh, 0600 on key, 0644 on pub/config
	dirStat, err := os.Stat(sshDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), dirStat.Mode().Perm())

	keyStat, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), keyStat.Mode().Perm())

	pubStat, err := os.Stat(pubPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), pubStat.Mode().Perm())

	cfgStat, err := os.Stat(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), cfgStat.Mode().Perm())

	// Clobber prevention: sync with matching content: files must not be rewritten
	past := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(keyPath, past, past))
	require.NoError(t, os.Chtimes(pubPath, past, past))
	require.NoError(t, os.Chtimes(cfgPath, past, past))

	require.NoError(t, SyncSSHCredentials(tempHome, ssh))

	keyStatAfter, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, past.Unix(), keyStatAfter.ModTime().Unix(), "identical key content must not be rewritten")

	pubStatAfter, err := os.Stat(pubPath)
	require.NoError(t, err)
	assert.Equal(t, past.Unix(), pubStatAfter.ModTime().Unix(), "identical pub content must not be rewritten")

	cfgStatAfter, err := os.Stat(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, past.Unix(), cfgStatAfter.ModTime().Unix(), "identical config content must not be rewritten")

	// Revoke: sync with nil and empty SSHScope
	require.NoError(t, SyncSSHCredentials(tempHome, &config.SSHScope{}))

	_, err = os.Stat(keyPath)
	assert.True(t, os.IsNotExist(err))

	_, err = os.Stat(pubPath)
	assert.True(t, os.IsNotExist(err))

	_, err = os.Stat(cfgPath)
	assert.True(t, os.IsNotExist(err))

	// Re-create and test revocation with nil ssh
	require.NoError(t, SyncSSHCredentials(tempHome, ssh))
	_, err = os.Stat(keyPath)
	require.NoError(t, err)

	require.NoError(t, SyncSSHCredentials(tempHome, nil))
	_, err = os.Stat(keyPath)
	assert.True(t, os.IsNotExist(err))
}

func TestSyncSSHCredentials_MultiKey(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	sshDir := filepath.Join(tempHome, ".ssh")

	sshv := sshScopeOf(map[string]string{
		"id_ed25519": "-----BEGIN X-----\ned", "id_ed25519.pub": "ed25519-pub",
		"id_rsa": "-----BEGIN X-----\nrsa", "id_rsa.pub": "rsa-pub",
		"id_ecdsa": "-----BEGIN X-----\nec", "id_ecdsa.pub": "ecdsa-pub",
		"id_dsa": "-----BEGIN X-----\ndsa", "id_dsa.pub": "dsa-pub",
		"config": "Host *", "known_hosts": "github.com ssh-rsa ...",
	})
	ssh := &sshv

	require.NoError(t, SyncSSHCredentials(tempHome, ssh))

	dirStat, err := os.Stat(sshDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), dirStat.Mode().Perm())

	// Private keys must be 0600
	for _, priv := range []string{"id_ed25519", "id_rsa", "id_ecdsa", "id_dsa"} {
		p := filepath.Join(sshDir, priv)
		stat, err := os.Stat(p)
		require.NoError(t, err, "missing %s", priv)
		assert.Equal(t, os.FileMode(0600), stat.Mode().Perm(), "%s must be 0600", priv)
	}

	// Public keys, config, known_hosts must be 0644
	for _, pub := range []string{"id_ed25519.pub", "id_rsa.pub", "id_ecdsa.pub", "id_dsa.pub", "config", "known_hosts"} {
		p := filepath.Join(sshDir, pub)
		stat, err := os.Stat(p)
		require.NoError(t, err, "missing %s", pub)
		assert.Equal(t, os.FileMode(0644), stat.Mode().Perm(), "%s must be 0644", pub)
	}

	// Remove some keys (e.g. keep only id_ed25519 and config)
	subsetv := sshScopeOf(map[string]string{"id_ed25519": "-----BEGIN X-----\ned", "id_ed25519.pub": "ed25519-pub", "config": "Host *"})
	subsetSSH := &subsetv
	require.NoError(t, SyncSSHCredentials(tempHome, subsetSSH))

	// id_ed25519 and config should still exist
	_, err = os.Stat(filepath.Join(sshDir, "id_ed25519"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(sshDir, "config"))
	require.NoError(t, err)

	// Deleted managed files should be gone
	for _, deleted := range []string{"id_rsa", "id_rsa.pub", "id_ecdsa", "id_ecdsa.pub", "id_dsa", "id_dsa.pub", "known_hosts"} {
		p := filepath.Join(sshDir, deleted)
		_, err = os.Stat(p)
		assert.True(t, os.IsNotExist(err), "%s should have been removed", deleted)
	}

	// Complete revocation
	require.NoError(t, SyncSSHCredentials(tempHome, &config.SSHScope{}))
	for _, m := range []string{"id_ed25519", "id_ed25519.pub", "config", "id_rsa", "known_hosts"} {
		p := filepath.Join(sshDir, m)
		_, err = os.Stat(p)
		assert.True(t, os.IsNotExist(err), "%s should have been removed on revocation", m)
	}
}

func TestSyncSSHCredentials_MultiKeyOnTreeChange(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()

	tree := validTree(proxy.Config{})
	tree.Credentials.SSH = sshScopeOf(map[string]string{"id_ed25519": "-----BEGIN X-----\ninitial-ed25519-priv", "id_ed25519.pub": "initial-ed25519-pub"})

	initialConf, err := runnerconfig.SandboxConfigFromTree(tree)
	require.NoError(t, err)

	src := &fakeSource{conf: initialConf, tree: tree}

	var setupSSHCount int
	var clearSSHCount int
	var mu sync.Mutex
	sb := Setup(src, Options{
		SkipSSH: false,
		HomeDir: tempHome,
		SetupSSH: func() error {
			mu.Lock()
			setupSSHCount++
			mu.Unlock()
			return nil
		},
		ClearSSH: func() error {
			mu.Lock()
			clearSSHCount++
			mu.Unlock()
			return nil
		},
	})
	t.Cleanup(sb.Close)

	require.NoError(t, sb.Ready())

	mu.Lock()
	assert.Equal(t, 2, setupSSHCount, "SetupSSH called on startup and initial credential sync")
	assert.Equal(t, 0, clearSSHCount)
	mu.Unlock()

	// Add an RSA key alongside ed25519
	updatedTree := validTree(proxy.Config{})
	updatedTree.Credentials.SSH = sshScopeOf(map[string]string{
		"id_ed25519": "-----BEGIN X-----\ninitial-ed25519-priv", "id_ed25519.pub": "initial-ed25519-pub",
		"id_rsa": "-----BEGIN X-----\nadded-rsa-priv", "id_rsa.pub": "added-rsa-pub",
	})

	src.fire(updatedTree)

	mu.Lock()
	assert.Equal(t, 3, setupSSHCount, "SetupSSH should be called when private key added")
	assert.Equal(t, 0, clearSSHCount)
	mu.Unlock()

	// Verify both exist on disk
	sshDir := filepath.Join(tempHome, ".ssh")
	_, err = os.Stat(filepath.Join(sshDir, "id_ed25519"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(sshDir, "id_rsa"))
	require.NoError(t, err)

	// Revoke all private keys
	revokedTree := validTree(proxy.Config{})
	revokedTree.Credentials.SSH = config.SSHScope{}

	src.fire(revokedTree)

	mu.Lock()
	assert.Equal(t, 3, setupSSHCount)
	assert.Equal(t, 1, clearSSHCount, "ClearSSH must be called when all private keys are revoked")
	mu.Unlock()

	// Files removed
	_, err = os.Stat(filepath.Join(sshDir, "id_ed25519"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(sshDir, "id_rsa"))
	assert.True(t, os.IsNotExist(err))
}

func sshScopeOf(files map[string]string) config.SSHScope {
	s := config.SSHScope{Files: map[string]config.SSHFile{}}
	for k, v := range files {
		s.Files[k] = config.SSHFile{Data: []byte(v)}
	}
	return s
}

func TestSyncSSHCredentials_MultiKeyAndRevocation(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "user_own"), []byte("mine"), 0600))

	pem := func(n string) string { return "-----BEGIN OPENSSH PRIVATE KEY-----\n" + n }
	scope := sshScopeOf(map[string]string{
		"huangyu": pem("a"), "huangyu.pub": "pub-a", "work": pem("b"), "config": "Host *",
	})
	require.NoError(t, SyncSSHCredentials(home, &scope))

	mode := func(name string) os.FileMode {
		fi, err := os.Stat(filepath.Join(sshDir, name))
		require.NoError(t, err)
		return fi.Mode().Perm()
	}
	assert.Equal(t, os.FileMode(0600), mode("huangyu"))
	assert.Equal(t, os.FileMode(0600), mode("work"))
	assert.Equal(t, os.FileMode(0644), mode("huangyu.pub"))
	assert.Equal(t, os.FileMode(0644), mode("config"))

	// Removing one key deletes only that file
	scope2 := sshScopeOf(map[string]string{"huangyu": pem("a"), "huangyu.pub": "pub-a", "config": "Host *"})
	require.NoError(t, SyncSSHCredentials(home, &scope2))
	assert.NoFileExists(t, filepath.Join(sshDir, "work"))
	assert.FileExists(t, filepath.Join(sshDir, "huangyu"))
	assert.FileExists(t, filepath.Join(sshDir, "user_own"))

	// Revocation clears managed files, leaves unrelated ones
	require.NoError(t, SyncSSHCredentials(home, &config.SSHScope{}))
	for _, n := range []string{"huangyu", "huangyu.pub", "config"} {
		assert.NoFileExists(t, filepath.Join(sshDir, n))
	}
	assert.FileExists(t, filepath.Join(sshDir, "user_own"))
}

func TestSyncSSHCredentials_ReservedAndUnsafeNamesNotSynced(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	pem := "-----BEGIN X-----\nk"
	scope := sshScopeOf(map[string]string{
		"good": pem, config.SSHManifestName: "evil", "evil\nvictim": pem, "padded ": pem,
	})
	require.NoError(t, SyncSSHCredentials(home, &scope))
	entries, err := os.ReadDir(sshDir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"good", config.SSHManifestName}, names)
	manifest, err := os.ReadFile(filepath.Join(sshDir, config.SSHManifestName))
	require.NoError(t, err)
	assert.Equal(t, "good\n", string(manifest))
}

func TestSyncSSHCredentials_LegacyUpgradeSweepOnce(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0700))
	// Pre-manifest install left a legacy key on disk.
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("old"), 0600))

	scope := sshScopeOf(map[string]string{"huangyu": "-----BEGIN X-----\nk"})
	require.NoError(t, SyncSSHCredentials(home, &scope))
	assert.NoFileExists(t, filepath.Join(sshDir, "id_rsa"), "legacy file swept on upgrade")

	// Once a manifest exists, user-placed legacy-name files survive.
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("user"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte("user"), 0644))
	require.NoError(t, SyncSSHCredentials(home, &scope))
	assert.FileExists(t, filepath.Join(sshDir, "id_rsa"))
	assert.FileExists(t, filepath.Join(sshDir, "known_hosts"))

	// ...also across revocation.
	require.NoError(t, SyncSSHCredentials(home, &config.SSHScope{}))
	assert.NoFileExists(t, filepath.Join(sshDir, "huangyu"))
	assert.FileExists(t, filepath.Join(sshDir, "id_rsa"))
	assert.FileExists(t, filepath.Join(sshDir, "known_hosts"))
}

func TestSyncSSHCredentials_PerKeyRemovalRevokesFromAgent(t *testing.T) {
	t.Parallel()
	tempHome := t.TempDir()
	pem := "-----BEGIN X-----\n"
	tree := validTree(proxy.Config{})
	tree.Credentials.SSH = sshScopeOf(map[string]string{"a": pem + "a", "a.pub": "pa", "b": pem + "b"})
	conf, err := runnerconfig.SandboxConfigFromTree(tree)
	require.NoError(t, err)
	src := &fakeSource{conf: conf, tree: tree}

	var mu sync.Mutex
	var removed []string
	sb := Setup(src, Options{
		HomeDir:  tempHome,
		SetupSSH: func() error { return nil },
		ClearSSH: func() error { return nil },
		RemoveSSHKey: func(p string) error {
			mu.Lock()
			removed = append(removed, filepath.Base(p))
			mu.Unlock()
			return nil
		},
	})
	t.Cleanup(sb.Close)
	require.NoError(t, sb.Ready())

	next := validTree(proxy.Config{})
	next.Credentials.SSH = sshScopeOf(map[string]string{"b": pem + "b"})
	src.fire(next)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"a"}, removed)
	assert.NoFileExists(t, filepath.Join(tempHome, ".ssh", "a"))
	assert.NoFileExists(t, filepath.Join(tempHome, ".ssh", "a.pub"))
	assert.FileExists(t, filepath.Join(tempHome, ".ssh", "b"))
}

func TestSyncSSHCredentials_RotationRevokesBeforeOverwrite(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	target := filepath.Join(home, ".ssh", "k")
	old := sshScopeOf(map[string]string{"k": "-----BEGIN X-----\nold"})
	require.NoError(t, SyncSSHCredentials(home, &old))

	var seen []string
	hook := func(p string) {
		b, err := os.ReadFile(p)
		require.NoError(t, err, "file must still exist when hook fires")
		seen = append(seen, p+"="+string(b))
	}
	next := sshScopeOf(map[string]string{"k": "-----BEGIN X-----\nnew"})
	require.NoError(t, syncSSHCredentials(home, &next, hook))
	assert.Equal(t, []string{target + "=-----BEGIN X-----\nold"}, seen)
	b, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "-----BEGIN X-----\nnew", string(b))
}

func TestSyncSSHCredentials_RevocationLegacySweepWithoutManifest(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("-----BEGIN X-----\nold"), 0600))

	var removed []string
	require.NoError(t, syncSSHCredentials(home, &config.SSHScope{}, func(p string) { removed = append(removed, filepath.Base(p)) }))
	assert.NoFileExists(t, filepath.Join(sshDir, "id_rsa"))
	assert.Equal(t, []string{"id_rsa"}, removed)

	// Sweep is one-time: a later user-placed legacy-name file survives.
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("user"), 0600))
	require.NoError(t, SyncSSHCredentials(home, &config.SSHScope{}))
	assert.FileExists(t, filepath.Join(sshDir, "id_rsa"))
}

func TestSyncSSHCredentials_ManifestReadErrorAborts(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(filepath.Join(sshDir, config.SSHManifestName), 0700)) // unreadable as a file
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("user"), 0600))
	require.Error(t, SyncSSHCredentials(home, &config.SSHScope{}))
	assert.FileExists(t, filepath.Join(sshDir, "id_rsa"))
}
