package runnerfs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupLeavesSharedDirToOrphanSweep(t *testing.T) {
	base := t.TempDir()
	roots := CleanupRoots{Tmp: filepath.Join(base, "tmp"), Sessions: filepath.Join(base, "sessions"), Shared: filepath.Join(base, "shared")}
	for _, d := range []string{roots.Tmp, roots.Sessions, roots.Shared} {
		require.NoError(t, os.MkdirAll(filepath.Join(d, "root-1"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(d, "other"), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(roots.Shared, "root-1", "report.md"), []byte("x"), 0o644))

	// Per-chat cleanup must not touch shared/<id>: descendants may still use it.
	res := roots.Cleanup(CleanupSpec{ChatIDs: []string{"root-1"}})
	assert.Empty(t, res.Failures)
	assert.DirExists(t, filepath.Join(roots.Shared, "root-1"))
	assert.FileExists(t, filepath.Join(roots.Shared, "root-1", "report.md"))
	assert.NoDirExists(t, filepath.Join(roots.Tmp, "root-1"))
	assert.NoDirExists(t, filepath.Join(roots.Sessions, "root-1"))

	// The orphan sweep (row gone, idle past the cutoff) removes it; "other" is kept.
	res = roots.Cleanup(CleanupSpec{Cutoff: time.Now().Add(time.Hour), Keep: []string{"other"}})
	assert.Empty(t, res.Failures)
	assert.NoDirExists(t, filepath.Join(roots.Shared, "root-1"))
	assert.DirExists(t, filepath.Join(roots.Shared, "other"))
}
