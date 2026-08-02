package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureWorkspaceTrusted(t *testing.T) {
	tempHome := t.TempDir()

	oldHome := os.Getenv("HOME")
	err := os.Setenv("HOME", tempHome)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Setenv("HOME", oldHome)
	})

	cliDir := filepath.Join(tempHome, ".gemini", "antigravity-cli")
	err = os.MkdirAll(cliDir, 0755)
	require.NoError(t, err)

	settingsPath := filepath.Join(cliDir, "settings.json")

	// 1. Write an initial settings.json with a trusted workspace
	initialSettings := `{
  "model": "test-model",
  "trustedWorkspaces": [
    "/some/trusted/path"
  ]
}`
	err = os.WriteFile(settingsPath, []byte(initialSettings), 0644)
	require.NoError(t, err)

	// 2. Checking the already trusted path should succeed without modifying the file
	err = ensureWorkspaceTrusted("/some/trusted/path")
	require.NoError(t, err)

	// 3. Checking an untrusted path should add it to settings.json and succeed
	untrustedPath, err := filepath.Abs(".")
	require.NoError(t, err)
	untrustedPath = filepath.Clean(untrustedPath)

	err = ensureWorkspaceTrusted(untrustedPath)
	require.NoError(t, err)

	// 4. Verify settings.json was updated and contains the new path while preserving other keys
	updatedData, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var config struct {
		Model             string   `json:"model"`
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	err = json.Unmarshal(updatedData, &config)
	require.NoError(t, err)

	assert.Equal(t, "test-model", config.Model)
	assert.Contains(t, config.TrustedWorkspaces, "/some/trusted/path")
	assert.Contains(t, config.TrustedWorkspaces, untrustedPath)
}
