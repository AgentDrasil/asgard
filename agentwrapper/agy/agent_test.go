package agy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
)

func writeTestContract(t *testing.T, contract *common.Contract) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "AW_AGENTS.md")
	require.NoError(t, os.WriteFile(path, []byte(common.Render(contract)), 0644))
	t.Setenv(common.EnvVar, path)
}

func TestInstallContract_NoContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(common.EnvVar, filepath.Join(home, "missing.md"))

	cleanup, err := InstallContract()
	require.NoError(t, err)
	cleanup()

	_, err = os.Stat(filepath.Join(home, ".gemini", "GEMINI.md"))
	assert.True(t, os.IsNotExist(err))
}

func TestInstallContract_TeamAndRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTestContract(t, &common.Contract{
		AgentID: "builder", AgentName: "Builder", Team: "core", Body: "AGY_CONTRACT_BODY",
	})

	cleanup, err := InstallContract()
	require.NoError(t, err)

	target := filepath.Join(home, ".gemini", "GEMINI.md")
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "managed by aw")
	assert.Contains(t, content, "AGY_CONTRACT_BODY")
	assert.Contains(t, content, "/bin/ask-user")
	assert.Contains(t, content, "call-peer")

	cleanup()

	// Not pre-existing: removed again.
	_, err = os.Stat(target)
	assert.True(t, os.IsNotExist(err))
}

func TestInstallContract_RestoresUserContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTestContract(t, &common.Contract{AgentID: "x", Body: "x"})

	geminiDir := filepath.Join(home, ".gemini")
	require.NoError(t, os.MkdirAll(geminiDir, 0755))
	target := filepath.Join(geminiDir, "GEMINI.md")
	require.NoError(t, os.WriteFile(target, []byte("# my gemini rules"), 0644))

	cleanup, err := InstallContract()
	require.NoError(t, err)

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "# my gemini rules")

	cleanup()

	data, err = os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "# my gemini rules", string(data))
}
