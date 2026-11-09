package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentGit runs git the way the agent's sandbox does: inside the worktree,
// writing to its private object directory with the shared store as alternate.
func agentGit(t *testing.T, p Plan, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = p.Path
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_OBJECT_DIRECTORY="+p.ObjectsDir,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(p.GitDir(), "objects"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "agent git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func agentCommit(t *testing.T, p Plan, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(p.Path, name)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Path, name), []byte(content), 0o644))
	agentGit(t, p, "add", ".")
	agentGit(t, p, "commit", "-m", "add "+name)
}

func ensured(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	_, err := f.plan.Ensure(t.Context())
	require.NoError(t, err)
	return f
}

func TestMerge_FastForwards(t *testing.T) {
	t.Parallel()
	f := ensured(t)
	agentCommit(t, f.plan, "b.txt", "b\n")
	agentCommit(t, f.plan, "d/c.txt", "c\n")
	tip := agentGit(t, f.plan, "rev-parse", "HEAD")

	// The commits exist only in the private store until the merge.
	cmd := exec.Command("git", "cat-file", "-e", tip)
	cmd.Dir = f.repo
	require.Error(t, cmd.Run())

	res, err := f.plan.Merge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, MergeMerged, res.Status)
	assert.Equal(t, "main", res.Target)
	assert.Equal(t, tip, gitT(t, f.repo, "rev-parse", "main"))
	assert.FileExists(t, filepath.Join(f.repo, "b.txt"))
	assert.FileExists(t, filepath.Join(f.repo, "d", "c.txt"))
	assert.Empty(t, gitT(t, f.repo, "status", "--porcelain"))
	// The shared store is self-sufficient and consistent.
	gitT(t, f.repo, "fsck", "--strict")
	assert.Contains(t, res.Guidance(), "Merged into main")

	// Merging again has nothing to do; the worktree is still usable.
	res, err = f.plan.Merge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, MergeNothing, res.Status)
	assert.DirExists(t, f.plan.Path)
}

func TestMerge_MainMoved(t *testing.T) {
	t.Parallel()
	f := ensured(t)
	agentCommit(t, f.plan, "b.txt", "b\n")

	require.NoError(t, os.WriteFile(filepath.Join(f.repo, "other.txt"), []byte("o"), 0o644))
	gitT(t, f.repo, "add", ".")
	gitT(t, f.repo, "commit", "-m", "other")
	mainTip := gitT(t, f.repo, "rev-parse", "main")

	res, err := f.plan.Merge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, MergeMainMoved, res.Status)
	assert.Contains(t, res.Guidance(), "git rebase main")
	assert.Equal(t, mainTip, gitT(t, f.repo, "rev-parse", "main"), "main must not move")

	// After the rebase the agent asked for, the merge goes through.
	agentGit(t, f.plan, "rebase", "main")
	res, err = f.plan.Merge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, MergeMerged, res.Status)
	assert.FileExists(t, filepath.Join(f.repo, "b.txt"))
	assert.FileExists(t, filepath.Join(f.repo, "other.txt"))
}

func TestMerge_DirtyWorktree(t *testing.T) {
	t.Parallel()
	f := ensured(t)
	agentCommit(t, f.plan, "b.txt", "b\n")
	require.NoError(t, os.WriteFile(filepath.Join(f.plan.Path, "wip.txt"), []byte("w"), 0o644))

	res, err := f.plan.Merge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, MergeDirtyWorktree, res.Status)
	assert.Contains(t, res.Detail, "wip.txt")
	assert.Contains(t, res.Guidance(), "wip.txt")
	assert.NotContains(t, gitT(t, f.repo, "log", "--format=%s", "main"), "add b.txt")
}

func TestMerge_MainCheckoutProblems(t *testing.T) {
	t.Parallel()

	t.Run("dirty main", func(t *testing.T) {
		t.Parallel()
		f := ensured(t)
		agentCommit(t, f.plan, "b.txt", "b\n")
		require.NoError(t, os.WriteFile(filepath.Join(f.repo, "a.txt"), []byte("edited\n"), 0o644))

		res, err := f.plan.Merge(t.Context())
		require.NoError(t, err)
		assert.Equal(t, MergeMainDirty, res.Status)
		assert.Contains(t, res.Guidance(), "not yours to fix")
	})

	t.Run("detached main", func(t *testing.T) {
		t.Parallel()
		f := ensured(t)
		agentCommit(t, f.plan, "b.txt", "b\n")
		gitT(t, f.repo, "checkout", "--detach")

		res, err := f.plan.Merge(t.Context())
		require.NoError(t, err)
		assert.Equal(t, MergeMainDetached, res.Status)
	})
}

func TestMerge_InvalidObjects(t *testing.T) {
	t.Parallel()
	f := ensured(t)

	// A commit whose tree names a blob that does not exist anywhere.
	cmd := exec.Command("git", "mktree", "--missing")
	cmd.Dir = f.plan.Path
	cmd.Env = append(os.Environ(), "GIT_OBJECT_DIRECTORY="+f.plan.ObjectsDir,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(f.plan.GitDir(), "objects"))
	cmd.Stdin = strings.NewReader("100644 blob 1111111111111111111111111111111111111111\tmissing.txt\n")
	tree, err := cmd.Output()
	require.NoError(t, err)
	commit := agentGit(t, f.plan, "commit-tree", strings.TrimSpace(string(tree)), "-p", "HEAD", "-m", "broken")
	agentGit(t, f.plan, "update-ref", "refs/heads/"+f.plan.Branch, commit)

	mainTip := gitT(t, f.repo, "rev-parse", "main")
	res, err := f.plan.Merge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, MergeInvalidObjects, res.Status)
	assert.Equal(t, mainTip, gitT(t, f.repo, "rev-parse", "main"), "main must not move")
}

func TestMerge_TamperedWorktreeIsRefused(t *testing.T) {
	t.Parallel()
	f := ensured(t)
	agentCommit(t, f.plan, "b.txt", "b\n")

	// The agent can write its admin dir; redirecting commondir must not make
	// the runner's git follow it.
	admin := filepath.Join(f.plan.GitDir(), "worktrees", f.plan.Name)
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte("/tmp/evil/.git\n"), 0o644))
	_, err := f.plan.Merge(t.Context())
	require.ErrorContains(t, err, "tampered")

	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(admin, "link")))
	_, err = f.plan.Merge(t.Context())
	require.ErrorContains(t, err, "symlink")
	require.NoError(t, os.Remove(filepath.Join(admin, "link")))

	require.NoError(t, os.WriteFile(filepath.Join(admin, "config.worktree"), []byte("[core]\n\tfsmonitor=/x\n"), 0o644))
	_, err = f.plan.Merge(t.Context())
	require.ErrorContains(t, err, "own config")
}

func TestMerge_NoWorktree(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.plan.Merge(t.Context())
	require.ErrorContains(t, err, "does not exist")
}
