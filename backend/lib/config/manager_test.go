package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/elmhuangyu/configs/pkg/configlib"
)

func TestNewEtcdConfigManager_MissingEnvFailsFast(t *testing.T) {
	t.Setenv(EnvEtcdEndpoints, "")
	ctx := context.Background()

	mgr, client, err := NewEtcdConfigManager(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingEtcdEndpoints)
	assert.Nil(t, mgr)
	assert.Nil(t, client)
}

func TestAsgardTreeConfig_BindingFromMemorySource(t *testing.T) {
	kvs := configlib.FlattenKVs{
		"asgard/config.yaml": []byte(`
debug: true
host: https://asgard.example.com
port: 7000
internal_port: 8081
db: sqlite
gemini_api_key: test-gemini-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
`),
		"asgard/proxy.yaml": []byte(`
enable: true
server:
  addr: 127.0.0.1:8082
rules:
  - host: api.x.ai
    header_key: Authorization
    dummy_secret: Bearer dummy
`),
		"aw/config.yaml": []byte(`
agents:
  - agy:
      - gemini.*
`),
		"credentials/opencode/opencode.aw.yaml": []byte(`env:
  OPENCODE_ZAI_API_KEY: "test-zai-key"
`),
		"credentials/simplest/key.yaml": []byte(`
providers:
  google:
    apiKey: test-google-key
`),
		"simplest/models.yaml":                    []byte("models: []\n"),
		"simplest/providers.yaml":                 []byte("providers: {}\n"),
		"credentials/auth.json":                   []byte(`{"type":"test"}`),
		"credentials/agy/antigravity-oauth-token": []byte("test-oauth-token"),
		"credentials/claude/.credentials.json":    []byte(`{"claude_token":"test-claude"}`),
		"credentials/gemini-api-key":              []byte("test-gemini-secret"),
		"credentials/xai-api-key":                 []byte("test-xai-secret"),
		"credentials/gcp/gcp-sa.json":             []byte(`{"project_id":"test-sa-proj"}`),
		"credentials/ssh/key":                     []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nkey\n-----END OPENSSH PRIVATE KEY-----"),
		"credentials/ssh/key.pub":                 []byte("ssh-rsa AAAAB3NzaC1yc2E... user@host"),
		"credentials/ssh/id_ed25519":              []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ned25519-key\n-----END OPENSSH PRIVATE KEY-----"),
		"credentials/ssh/id_ed25519.pub":          []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5... user@host"),
		"credentials/ssh/id_rsa":                  []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nrsa-key\n-----END OPENSSH PRIVATE KEY-----"),
		"credentials/ssh/id_rsa.pub":              []byte("ssh-rsa AAAAB3NzaC1yc2E... id_rsa-pub"),
		"credentials/ssh/id_ecdsa":                []byte("-----BEGIN OPENSSH PRIVATE KEY-----\necdsa-key\n-----END OPENSSH PRIVATE KEY-----"),
		"credentials/ssh/id_ecdsa.pub":            []byte("ecdsa-sha2-nistp256 AAAAE2VjZHNh... id_ecdsa-pub"),
		"credentials/ssh/id_dsa":                  []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ndsa-key\n-----END OPENSSH PRIVATE KEY-----"),
		"credentials/ssh/id_dsa.pub":              []byte("ssh-dss AAAAB3NzaC1kc3M... id_dsa-pub"),
		"credentials/ssh/huangyu":                 []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nhuangyu\n-----END OPENSSH PRIVATE KEY-----"),
		"credentials/ssh/huangyu.pub":             []byte("ssh-ed25519 AAAA huangyu"),
		"credentials/ssh/config":                  []byte("Host *\n  ServerAliveInterval 60\n"),
		"credentials/ssh/known_hosts":             []byte("github.com ssh-rsa AAAAB3NzaC1yc2E..."),
	}

	src := configlib.NewMemSource(kvs)
	mgr := configlib.NewManager[AsgardTreeConfig](src)

	ctx := context.Background()
	err := mgr.Start(ctx)
	require.NoError(t, err)

	cfg := mgr.Get()
	require.NotNil(t, cfg)

	// Validate Asgard main config
	assert.True(t, cfg.Asgard.Config.Debug)
	assert.Equal(t, "https://asgard.example.com", cfg.Asgard.Config.Host)
	assert.Equal(t, 7000, cfg.Asgard.Config.Port)
	assert.Equal(t, "test-gemini-key", cfg.Asgard.Config.GeminiAPIKey)

	// Validate Proxy
	assert.True(t, cfg.Asgard.Proxy.Enable)
	assert.Equal(t, "127.0.0.1:8082", cfg.Asgard.Proxy.Server.Addr)
	assert.Len(t, cfg.Asgard.Proxy.Rules, 1)

	// Validate AW
	assert.True(t, cfg.AW.IsModelAllowed("agy", "gemini-1.5-pro"))

	// Validate simplest raw scope
	assert.NotEmpty(t, cfg.Simplest.Models)
	assert.NotEmpty(t, cfg.Simplest.Providers)

	// Validate credentials scope
	assert.Equal(t, []byte("env:\n  OPENCODE_ZAI_API_KEY: \"test-zai-key\"\n"), cfg.Credentials.Opencode)
	assert.NotEmpty(t, cfg.Credentials.Simplest)
	assert.Equal(t, "test-gemini-secret", cfg.Credentials.GeminiAPIKey)
	assert.Equal(t, "test-xai-secret", cfg.Credentials.XAIAPIKey)
	assert.Equal(t, []byte(`{"project_id":"test-sa-proj"}`), cfg.Credentials.GCPSAKey)
	assert.Equal(t, []byte(`{"type":"test"}`), cfg.Credentials.Auth)
	// Legacy agy/claude keys in etcd are ignored (accounts are runner-local).
	ssh := cfg.Credentials.SSH.AllFiles()
	assert.Equal(t, []byte("ssh-rsa AAAAB3NzaC1yc2E... user@host"), ssh["key.pub"])
	assert.Equal(t, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ned25519-key\n-----END OPENSSH PRIVATE KEY-----"), ssh["id_ed25519"])
	assert.Equal(t, []byte("ssh-rsa AAAAB3NzaC1yc2E... id_rsa-pub"), ssh["id_rsa.pub"])
	assert.Equal(t, []byte("ssh-dss AAAAB3NzaC1kc3M... id_dsa-pub"), ssh["id_dsa.pub"])
	assert.Equal(t, []byte("Host *\n  ServerAliveInterval 60\n"), ssh["config"])
	assert.Equal(t, []byte("github.com ssh-rsa AAAAB3NzaC1yc2E..."), ssh["known_hosts"])
	// Arbitrary filenames bind too
	assert.Equal(t, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nhuangyu\n-----END OPENSSH PRIVATE KEY-----"), ssh["huangyu"])
	assert.Equal(t, []byte("ssh-ed25519 AAAA huangyu"), ssh["huangyu.pub"])

	// Validate SSH helper methods
	assert.True(t, cfg.Credentials.SSH.HasKeys())
	privKeys := cfg.Credentials.SSH.PrivateKeys()
	assert.Len(t, privKeys, 6) // key, id_ed25519, id_rsa, id_ecdsa, id_dsa, huangyu
	assert.Contains(t, privKeys, "huangyu")
	assert.Contains(t, privKeys, "key")
	assert.Contains(t, privKeys, "id_ed25519")
	assert.Contains(t, privKeys, "id_rsa")
	assert.Contains(t, privKeys, "id_ecdsa")
	assert.Contains(t, privKeys, "id_dsa")

	allFiles := cfg.Credentials.SSH.AllFiles()
	assert.Len(t, allFiles, 14)
	assert.Contains(t, allFiles, "key")
	assert.Contains(t, allFiles, "key.pub")
	assert.Contains(t, allFiles, "id_ed25519")
	assert.Contains(t, allFiles, "id_ed25519.pub")
	assert.Contains(t, allFiles, "id_rsa")
	assert.Contains(t, allFiles, "id_rsa.pub")
	assert.Contains(t, allFiles, "id_ecdsa")
	assert.Contains(t, allFiles, "id_ecdsa.pub")
	assert.Contains(t, allFiles, "id_dsa")
	assert.Contains(t, allFiles, "id_dsa.pub")
	assert.Contains(t, allFiles, "config")
	assert.Contains(t, allFiles, "known_hosts")

	fp := cfg.Credentials.SSH.Fingerprint()
	assert.NotEmpty(t, fp)
	assert.Len(t, fp, 32)
	// Deterministic fingerprint
	assert.Equal(t, fp, cfg.Credentials.SSH.Fingerprint())
}

func sshScope(files map[string]string) *SSHScope {
	s := &SSHScope{Files: map[string]SSHFile{}}
	for k, v := range files {
		s.Files[k] = SSHFile{Data: []byte(v)}
	}
	return s
}

const pemKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nx\n-----END OPENSSH PRIVATE KEY-----"

func TestSSHScope_HelperMethods(t *testing.T) {
	// Nil receiver tests
	var nilScope *SSHScope
	assert.Empty(t, nilScope.AllFiles())
	assert.Empty(t, nilScope.PrivateKeys())
	assert.False(t, nilScope.HasKeys())
	assert.Nil(t, nilScope.Fingerprint())

	// Empty struct tests
	emptyScope := &SSHScope{}
	assert.Empty(t, emptyScope.AllFiles())
	assert.Empty(t, emptyScope.PrivateKeys())
	assert.False(t, emptyScope.HasKeys())
	assert.Nil(t, emptyScope.Fingerprint())

	edScope := sshScope(map[string]string{
		"huangyu":       pemKey,
		"huangyu.pub":   "ed25519-public",
		"config":        "Host github.com",
		"notakey":       "plain text",
		"empty":         "",
		"../evil":       pemKey,
		"a/b":           pemKey,
		"..":            pemKey,
		SSHManifestName: "x",
		"k.sops.json":   "{}",
	})
	assert.True(t, edScope.HasKeys())
	assert.Equal(t, map[string][]byte{"huangyu": []byte(pemKey)}, edScope.PrivateKeys())
	all := edScope.AllFiles()
	assert.Len(t, all, 4) // huangyu, huangyu.pub, config, notakey
	assert.Contains(t, all, "notakey")

	fp1 := edScope.Fingerprint()
	assert.Len(t, fp1, 32)
	assert.Equal(t, fp1, edScope.Fingerprint())

	// Mutating content, adding or removing a key changes fingerprint
	changed := sshScope(map[string]string{"huangyu": pemKey + "2", "huangyu.pub": "ed25519-public", "config": "Host github.com", "notakey": "plain text"})
	assert.NotEqual(t, fp1, changed.Fingerprint())
	added := sshScope(map[string]string{"huangyu": pemKey, "second": pemKey, "huangyu.pub": "ed25519-public", "config": "Host github.com", "notakey": "plain text"})
	assert.NotEqual(t, fp1, added.Fingerprint())
	removed := sshScope(map[string]string{"huangyu.pub": "ed25519-public", "config": "Host github.com", "notakey": "plain text"})
	assert.NotEqual(t, fp1, removed.Fingerprint())

	// Length-prefix encoding: different boundaries produce distinct fingerprints
	assert.NotEqual(t, sshScope(map[string]string{"a": "foo", "b": "bar"}).Fingerprint(), sshScope(map[string]string{"a": "fo", "b": "obar"}).Fingerprint())
}

func TestSSHScope_ArbitraryNameBinding(t *testing.T) {
	kvs := configlib.FlattenKVs{
		"credentials/ssh/huangyu":     []byte(pemKey),
		"credentials/ssh/huangyu.pub": []byte("ssh-ed25519 AAAA"),
		"credentials/ssh/sub/nested":  []byte(pemKey),
	}
	mgr := configlib.NewManager[AsgardTreeConfig](configlib.NewMemSource(kvs))
	require.NoError(t, mgr.Start(context.Background()))
	ssh := mgr.Get().Credentials.SSH
	assert.Equal(t, []byte(pemKey), ssh.AllFiles()["huangyu"])
	assert.Equal(t, map[string][]byte{"huangyu": []byte(pemKey)}, ssh.PrivateKeys())
	assert.NotContains(t, ssh.AllFiles(), "sub")
}

func TestPortFromEnvOverridesConfigPort(t *testing.T) {
	t.Setenv(EnvPort, "7123")
	p, ok := PortFromEnv()
	require.True(t, ok)
	assert.Equal(t, 7123, p)

	c := &Config{DB: "sqlite", DSN: "test.db", Host: "127.0.0.1", GeminiAPIKey: "test-key", GeminiModelForChatTitle: "gemini-3.1-flash-lite", Port: 9000}
	require.NoError(t, c.ApplyDefaultsAndValidate())
	assert.Equal(t, 7123, c.Port)

	for _, bad := range []string{"", "abc", "0", "70000"} {
		t.Setenv(EnvPort, bad)
		_, ok := PortFromEnv()
		assert.False(t, ok, bad)
	}

	t.Setenv(EnvPort, "")
	c = &Config{DB: "sqlite", DSN: "test.db", Host: "127.0.0.1", GeminiAPIKey: "test-key", GeminiModelForChatTitle: "gemini-3.1-flash-lite", Port: 9000}
	require.NoError(t, c.ApplyDefaultsAndValidate())
	assert.Equal(t, 9000, c.Port, "legacy etcd port still works without the env")
}
