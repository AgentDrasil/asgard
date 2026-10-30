package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaths_RootOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvRoot, root)
	assert.Equal(t, root, Root())
	assert.Equal(t, filepath.Join(root, "config"), ConfigDir())
	assert.Equal(t, filepath.Join(root, "config", "config.yaml"), ConfigFile())
	assert.Equal(t, filepath.Join(root, "config", "proxy.yaml"), ProxyConfigFile())
	assert.Equal(t, filepath.Join(root, "config", "keys.yaml"), KeysFile())
	assert.Equal(t, filepath.Join(root, "config", "ca", "ca.crt"), CACertFile())
	assert.Equal(t, filepath.Join(root, "config", "ca", "ca.key"), CAKeyFile())
	assert.Equal(t, root, AgentsRoot())
	assert.Equal(t, filepath.Join(root, "agents"), AgentsDir())
	assert.Equal(t, filepath.Join(root, "teams.yaml"), TeamsFile())
	assert.Equal(t, filepath.Join(root, "agents-cache"), AgentsCacheDir())
	assert.Equal(t, filepath.Join(root, "agents-cache", "coder@abc123"), AgentCacheDir("coder", "abc123"))
	assert.Equal(t, filepath.Join(root, "agents-cache", "runners", "n1", "coder@abc123"), RunnerAgentCacheDir("n1", "coder", "abc123"))
}

func TestPaths_Fallback(t *testing.T) {
	r := Root()
	require.NotEmpty(t, r)
}
