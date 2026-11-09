package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// gitT runs plain git (no hardening) for test setup and assertions.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

type fixture struct {
	root string // worktree root
	repo string
	objs string // objects base
	plan Plan
}

// newFixture creates a repository with one commit on main and a plan for chat c1.
func newFixture(t *testing.T) fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := fixture{root: filepath.Join(base, "wt"), repo: filepath.Join(base, "src", "repo"), objs: filepath.Join(base, "sess", "c1")}
	require.NoError(t, os.MkdirAll(f.repo, 0o755))
	gitT(t, f.repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(f.repo, "a.txt"), []byte("a\n"), 0o644))
	gitT(t, f.repo, "add", ".")
	gitT(t, f.repo, "commit", "-m", "init")
	f.plan, err = PlanFor(f.root, f.repo, "c1", f.objs)
	require.NoError(t, err)
	return f
}
