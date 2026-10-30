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

func TestSyncCLICredentials_Direct(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		creds      *config.CredentialsScope
		wantOAuth  string
		wantToken  string
		checkOAuth bool
		checkToken bool
	}{
		{
			name:  "nil creds does nothing",
			creds: nil,
		},
		{
			name: "oauth creds written with 0600 mode and dir 0700",
			creds: &config.CredentialsScope{
				OAuthCreds: []byte(`{"access_token":"token-1"}`),
			},
			wantOAuth:  `{"access_token":"token-1"}`,
			checkOAuth: true,
		},
		{
			name: "antigravity token written with 0600 mode and dir 0700",
			creds: &config.CredentialsScope{
				AntigravityOAuthToken: []byte("token-agy-1"),
			},
			wantToken:  "token-agy-1",
			checkToken: true,
		},
		{
			name: "both credentials written together",
			creds: &config.CredentialsScope{
				OAuthCreds:            []byte(`{"access_token":"token-2"}`),
				AntigravityOAuthToken: []byte("token-agy-2"),
			},
			wantOAuth:  `{"access_token":"token-2"}`,
			wantToken:  "token-agy-2",
			checkOAuth: true,
			checkToken: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempHome := t.TempDir()
			err := SyncCLICredentials(tempHome, tt.creds)
			require.NoError(t, err)

			if tt.checkOAuth {
				oauthPath := filepath.Join(tempHome, ".gemini", "oauth_creds.json")
				data, err := os.ReadFile(oauthPath)
				require.NoError(t, err)
				assert.Equal(t, tt.wantOAuth, string(data))

				info, err := os.Stat(oauthPath)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

				dirInfo, err := os.Stat(filepath.Join(tempHome, ".gemini"))
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0700), dirInfo.Mode().Perm())
			}

			if tt.checkToken {
				tokenPath := filepath.Join(tempHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
				data, err := os.ReadFile(tokenPath)
				require.NoError(t, err)
				assert.Equal(t, tt.wantToken, string(data))

				info, err := os.Stat(tokenPath)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

				dirInfo, err := os.Stat(filepath.Join(tempHome, ".gemini", "antigravity-cli"))
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0700), dirInfo.Mode().Perm())
			}
		})
	}
}

func TestSyncCLICredentials_OnTreeChange(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()

	tree := validTree(proxy.Config{})
	tree.Credentials.OAuthCreds = []byte(`{"access_token":"initial-token"}`)
	tree.Credentials.AntigravityOAuthToken = []byte(`tok-initial`)

	initialConf, err := runnerconfig.SandboxConfigFromTree(tree)
	require.NoError(t, err)

	src := &fakeSource{conf: initialConf, tree: tree}

	sb := Setup(src, Options{SkipSSH: true, HomeDir: tempHome})
	t.Cleanup(sb.Close)

	require.NoError(t, sb.Ready())

	// Verify ~/.gemini/oauth_creds.json written on initial setup
	oauthPath := filepath.Join(tempHome, ".gemini", "oauth_creds.json")
	oauthBytes, err := os.ReadFile(oauthPath)
	require.NoError(t, err)
	assert.Equal(t, `{"access_token":"initial-token"}`, string(oauthBytes))

	oauthStat, err := os.Stat(oauthPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), oauthStat.Mode().Perm())

	geminiDirStat, err := os.Stat(filepath.Join(tempHome, ".gemini"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), geminiDirStat.Mode().Perm())

	// Verify antigravity-oauth-token
	agyPath := filepath.Join(tempHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	agyBytes, err := os.ReadFile(agyPath)
	require.NoError(t, err)
	assert.Equal(t, "tok-initial", string(agyBytes))

	agyStat, err := os.Stat(agyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), agyStat.Mode().Perm())

	// Now fire tree change with updated credentials
	updatedTree := validTree(proxy.Config{})
	updatedTree.Credentials.OAuthCreds = []byte(`{"access_token":"updated-token"}`)
	updatedTree.Credentials.AntigravityOAuthToken = []byte(`tok-updated`)

	src.fire(updatedTree)

	// Verify updated ~/.gemini/oauth_creds.json
	oauthBytes, err = os.ReadFile(oauthPath)
	require.NoError(t, err)
	assert.Equal(t, `{"access_token":"updated-token"}`, string(oauthBytes))

	oauthStat, err = os.Stat(oauthPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), oauthStat.Mode().Perm())

	// Verify updated antigravity-oauth-token
	agyBytes, err = os.ReadFile(agyPath)
	require.NoError(t, err)
	assert.Equal(t, "tok-updated", string(agyBytes))

	agyStat, err = os.Stat(agyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), agyStat.Mode().Perm())

	// Now fire tree change with cleared/revoked credentials
	clearedTree := validTree(proxy.Config{})
	clearedTree.Credentials.OAuthCreds = nil
	clearedTree.Credentials.AntigravityOAuthToken = nil

	src.fire(clearedTree)

	// Verify both credential files are removed upon revocation
	_, err = os.Stat(oauthPath)
	assert.True(t, os.IsNotExist(err), "oauth_creds.json must be removed when credential is empty/cleared")

	_, err = os.Stat(agyPath)
	assert.True(t, os.IsNotExist(err), "antigravity-oauth-token must be removed when credential is empty/cleared")
}

func TestSyncCLICredentials_RevocationAndPermissionTightening(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	geminiDir := filepath.Join(tempHome, ".gemini")
	agyDir := filepath.Join(geminiDir, "antigravity-cli")

	// Pre-create directories with loose permissions 0755
	require.NoError(t, os.MkdirAll(agyDir, 0755))
	require.NoError(t, os.Chmod(geminiDir, 0755))
	require.NoError(t, os.Chmod(agyDir, 0755))

	// Write pre-existing credentials
	oauthPath := filepath.Join(geminiDir, "oauth_creds.json")
	agyPath := filepath.Join(agyDir, "antigravity-oauth-token")
	require.NoError(t, os.WriteFile(oauthPath, []byte("old-oauth"), 0644))
	require.NoError(t, os.WriteFile(agyPath, []byte("old-agy"), 0644))

	// Sync with updated credentials
	creds := &config.CredentialsScope{
		OAuthCreds:            []byte("new-oauth"),
		AntigravityOAuthToken: []byte("new-agy"),
	}
	require.NoError(t, SyncCLICredentials(tempHome, creds))

	// Verify permissions tightened to 0700 on directories and 0600 on files
	geminiStat, err := os.Stat(geminiDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), geminiStat.Mode().Perm())

	agyDirStat, err := os.Stat(agyDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), agyDirStat.Mode().Perm())

	oauthStat, err := os.Stat(oauthPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), oauthStat.Mode().Perm())

	// Sync with matching content: should succeed without error (no-op clobber protection)
	require.NoError(t, SyncCLICredentials(tempHome, creds))

	// Revoke: sync with empty credentials
	emptyCreds := &config.CredentialsScope{}
	require.NoError(t, SyncCLICredentials(tempHome, emptyCreds))

	_, err = os.Stat(oauthPath)
	assert.True(t, os.IsNotExist(err))

	_, err = os.Stat(agyPath)
	assert.True(t, os.IsNotExist(err))
}
