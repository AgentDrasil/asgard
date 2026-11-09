package worktree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanFor(t *testing.T) {
	t.Parallel()

	p, err := PlanFor("/src/wt", "/src/my repo", "c1", "/sess/c1")
	require.NoError(t, err)
	assert.Equal(t, "/src/wt/my_repo-c1", p.Path)
	assert.Equal(t, "my_repo-c1", p.Name)
	assert.Equal(t, "asgard/c1", p.RefNamespace)
	assert.Equal(t, "asgard/c1/work", p.Branch)
	assert.Equal(t, "/sess/c1/git-objects", p.ObjectsDir)
	assert.Equal(t, "/src/my repo/.git", p.GitDir())

	tests := []struct {
		name                  string
		root, repo, chat, obj string
		errSubstr             string
	}{
		{"bad chat id", "/w", "/r", "../x", "/o", "invalid chat id"},
		{"empty chat id", "/w", "/r", "", "/o", "invalid chat id"},
		{"relative root", "w", "/r", "c1", "/o", "absolute"},
		{"relative repo", "/w", "r", "c1", "/o", "absolute"},
		{"relative objects", "/w", "/r", "c1", "o", "absolute"},
		{"root inside repo", "/r/wt", "/r", "c1", "/o", "overlaps"},
		{"repo inside root", "/w", "/w/r", "c1", "/o", "overlaps"},
		{"root is repo", "/r", "/r", "c1", "/o", "overlaps"},
		{"repo is slash", "/w", "/", "c1", "/o", "worktree of /"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := PlanFor(tt.root, tt.repo, tt.chat, tt.obj)
			require.ErrorContains(t, err, tt.errSubstr)
		})
	}
}

func TestEnsure_CreatesAndReuses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()

	wr, err := f.plan.Ensure(ctx)
	require.NoError(t, err)
	assert.Equal(t, f.plan.GitDir(), wr.GitDir)
	assert.Equal(t, f.plan.Name, wr.Name)

	// The worktree is checked out on the chat's branch with the repo content.
	assert.Equal(t, "asgard/c1/work", gitT(t, f.plan.Path, "branch", "--show-current"))
	data, err := os.ReadFile(filepath.Join(f.plan.Path, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "a\n", string(data))
	for _, d := range []string{
		f.plan.ObjectsDir,
		filepath.Join(f.plan.GitDir(), "refs", "heads", "asgard", "c1"),
		filepath.Join(f.plan.GitDir(), "logs", "refs", "heads", "asgard", "c1"),
		filepath.Join(f.plan.GitDir(), "worktrees", f.plan.Name),
	} {
		assert.DirExists(t, d)
	}

	// Work done in the worktree survives a second Ensure (resumed chat).
	require.NoError(t, os.WriteFile(filepath.Join(f.plan.Path, "b.txt"), []byte("b"), 0o644))
	gitT(t, f.plan.Path, "add", ".")
	gitT(t, f.plan.Path, "commit", "-m", "b")
	_, err = f.plan.Ensure(ctx)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(f.plan.Path, "b.txt"))
}

func TestEnsure_Refusals(t *testing.T) {
	t.Parallel()

	t.Run("not a git repo", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		require.NoError(t, os.RemoveAll(f.plan.GitDir()))
		_, err := f.plan.Ensure(t.Context())
		require.ErrorIs(t, err, ErrNotGitRepo)
	})

	t.Run("git dir is a file", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		require.NoError(t, os.RemoveAll(f.plan.GitDir()))
		require.NoError(t, os.WriteFile(f.plan.GitDir(), []byte("gitdir: /elsewhere"), 0o644))
		_, err := f.plan.Ensure(t.Context())
		require.ErrorIs(t, err, ErrNotGitRepo)
	})

	t.Run("dangerous repo config", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		gitT(t, f.repo, "config", "core.fsmonitor", "/tmp/evil")
		_, err := f.plan.Ensure(t.Context())
		require.ErrorContains(t, err, "not allowed on the runner")
		assert.NoDirExists(t, f.plan.Path)
	})

	t.Run("path taken by a plain directory", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		require.NoError(t, os.MkdirAll(f.plan.Path, 0o755))
		_, err := f.plan.Ensure(t.Context())
		require.ErrorContains(t, err, "not a git worktree")
	})

	t.Run("path belongs to another repository", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		require.NoError(t, os.MkdirAll(f.plan.Path, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(f.plan.Path, ".git"), []byte("gitdir: /other/.git/worktrees/x\n"), 0o644))
		_, err := f.plan.Ensure(t.Context())
		require.ErrorContains(t, err, "another repository")
	})
}

func TestEnsure_HooksDoNotRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(f.plan.GitDir(), "hooks", "post-checkout")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o755))
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755))

	_, err := f.plan.Ensure(t.Context())
	require.NoError(t, err)
	assert.NoFileExists(t, marker, "repository hooks must not run on the runner")
}

func TestRemove(t *testing.T) {
	t.Parallel()

	t.Run("clean and merged", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		_, err := f.plan.Ensure(t.Context())
		require.NoError(t, err)

		require.NoError(t, f.plan.Remove(t.Context(), false))
		assert.NoDirExists(t, f.plan.Path)
		assert.NoDirExists(t, f.plan.ObjectsDir)
		assert.Empty(t, gitT(t, f.repo, "branch", "--list", f.plan.Branch))
		// Removing again is a no-op.
		require.NoError(t, f.plan.Remove(t.Context(), false))
	})

	t.Run("dirty worktree is kept", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		_, err := f.plan.Ensure(t.Context())
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(f.plan.Path, "dirty.txt"), []byte("x"), 0o644))

		require.Error(t, f.plan.Remove(t.Context(), false))
		assert.DirExists(t, f.plan.Path)

		require.NoError(t, f.plan.Remove(t.Context(), true))
		assert.NoDirExists(t, f.plan.Path)
	})

	t.Run("unmerged branch is kept", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		_, err := f.plan.Ensure(t.Context())
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(f.plan.Path, "c.txt"), []byte("c"), 0o644))
		gitT(t, f.plan.Path, "add", ".")
		gitT(t, f.plan.Path, "commit", "-m", "c")

		err = f.plan.Remove(t.Context(), false)
		require.ErrorContains(t, err, "deleting branch")
		assert.NotEmpty(t, gitT(t, f.repo, "branch", "--list", f.plan.Branch), "unmerged work must survive")
	})
}
