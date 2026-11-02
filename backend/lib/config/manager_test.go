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
		"aw/opencode.aw.yaml": []byte(`env:
  OPENCODE_ZAI_API_KEY: "test-zai-key"
`),
		"simplest/key.yaml": []byte(`
providers:
  google:
    apiKey: test-google-key
`),
		"simplest/models.yaml":                 []byte("models: []\n"),
		"simplest/providers.yaml":              []byte("providers: {}\n"),
		"credentials/auth.json":                []byte(`{"type":"test"}`),
		"credentials/oauth_creds.json":         []byte(`{"client_id":"test-id","client_secret":"test-secret"}`),
		"credentials/antigravity-oauth-token":  []byte("test-oauth-token"),
		"credentials/claude/.credentials.json": []byte(`{"claude_token":"test-claude"}`),
		"credentials/.credentials.json":        []byte(`{"claude_root_token":"test-claude-root"}`),
		"credentials/gemini-api-key":           []byte("test-gemini-secret"),
		"credentials/xai-api-key":              []byte("test-xai-secret"),
		"credentials/ssh/key":                  []byte("ssh-rsa AAAAB3NzaC1yc2E..."),
		"credentials/ssh/key.pub":              []byte("ssh-rsa AAAAB3NzaC1yc2E... user@host"),
		"credentials/ssh/id_ed25519":           []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ned25519-key\n-----END OPENSSH PRIVATE KEY-----"),
		"credentials/ssh/id_ed25519.pub":       []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5... user@host"),
		"credentials/ssh/id_rsa":               []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nrsa-key\n-----END OPENSSH PRIVATE KEY-----"),
		"credentials/ssh/id_rsa.pub":           []byte("ssh-rsa AAAAB3NzaC1yc2E... id_rsa-pub"),
		"credentials/ssh/id_ecdsa":             []byte("-----BEGIN OPENSSH PRIVATE KEY-----\necdsa-key\n-----END OPENSSH PRIVATE KEY-----"),
		"credentials/ssh/id_ecdsa.pub":         []byte("ecdsa-sha2-nistp256 AAAAE2VjZHNh... id_ecdsa-pub"),
		"credentials/ssh/id_dsa":               []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ndsa-key\n-----END OPENSSH PRIVATE KEY-----"),
		"credentials/ssh/id_dsa.pub":           []byte("ssh-dss AAAAB3NzaC1kc3M... id_dsa-pub"),
		"credentials/ssh/config":               []byte("Host *\n  ServerAliveInterval 60\n"),
		"credentials/ssh/known_hosts":          []byte("github.com ssh-rsa AAAAB3NzaC1yc2E..."),
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
	assert.Equal(t, []byte("env:\n  OPENCODE_ZAI_API_KEY: \"test-zai-key\"\n"), cfg.OpencodeAW)

	// Validate simplest raw scope
	assert.NotEmpty(t, cfg.Simplest.Key)
	assert.NotEmpty(t, cfg.Simplest.Models)
	assert.NotEmpty(t, cfg.Simplest.Providers)

	// Validate credentials scope
	assert.Equal(t, "test-gemini-secret", cfg.Credentials.GeminiAPIKey)
	assert.Equal(t, "test-xai-secret", cfg.Credentials.XAIAPIKey)
	assert.Equal(t, []byte(`{"type":"test"}`), cfg.Credentials.Auth)
	assert.Equal(t, []byte(`{"client_id":"test-id","client_secret":"test-secret"}`), cfg.Credentials.OAuthCreds)
	assert.Equal(t, []byte("test-oauth-token"), cfg.Credentials.AntigravityOAuthToken)
	assert.Equal(t, []byte(`{"claude_token":"test-claude"}`), cfg.Credentials.Claude)
	assert.Equal(t, []byte(`{"claude_root_token":"test-claude-root"}`), cfg.Credentials.ClaudeRoot)
	assert.Equal(t, []byte(`{"claude_token":"test-claude"}`), cfg.Credentials.GetClaudeCredentials())
	assert.Equal(t, []byte("ssh-rsa AAAAB3NzaC1yc2E..."), cfg.Credentials.SSH.Key)
	assert.Equal(t, []byte("ssh-rsa AAAAB3NzaC1yc2E... user@host"), cfg.Credentials.SSH.KeyPub)
	assert.Equal(t, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ned25519-key\n-----END OPENSSH PRIVATE KEY-----"), cfg.Credentials.SSH.IdEd25519)
	assert.Equal(t, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5... user@host"), cfg.Credentials.SSH.IdEd25519Pub)
	assert.Equal(t, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nrsa-key\n-----END OPENSSH PRIVATE KEY-----"), cfg.Credentials.SSH.IdRSA)
	assert.Equal(t, []byte("ssh-rsa AAAAB3NzaC1yc2E... id_rsa-pub"), cfg.Credentials.SSH.IdRSAPub)
	assert.Equal(t, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\necdsa-key\n-----END OPENSSH PRIVATE KEY-----"), cfg.Credentials.SSH.IdECDSA)
	assert.Equal(t, []byte("ecdsa-sha2-nistp256 AAAAE2VjZHNh... id_ecdsa-pub"), cfg.Credentials.SSH.IdECDSAPub)
	assert.Equal(t, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ndsa-key\n-----END OPENSSH PRIVATE KEY-----"), cfg.Credentials.SSH.IdDSA)
	assert.Equal(t, []byte("ssh-dss AAAAB3NzaC1kc3M... id_dsa-pub"), cfg.Credentials.SSH.IdDSAPub)
	assert.Equal(t, []byte("Host *\n  ServerAliveInterval 60\n"), cfg.Credentials.SSH.Config)
	assert.Equal(t, []byte("github.com ssh-rsa AAAAB3NzaC1yc2E..."), cfg.Credentials.SSH.KnownHosts)

	// Validate SSH helper methods
	assert.True(t, cfg.Credentials.SSH.HasKeys())
	privKeys := cfg.Credentials.SSH.PrivateKeys()
	assert.Len(t, privKeys, 5) // key, id_ed25519, id_rsa, id_ecdsa, id_dsa
	assert.Contains(t, privKeys, "key")
	assert.Contains(t, privKeys, "id_ed25519")
	assert.Contains(t, privKeys, "id_rsa")
	assert.Contains(t, privKeys, "id_ecdsa")
	assert.Contains(t, privKeys, "id_dsa")

	allFiles := cfg.Credentials.SSH.AllFiles()
	assert.Len(t, allFiles, 12)
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

	// Subset of keys: ed25519 only
	edScope := &SSHScope{
		IdEd25519:    []byte("ed25519-private"),
		IdEd25519Pub: []byte("ed25519-public"),
		Config:       []byte("Host github.com"),
	}
	assert.True(t, edScope.HasKeys())
	assert.Equal(t, map[string][]byte{"id_ed25519": []byte("ed25519-private")}, edScope.PrivateKeys())
	all := edScope.AllFiles()
	assert.Len(t, all, 3)
	assert.Equal(t, []byte("ed25519-private"), all["id_ed25519"])
	assert.Equal(t, []byte("ed25519-public"), all["id_ed25519.pub"])
	assert.Equal(t, []byte("Host github.com"), all["config"])

	fp1 := edScope.Fingerprint()
	assert.Len(t, fp1, 32)

	// Mutating content changes fingerprint
	edScope2 := &SSHScope{
		IdEd25519:    []byte("ed25519-private-changed"),
		IdEd25519Pub: []byte("ed25519-public"),
		Config:       []byte("Host github.com"),
	}
	assert.NotEqual(t, fp1, edScope2.Fingerprint())

	// Length-prefix encoding: keys with different lengths/boundaries produce distinct fingerprints
	scopeA := &SSHScope{Key: []byte("foo"), KeyPub: []byte("bar")}
	scopeB := &SSHScope{Key: []byte("fo"), KeyPub: []byte("obar")}
	assert.NotEqual(t, scopeA.Fingerprint(), scopeB.Fingerprint())
}

func TestCredentialsScope_GetClaudeCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scope    *CredentialsScope
		expected []byte
	}{
		{
			name:     "nil receiver returns nil",
			scope:    nil,
			expected: nil,
		},
		{
			name:     "empty struct returns nil",
			scope:    &CredentialsScope{},
			expected: nil,
		},
		{
			name: "claude present returns claude",
			scope: &CredentialsScope{
				Claude: []byte("claude-creds"),
			},
			expected: []byte("claude-creds"),
		},
		{
			name: "claudeRoot present returns claudeRoot",
			scope: &CredentialsScope{
				ClaudeRoot: []byte("root-creds"),
			},
			expected: []byte("root-creds"),
		},
		{
			name: "both claude and claudeRoot present favors claude",
			scope: &CredentialsScope{
				Claude:     []byte("claude-creds"),
				ClaudeRoot: []byte("root-creds"),
			},
			expected: []byte("claude-creds"),
		},
		{
			name: "empty claude with non-empty claudeRoot falls back to claudeRoot",
			scope: &CredentialsScope{
				Claude:     []byte(""),
				ClaudeRoot: []byte("root-creds"),
			},
			expected: []byte("root-creds"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.scope.GetClaudeCredentials()
			assert.Equal(t, tt.expected, got)
		})
	}
}
