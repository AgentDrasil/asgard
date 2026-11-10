package bwrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func TestSharedDirBoundInAgentAndCommandSandboxes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ASGARD_ROOT", filepath.Join(home, "asgard"))
	t.Setenv("ASGARD_SESSION_DATA", filepath.Join(home, "asgard", "session-data"))
	for _, sub := range []string{".cache", ".config", ".local", ".ssh", "asgard/config"} {
		require.NoError(t, os.MkdirAll(filepath.Join(home, sub), 0o755))
	}
	shared := filepath.Join(home, "asgard", "session-data", "shared", "root-1")
	runDir := t.TempDir()

	for _, readonly := range []bool{false, true} {
		cfg := &agentspec.AgentConfig{ID: "a", Name: "a", Description: "a", RunDirs: []string{runDir}, SharedChatID: "root-1"}
		if readonly {
			cfg.LockMode = agentspec.LockModeReadOnly
		}
		args, err := buildSandboxArgs(cfg, "", agentspec.CLITarget{}, false, "", runDir, "", "child", "", "")
		require.NoError(t, err)
		assert.GreaterOrEqual(t, indexOfSeq(args, "--bind", shared, "/shared"), 0, "agent sandbox, readonly=%v: %v", readonly, args)

		cmd, err := commandForCommandExec(cfg, runDir, "", "", "child", "", false, nil)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--bind", shared, "/shared"), 0, "command sandbox, readonly=%v: %v", readonly, cmd.Args)
	}
	assert.DirExists(t, shared)

	// No spawn tree: nothing is mounted.
	cfg := &agentspec.AgentConfig{ID: "a", Name: "a", Description: "a", RunDirs: []string{runDir}}
	args, err := buildSandboxArgs(cfg, "", agentspec.CLITarget{}, false, "", runDir, "", "child", "", "")
	require.NoError(t, err)
	assert.Equal(t, -1, indexOfSeq(args, "--bind", shared, "/shared"))
	for _, a := range args {
		assert.NotEqual(t, "/shared", a)
	}
}

func TestAppendSharedArgsRejectsBadIDs(t *testing.T) {
	t.Setenv("ASGARD_SESSION_DATA", t.TempDir())
	for _, id := range []string{"..", ".", "a/b", "../x"} {
		_, err := appendSharedArgs(nil, &agentspec.AgentConfig{SharedChatID: id})
		assert.Error(t, err, id)
	}
}

func TestReadOnlyPromptMentionsSharedOnlyWithSharedDir(t *testing.T) {
	dir := t.TempDir()
	read := func(cfg *agentspec.AgentConfig) string {
		p, err := writeContractFile(dir, cfg, "", "")
		require.NoError(t, err)
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		return string(b)
	}
	plain := read(&agentspec.AgentConfig{ID: "a", LockMode: agentspec.LockModeReadOnly})
	assert.Contains(t, plain, ReadOnlyWorkspacePrompt)
	assert.NotContains(t, plain, ReadOnlySharedPrompt)

	shared := read(&agentspec.AgentConfig{ID: "a", LockMode: agentspec.LockModeReadOnly, SharedChatID: "root-1"})
	assert.Contains(t, shared, ReadOnlyWorkspacePrompt)
	assert.Contains(t, shared, ReadOnlySharedPrompt)

	writable := read(&agentspec.AgentConfig{ID: "a", SharedChatID: "root-1"})
	assert.NotContains(t, writable, ReadOnlySharedPrompt)
}
