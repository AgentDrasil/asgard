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
		"simplest/key.yaml": []byte(`
providers:
  google:
    apiKey: test-google-key
`),
		"simplest/models.yaml":                []byte("models: []\n"),
		"simplest/providers.yaml":             []byte("providers: {}\n"),
		"credentials/auth.json":               []byte(`{"type":"test"}`),
		"credentials/antigravity-oauth-token": []byte("test-oauth-token"),
		"credentials/gemini-api-key":          []byte("test-gemini-secret"),
		"credentials/xai-api-key":             []byte("test-xai-secret"),
		"credentials/ssh/key":                 []byte("ssh-rsa AAAAB3NzaC1yc2E..."),
		"credentials/ssh/key.pub":             []byte("ssh-rsa AAAAB3NzaC1yc2E... user@host"),
		"credentials/ssh/config":              []byte("Host *\n  ServerAliveInterval 60\n"),
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
	assert.NotEmpty(t, cfg.Simplest.Key)
	assert.NotEmpty(t, cfg.Simplest.Models)
	assert.NotEmpty(t, cfg.Simplest.Providers)

	// Validate credentials scope
	assert.Equal(t, "test-gemini-secret", cfg.Credentials.GeminiAPIKey)
	assert.Equal(t, "test-xai-secret", cfg.Credentials.XAIAPIKey)
	assert.Equal(t, []byte(`{"type":"test"}`), cfg.Credentials.Auth)
	assert.Equal(t, []byte("test-oauth-token"), cfg.Credentials.AntigravityOAuthToken)
	assert.Equal(t, []byte("ssh-rsa AAAAB3NzaC1yc2E..."), cfg.Credentials.SSH.Key)
	assert.Equal(t, []byte("ssh-rsa AAAAB3NzaC1yc2E... user@host"), cfg.Credentials.SSH.KeyPub)
	assert.Equal(t, []byte("Host *\n  ServerAliveInterval 60\n"), cfg.Credentials.SSH.Config)
}
