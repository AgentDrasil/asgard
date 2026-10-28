package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/proxy"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

func TestSandboxConfigFromTree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tree := &config.AsgardTreeConfig{}
	tree.Asgard.Config.Host = "127.0.0.1"
	tree.Asgard.Config.GeminiAPIKey = "k"
	tree.Asgard.Config.GeminiModelForChatTitle = "m"
	tree.Asgard.Config.DB = "sqlite"
	conf, err := SandboxConfigFromTree(tree)
	require.NoError(t, err)
	assert.False(t, conf.IsProxyEnabled())
	assert.Equal(t, paths.ConfigFile(), conf.GetConfigPath())
	assert.NotEmpty(t, conf.GetProviders())

	tree.Asgard.Proxy = proxy.Config{Enable: true} // enabled without rules: invalid, like on the backend
	_, err = SandboxConfigFromTree(tree)
	assert.ErrorContains(t, err, "invalid configuration from etcd")

	tree.Asgard.Proxy = proxy.Config{}
	tree.Asgard.Config.DB = "bogus"
	_, err = SandboxConfigFromTree(tree)
	assert.ErrorContains(t, err, "invalid configuration from etcd")
}
