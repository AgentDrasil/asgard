package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaths_RootAndSubdirs(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	expectedRoot := filepath.Join(tempHome, "asgard")
	assert.Equal(t, expectedRoot, Root())
	assert.Equal(t, filepath.Join(expectedRoot, "config"), ConfigDir())
	assert.Equal(t, filepath.Join(expectedRoot, "config", "config.yaml"), ConfigFile())
	assert.Equal(t, filepath.Join(expectedRoot, "config", "proxy.yaml"), ProxyConfigFile())
	assert.Equal(t, filepath.Join(expectedRoot, "config", "keys.yaml"), KeysFile())
	assert.Equal(t, filepath.Join(expectedRoot, "config", "ca", "ca.crt"), CACertFile())
	assert.Equal(t, filepath.Join(expectedRoot, "config", "ca", "ca.key"), CAKeyFile())
	assert.Equal(t, expectedRoot, AgentsRoot())
	assert.Equal(t, filepath.Join(expectedRoot, "agents"), AgentsDir())
	assert.Equal(t, filepath.Join(expectedRoot, "teams.yaml"), TeamsFile())
	assert.Equal(t, filepath.Join(expectedRoot, "logs"), LogsDir())
	assert.Equal(t, filepath.Join(expectedRoot, "data"), DataDir())
	assert.Equal(t, filepath.Join(expectedRoot, "data", "data.db"), DBFile())
	assert.Equal(t, filepath.Join(expectedRoot, "data", "sessions"), SessionsDir())
	assert.Equal(t, filepath.Join(expectedRoot, "data", "sessions", "chat-1"), SessionDir("chat-1"))
	assert.Equal(t, filepath.Join(expectedRoot, "data", "tmp"), TmpDir())
	assert.Equal(t, filepath.Join(expectedRoot, "data", "tmp", "chat-1"), SessionTmpDir("chat-1"))
	assert.Equal(t, filepath.Join(expectedRoot, "data", "tmp", "chat-1", "attachments"), AttachmentsDir("chat-1"))
	assert.Equal(t, filepath.Join(expectedRoot, "data", "tmp", "sock-name"), SockDir("sock-name"))
}

func TestPaths_CABundle(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	require.Equal(t, ".asgard-ca", CABundleDirName)
	expectedBase := filepath.Join(tempHome, "asgard", "data", "tmp", ".asgard-ca")
	assert.Equal(t, expectedBase, CABundleBaseDir())
	assert.Equal(t, filepath.Join(expectedBase, "chat-xyz"), CABundleDir("chat-xyz"))
}
