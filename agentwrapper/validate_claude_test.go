package agentwrapper

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateClaudeSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ASGARD_ACCOUNTS_DIR", filepath.Join(home, "accounts"))
	orig := homeDirFn
	homeDirFn = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDirFn = orig })

	assert.ErrorContains(t, ValidateClaudeSetup(), "aw claude login <alias>")

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", ".credentials.json"), 0o755))
	assert.ErrorContains(t, ValidateClaudeSetup(), "is a directory")

	require.NoError(t, os.RemoveAll(filepath.Join(home, ".claude", ".credentials.json")))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o600))
	assert.NoError(t, ValidateClaudeSetup())
}

func TestValidateClaudeSetup_AccountCredentials(t *testing.T) {
	home := t.TempDir()
	accts := filepath.Join(home, "accounts")
	t.Setenv("ASGARD_ACCOUNTS_DIR", accts)
	orig := homeDirFn
	homeDirFn = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDirFn = orig })

	require.NoError(t, os.MkdirAll(filepath.Join(accts, "claude", "work"), 0o700))
	assert.ErrorContains(t, ValidateClaudeSetup(), "aw claude login <alias>")

	require.NoError(t, os.WriteFile(filepath.Join(accts, "claude", "work", ".credentials.json"), []byte("{}"), 0o600))
	assert.NoError(t, ValidateClaudeSetup())
}

func TestClaudeModelCandidates(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"opus/high", "opus"}, ModelCandidates("claude", "opus/high"))
	assert.Equal(t, []string{"sonnet"}, ModelCandidates("claude", "sonnet"))
	assert.True(t, IsKnownModel("claude", "fable"))
	assert.True(t, IsKnownModel("claude", "haiku/low"))
	assert.Equal(t, 1048576, GetModelContextWindow("claude", "opus/high"))
}
