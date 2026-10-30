package sshagent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClearKeys_NoSocketGraceful(t *testing.T) {
	// ClearKeys with no socket should return nil without error
	t.Setenv("SSH_AUTH_SOCK", "")
	err := ClearKeys()
	assert.NoError(t, err)

	// ClearKeys with non-existent socket should return nil
	t.Setenv("SSH_AUTH_SOCK", "/nonexistent/socket.sock")
	err = ClearKeys()
	assert.NoError(t, err)
}

func TestRemoveKey_NoSocketGraceful(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	err := RemoveKey("/some/key")
	assert.NoError(t, err)

	t.Setenv("SSH_AUTH_SOCK", "/nonexistent/socket.sock")
	err = RemoveKey("/some/key")
	assert.NoError(t, err)
}

func TestAddKeysFromDir_SkipsNonPrivateKeys(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	// Write public key, config, known_hosts, authorized_keys
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "key.pub"), []byte("ssh-rsa pub"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "config"), []byte("Host *"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "known_hosts"), []byte("hosts"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "authorized_keys"), []byte("auth"), 0644))

	// Subdir should be ignored
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "subdir"), 0755))

	// With only non-private files, AddKeysFromDir should succeed without error
	err := AddKeysFromDir(tempDir)
	assert.NoError(t, err)
}

func TestAddKeysFromDir_NonExistentDir(t *testing.T) {
	t.Parallel()

	err := AddKeysFromDir("/nonexistent/dir/for/ssh")
	assert.Error(t, err)
}
