package sshagent

import (
	"fmt"
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

func TestClearKeys_ExternalAgentIteratesSSHDir(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Create fake ssh-add script that records args
	binDir := t.TempDir()
	logFile := filepath.Join(binDir, "calls.log")
	scriptPath := filepath.Join(binDir, "ssh-add")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\nexit 0\n", logFile)
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	// Create dummy socket
	sockPath := filepath.Join(tempHome, "agent.sock")
	require.NoError(t, os.WriteFile(sockPath, []byte{}, 0600))
	t.Setenv("SSH_AUTH_SOCK", sockPath)

	sshDir := filepath.Join(tempHome, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "key"), []byte("priv1"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("priv2"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("priv3"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "key.pub"), []byte("pub"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "config"), []byte("cfg"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte("hosts"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "authorized_keys"), []byte("auth"), 0644))

	err := ClearKeys()
	assert.NoError(t, err)

	logged, err := os.ReadFile(logFile)
	require.NoError(t, err)
	logStr := string(logged)
	assert.Contains(t, logStr, "-d "+filepath.Join(sshDir, "key"))
	assert.Contains(t, logStr, "-d "+filepath.Join(sshDir, "id_ed25519"))
	assert.Contains(t, logStr, "-d "+filepath.Join(sshDir, "id_rsa"))
	assert.NotContains(t, logStr, "key.pub")
	assert.NotContains(t, logStr, "config")
	assert.NotContains(t, logStr, "known_hosts")
	assert.NotContains(t, logStr, "authorized_keys")
}
