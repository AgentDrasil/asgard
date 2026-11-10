package bwrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// worktreeFixture lays out the directories the runner creates before a run.
func worktreeFixture(t *testing.T) (*agentspec.WorktreeRun, string) {
	t.Helper()
	root := t.TempDir()
	wr := &agentspec.WorktreeRun{
		GitDir:       filepath.Join(root, "repo", ".git"),
		Name:         "repo-c1",
		RefNamespace: "asgard/c1",
		ObjectsDir:   filepath.Join(root, "priv", "c1"),
	}
	for _, d := range []string{
		filepath.Join(wr.GitDir, "worktrees", wr.Name),
		filepath.Join(wr.GitDir, "refs", "heads", "asgard", "c1"),
		filepath.Join(wr.GitDir, "logs", "refs", "heads", "asgard", "c1"),
		filepath.Join(wr.GitDir, "objects"),
		wr.ObjectsDir,
	} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	return wr, root
}

func TestAppendWorktreeArgs_NoWorktree(t *testing.T) {
	t.Parallel()

	in := []string{"--x"}
	out, err := appendWorktreeArgs(in, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, in, out)

	out, err = appendWorktreeArgs(in, &agentspec.AgentConfig{}, nil)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestAppendWorktreeArgs_Binds(t *testing.T) {
	t.Parallel()

	wr, _ := worktreeFixture(t)
	cfg := &agentspec.AgentConfig{WorktreeRun: wr}
	args, err := appendWorktreeArgs(nil, cfg, nil)
	require.NoError(t, err)
	s := strings.Join(args, " ")

	// The main checkout and its git dir are read-only and come before every
	// writable sub-bind.
	mainDir := filepath.Dir(wr.GitDir)
	mainIdx := strings.Index(s, "--ro-bind "+mainDir+" "+mainDir)
	require.GreaterOrEqual(t, mainIdx, 0, "the main working tree is read-only too")
	roIdx := strings.Index(s, "--ro-bind "+wr.GitDir+" "+wr.GitDir)
	require.Greater(t, roIdx, mainIdx)
	for _, rw := range []string{
		filepath.Join(wr.GitDir, "worktrees", "repo-c1"),
		filepath.Join(wr.GitDir, "refs", "heads", "asgard", "c1"),
		filepath.Join(wr.GitDir, "logs", "refs", "heads", "asgard", "c1"),
		wr.ObjectsDir,
	} {
		idx := strings.Index(s, "--bind "+rw+" "+rw)
		require.GreaterOrEqual(t, idx, 0, rw)
		assert.Greater(t, idx, roIdx, "%s must follow the read-only git dir", rw)
	}

	// Nothing else under the git dir (shared objects, other refs, config) is writable.
	assert.NotContains(t, s, "--bind "+wr.GitDir+" ")
	assert.NotContains(t, s, "--bind "+mainDir+" ")
	assert.NotContains(t, s, "--bind "+filepath.Join(wr.GitDir, "objects")+" ")
	assert.NotContains(t, s, "--bind "+filepath.Join(wr.GitDir, "refs")+" ")

	assert.Contains(t, s, "--setenv GIT_OBJECT_DIRECTORY "+wr.ObjectsDir)
	assert.Contains(t, s, "--setenv GIT_ALTERNATE_OBJECT_DIRECTORIES "+filepath.Join(wr.GitDir, "objects"))
	assert.Contains(t, s, "--setenv GIT_CONFIG_KEY_0 gc.auto")
	assert.Contains(t, s, "--setenv GIT_CONFIG_KEY_1 maintenance.auto")
}

func TestAppendWorktreeArgs_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		modify    func(wr *agentspec.WorktreeRun, root string)
		ro        func(wr *agentspec.WorktreeRun) []string
		errSubstr string
	}{
		{"relative git dir", func(wr *agentspec.WorktreeRun, _ string) { wr.GitDir = "repo/.git" }, nil, "absolute"},
		{"relative objects dir", func(wr *agentspec.WorktreeRun, _ string) { wr.ObjectsDir = "priv" }, nil, "absolute"},
		{"empty name", func(wr *agentspec.WorktreeRun, _ string) { wr.Name = "" }, nil, "invalid worktree name"},
		{"name with slash", func(wr *agentspec.WorktreeRun, _ string) { wr.Name = "a/b" }, nil, "invalid worktree name"},
		{"dotdot name", func(wr *agentspec.WorktreeRun, _ string) { wr.Name = ".." }, nil, "invalid worktree name"},
		{"empty namespace", func(wr *agentspec.WorktreeRun, _ string) { wr.RefNamespace = "" }, nil, "ref namespace"},
		{"namespace escapes", func(wr *agentspec.WorktreeRun, _ string) { wr.RefNamespace = "asgard/../.." }, nil, "ref namespace"},
		{"namespace lock suffix", func(wr *agentspec.WorktreeRun, _ string) { wr.RefNamespace = "asgard/c1.lock" }, nil, "ref namespace"},
		{"namespace absolute", func(wr *agentspec.WorktreeRun, _ string) { wr.RefNamespace = "/asgard/c1" }, nil, "ref namespace"},
		{
			"missing refs dir",
			func(wr *agentspec.WorktreeRun, _ string) {
				require.NoError(t, os.Remove(filepath.Join(wr.GitDir, "refs", "heads", "asgard", "c1")))
			},
			nil, "worktree path",
		},
		{
			"git dir under readonly mount",
			func(*agentspec.WorktreeRun, string) {},
			func(wr *agentspec.WorktreeRun) []string { return []string{filepath.Dir(wr.GitDir)} },
			"readonly mount",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wr, root := worktreeFixture(t)
			tt.modify(wr, root)
			var ro []string
			if tt.ro != nil {
				ro = tt.ro(wr)
			}
			_, err := appendWorktreeArgs(nil, &agentspec.AgentConfig{WorktreeRun: wr}, ro)
			require.ErrorContains(t, err, tt.errSubstr)
		})
	}
}

