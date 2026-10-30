package runnerfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoveChildCountsOnlyDirectories(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "dir"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))
	n, err := removeChild(root, "dir")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	n, err = removeChild(root, "link")
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	_, err = os.Lstat(filepath.Join(root, "link"))
	assert.Error(t, err)
	_, err = os.Stat(outside)
	assert.NoError(t, err)
}
