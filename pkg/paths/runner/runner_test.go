package runner

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunnerPaths_Override(t *testing.T) {
	d := t.TempDir()
	t.Setenv(EnvDataDir, d)

	assert.Equal(t, d, DataDir())
	assert.Equal(t, filepath.Join(d, "logs"), LogsDir())
	assert.Equal(t, filepath.Join(d, "sessions"), SessionsDir())
	assert.Equal(t, filepath.Join(d, "sessions", "chat-1"), SessionDir("chat-1"))
	assert.Equal(t, filepath.Join(d, "tmp"), TmpDir())
	assert.Equal(t, filepath.Join(d, "tmp", "chat-1"), SessionTmpDir("chat-1"))
	assert.Equal(t, filepath.Join(d, "tmp", "chat-1", "attachments"), AttachmentsDir("chat-1"))
	assert.Equal(t, filepath.Join(d, "tmp", ".asgard-ca"), CABundleBaseDir())
	assert.Equal(t, filepath.Join(d, "tmp", ".asgard-ca", "chat-1"), CABundleDir("chat-1"))
	assert.Equal(t, filepath.Join(d, "tmp", "sock-1"), SockDir("sock-1"))
}