func TestBuildSandboxArgs_Worktree(t *testing.T) {
	wr, root := worktreeFixture(t)
	wtDir := filepath.Join(root, "wt", "repo-c1")
	require.NoError(t, os.MkdirAll(wtDir, 0o755))

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ASGARD_ROOT", filepath.Join(home, "asgard"))
	t.Setenv("ASGARD_SESSION_DATA", filepath.Join(home, "asgard", "session-data"))
	for _, sub := range []string{".cache", ".config", ".local", ".ssh", "asgard/config"} {
		require.NoError(t, os.MkdirAll(filepath.Join(home, sub), 0o755))
	}

	mainRepo := filepath.Dir(wr.GitDir)
	cfg := &agentspec.AgentConfig{
		ID: "a", Name: "a", Description: "a",
		RunDirs:     []string{wtDir},
		WorktreeRun: wr,
	}

	args, err := buildSandboxArgs(cfg, "", agentspec.CLITarget{}, false, "", wtDir, "", "c1", "", "")
	require.NoError(t, err)
	s := strings.Join(args, " ")

	assert.Contains(t, s, "--bind "+wtDir+" "+wtDir)
	assert.NotContains(t, s, "--bind "+mainRepo+" ", "main checkout must not be writable")
	assert.Contains(t, s, "--ro-bind "+wr.GitDir+" "+wr.GitDir)
	assert.Contains(t, s, "--setenv GIT_OBJECT_DIRECTORY "+wr.ObjectsDir)

	cmd, err := commandForCommandExec(cfg, wtDir, "", "", "c1", "", false, nil)
	require.NoError(t, err)
	cs := strings.Join(cmd.Args, " ")
	assert.Contains(t, cs, "--ro-bind "+wr.GitDir+" "+wr.GitDir)
	assert.Contains(t, cs, "--bind "+wr.ObjectsDir+" "+wr.ObjectsDir)
	assert.Contains(t, cs, "--setenv GIT_OBJECT_DIRECTORY "+wr.ObjectsDir)
}

func TestWriteContractFile_Worktree(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	md := filepath.Join(dir, "AGENTS.md")
	require.NoError(t, os.WriteFile(md, []byte("# Own instructions"), 0o644))
	cfg := &agentspec.AgentConfig{ID: "a", Name: "a"}

	plain, err := writeContractFile(dir, cfg, md, "")
	require.NoError(t, err)
	data, err := os.ReadFile(plain)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "asgard-merge", "no worktree, no worktree instructions")

	cfg.WorktreeRun = &agentspec.WorktreeRun{RefNamespace: "asgard/c1"}
	path, err := writeContractFile(dir, cfg, md, "")
	require.NoError(t, err)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	body := string(data)
	assert.Contains(t, body, "# Own instructions", "the agent's own instructions stay")
	assert.Contains(t, body, "asgard/c1/")
	assert.Contains(t, body, "asgard-merge")
	assert.Contains(t, body, "git worktree list")
	assert.Less(t, strings.Index(body, "# Own instructions"), strings.Index(body, "asgard-merge"))
}

func TestWorktreePromptSharedHasNoMerge(t *testing.T) {
	wr := &agentspec.WorktreeRun{RefNamespace: "asgard/owner"}
	assert.Contains(t, worktreePrompt(wr), "asgard-merge")

	wr.Shared = true
	prompt := worktreePrompt(wr)
	assert.NotContains(t, prompt, "asgard-merge")
	assert.Contains(t, prompt, "Do not merge")
	assert.Contains(t, prompt, "asgard/owner/")
}
