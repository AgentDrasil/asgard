package session

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSession_FileLock_Exclusive(t *testing.T) {
	// Sequential execution: lock operations are time- and file-dependent.
	tempDir := t.TempDir()
	m1 := New(tempDir)
	m2 := New(tempDir)

	sf1, err := m1.Create(tempDir, nil)
	require.NoError(t, err)
	require.NotEmpty(t, sf1.Path())

	// Write first message so file exists on disk
	_, err = sf1.AppendMessage(testUser("hello world"))
	require.NoError(t, err)
	sessionPath := sf1.Path()

	// Attempting to open the same session path concurrently from m2 must fail with ErrSessionLocked
	_, err = m2.Open(sessionPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionLocked)

	// Close first session instance
	expectedID := sf1.Header().ID
	require.NoError(t, sf1.Close())

	// Header and metadata must remain readable after Close
	assert.Equal(t, expectedID, sf1.Header().ID)
	assert.Equal(t, sessionPath, sf1.Path())

	// Idempotent Close check
	require.NoError(t, sf1.Close())

	// Now m2 must be able to open and lock the session successfully
	sf2, err := m2.Open(sessionPath)
	require.NoError(t, err)
	require.NotNil(t, sf2)
	assert.Equal(t, expectedID, sf2.Header().ID)

	// Now sf1 (or m1) trying to open again should fail because sf2 holds the lock
	_, err = m1.Open(sessionPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionLocked)

	// Cleanup sf2
	require.NoError(t, sf2.Close())
}

func TestSession_InMemory_NoLock(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	m := New(tempDir)

	sf := m.InMemory(tempDir)
	require.NotNil(t, sf)
	assert.Empty(t, sf.Path())

	_, err := sf.AppendMessage(testUser("in-memory message"))
	require.NoError(t, err)

	require.NoError(t, sf.Flush())
	require.NoError(t, sf.Close())

	// Verify no lock file or session file was created anywhere in tempDir
	matches, err := filepath.Glob(filepath.Join(tempDir, "*"))
	require.NoError(t, err)
	assert.Empty(t, matches)
}
