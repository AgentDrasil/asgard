package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

func writeContract(t *testing.T, home string, contract *common.Contract) {
	t.Helper()
	contractDir := filepath.Join(home, "session")
	require.NoError(t, os.MkdirAll(contractDir, 0755))
	contractPath := filepath.Join(contractDir, "AW_AGENTS.md")
	require.NoError(t, os.WriteFile(contractPath, []byte(common.Render(contract)), 0644))
	t.Setenv(common.EnvVar, contractPath)
}

// withSandboxHome redirects HOME so PrepareAgent writes into a temp dir.
func withSandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestPrepareAgent_NoContract(t *testing.T) {
	home := withSandboxHome(t)
	t.Setenv(common.EnvVar, filepath.Join(home, "missing.md"))

	name, cleanup, err := PrepareAgent(types.PromptOptions{AgentID: "intend"})
	require.NoError(t, err)
	assert.Empty(t, name)
	cleanup() // no-op, must not error
}

func TestPrepareAgent_FullAccessTeam(t *testing.T) {
	home := withSandboxHome(t)
	writeContract(t, home, &common.Contract{
		AgentID:    "builder",
		AgentName:  "Builder",
		ToolAccess: types.ToolAccessFull,
		Team:       "core",
		Body:       "Build things.\n",
	})

	name, cleanup, err := PrepareAgent(types.PromptOptions{})
	require.NoError(t, err)
	assert.Equal(t, "builder", name)

	agentFile := filepath.Join(home, ".config", "opencode", "agents", "builder.md")
	data, err := os.ReadFile(agentFile)
	require.NoError(t, err)
	content := string(data)

	// Frontmatter
	assert.Contains(t, content, "description: Builder")
	assert.Contains(t, content, "mode: primary")
	// Base denies: question always, task for team agents (pattern-map form —
	// a bare deny would be bypassed by `opencode run --auto`; question keeps
	// the bare form since it rejects the pattern-map schema)
	assert.Contains(t, content, "question: deny")
	assert.Contains(t, content, "task:\n    \"*\": deny")
	// Full access: no edit/bash restrictions
	assert.NotContains(t, content, "edit:")
	assert.NotContains(t, content, "bash:")
	// Body: header + peer header + contract body
	assert.Contains(t, content, "/bin/ask-user")
	assert.Contains(t, content, "call-peer")
	assert.Contains(t, content, "Build things.")

	// Ambient AGENTS.md was neutralized during the run.
	ambient := filepath.Join(home, ".config", "opencode", "AGENTS.md")
	ambientData, err := os.ReadFile(ambient)
	require.NoError(t, err)
	assert.Contains(t, string(ambientData), "managed by aw")

	cleanup()

	// Agent file removed, ambient file (absent before) removed.
	_, err = os.Stat(agentFile)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(ambient)
	assert.True(t, os.IsNotExist(err))
}

func TestPrepareAgent_DocOnlyNoTeam(t *testing.T) {
	home := withSandboxHome(t)
	writeContract(t, home, &common.Contract{
		AgentID:    "intend",
		AgentName:  "Intend",
		ToolAccess: types.ToolAccessDocOnly,
		Body:       "Analyze only.\n",
	})

	name, cleanup, err := PrepareAgent(types.PromptOptions{})
	require.NoError(t, err)
	assert.Equal(t, "intend", name)
	defer cleanup()

	data, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "agents", "intend.md"))
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "question: deny")
	// No team: native task delegation stays available (peer header absent).
	assert.NotContains(t, content, "task:")
	assert.NotContains(t, content, "call-peer")
	// Doc-only for opencode only disables shell execution; editing stays.
	assert.Contains(t, content, "bash:\n    \"*\": deny")
	assert.NotContains(t, content, "edit:")
	assert.Contains(t, content, "Analyze only.")
}

func TestPrepareAgent_OptsAgentIDWins(t *testing.T) {
	home := withSandboxHome(t)
	writeContract(t, home, &common.Contract{AgentID: "from-contract", Body: "x"})

	name, cleanup, err := PrepareAgent(types.PromptOptions{AgentID: "From CLI"})
	require.NoError(t, err)
	assert.Equal(t, "from-cli", name)
	defer cleanup()

	_, err = os.Stat(filepath.Join(home, ".config", "opencode", "agents", "from-cli.md"))
	require.NoError(t, err)
}

func TestPrepareAgent_RestoresPreexistingAmbient(t *testing.T) {
	home := withSandboxHome(t)
	writeContract(t, home, &common.Contract{AgentID: "intend", Body: "x"})

	opencodeDir := filepath.Join(home, ".config", "opencode")
	require.NoError(t, os.MkdirAll(opencodeDir, 0755))
	ambient := filepath.Join(opencodeDir, "AGENTS.md")
	require.NoError(t, os.WriteFile(ambient, []byte("# my own instructions"), 0644))

	_, cleanup, err := PrepareAgent(types.PromptOptions{})
	require.NoError(t, err)

	// During the run the ambient file is neutralized, not merged.
	data, err := os.ReadFile(ambient)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "# my own instructions")

	cleanup()

	// User content restored byte-for-byte.
	data, err = os.ReadFile(ambient)
	require.NoError(t, err)
	assert.Equal(t, "# my own instructions", string(data))
}

// TestPrepareAgent_ReadOnlyAmbient reproduces the host-environment failure
// where the user's read-only ~/.config/opencode/AGENTS.md made the run die
// with "neutralizing ambient file: permission denied": the takeover must
// chmod the file writable for the run and restore content and mode after.
func TestPrepareAgent_ReadOnlyAmbient(t *testing.T) {
	home := withSandboxHome(t)
	writeContract(t, home, &common.Contract{AgentID: "intend", Body: "x"})

	opencodeDir := filepath.Join(home, ".config", "opencode")
	require.NoError(t, os.MkdirAll(opencodeDir, 0755))
	ambient := filepath.Join(opencodeDir, "AGENTS.md")
	require.NoError(t, os.WriteFile(ambient, []byte("# my own instructions"), 0444))

	_, cleanup, err := PrepareAgent(types.PromptOptions{})
	require.NoError(t, err)

	data, err := os.ReadFile(ambient)
	require.NoError(t, err)
	assert.Contains(t, string(data), "managed by aw")

	cleanup()

	data, err = os.ReadFile(ambient)
	require.NoError(t, err)
	assert.Equal(t, "# my own instructions", string(data))

	st, err := os.Stat(ambient)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0444), st.Mode().Perm())
}
