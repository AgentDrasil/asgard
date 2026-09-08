package common

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func takeoverTarget(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "AMBIENT.md")
}

func TestFileTakeover_AbsentFileRemovedOnRestore(t *testing.T) {
	path := takeoverTarget(t)

	takeover, err := BeginFileTakeover(path)
	require.NoError(t, err)

	// The caller writes managed content into the absent file.
	require.NoError(t, os.WriteFile(path, []byte(ManagedMarker+"\nbody"), 0644))

	takeover.Restore()

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "file should be removed again after restore")
}

func TestFileTakeover_UserContentRestored(t *testing.T) {
	path := takeoverTarget(t)
	require.NoError(t, os.WriteFile(path, []byte("# user rules"), 0644))

	takeover, err := BeginFileTakeover(path)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte(ManagedMarker+"\nmanaged"), 0644))

	takeover.Restore()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# user rules", string(data))
}

// TestFileTakeover_ReadOnlyFile reproduces the "permission denied" class of
// failures seen with host users' read-only ambient instruction files
// (e.g. ~/.gemini/GEMINI.md): the takeover must chmod the file writable,
// allow the overwrite, and restore both the content and the original mode.
func TestFileTakeover_ReadOnlyFile(t *testing.T) {
	path := takeoverTarget(t)
	require.NoError(t, os.WriteFile(path, []byte("# user rules"), 0444))

	takeover, err := BeginFileTakeover(path)
	require.NoError(t, err)

	// Overwriting the read-only file must now succeed without EACCES.
	require.NoError(t, os.WriteFile(path, []byte(ManagedMarker+"\nmanaged"), 0644))

	takeover.Restore()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# user rules", string(data))

	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0444), st.Mode().Perm(), "original read-only mode must be restored")
}

// TestFileTakeover_CrashLeftoverRemovedOnRestore ensures a leftover managed
// file from a previous crashed run is treated as originally absent (removed
// by Restore) instead of being preserved as "user content".
func TestFileTakeover_CrashLeftoverRemovedOnRestore(t *testing.T) {
	path := takeoverTarget(t)
	require.NoError(t, os.WriteFile(path, []byte(ManagedMarker+"\nstale takeover"), 0644))

	takeover, err := BeginFileTakeover(path)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte(ManagedMarker+"\nfresh"), 0644))

	takeover.Restore()

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "leftover managed file must be removed, not restored")
}
