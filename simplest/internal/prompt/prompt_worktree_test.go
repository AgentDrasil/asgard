package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCtxFile writes a context file with the given content and returns its path.
func writeCtxFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

// setupMainRepo creates a plain main repo skeleton: main/.git/HEAD.
func setupMainRepo(t *testing.T, root string) string {
	t.Helper()
	main := filepath.Join(root, "main")
	require.NoError(t, os.MkdirAll(filepath.Join(main, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(main, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	return main
}

// setupLinkedWorktree creates main/.git/worktrees/<name>/{HEAD,commondir} and a
// nested checkout at main/.worktrees/<name> whose .git file points at the git dir.
func setupLinkedWorktree(t *testing.T, main, name string) string {
	t.Helper()
	gitDir := filepath.Join(main, ".git", "worktrees", name)
	require.NoError(t, os.MkdirAll(gitDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/feat\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644))
	wt := filepath.Join(main, ".worktrees", name)
	require.NoError(t, os.MkdirAll(wt, 0o755))
	gitFile := "gitdir: " + gitDir + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte(gitFile), 0o644))
	return wt
}

func TestLoadProjectContextFiles_WorktreeShadowing(t *testing.T) {
	t.Parallel()

	t.Run("main and worktree AGENTS.md: only worktree loads", func(t *testing.T) {
		t.Parallel()
		main := setupMainRepo(t, t.TempDir())
		wt := setupLinkedWorktree(t, main, "feat")
		writeCtxFile(t, main, "AGENTS.md", "main rules")
		writeCtxFile(t, wt, "AGENTS.md", "worktree rules")

		files := LoadProjectContextFiles(wt, t.TempDir())
		require.Len(t, files, 1)
		assert.Equal(t, filepath.Join(wt, "AGENTS.md"), files[0].Path)
		assert.Equal(t, "worktree rules", files[0].Content)
	})

	t.Run("worktree without context file: main loads once", func(t *testing.T) {
		t.Parallel()
		main := setupMainRepo(t, t.TempDir())
		wt := setupLinkedWorktree(t, main, "feat")
		writeCtxFile(t, main, "AGENTS.md", "main rules")

		files := LoadProjectContextFiles(wt, t.TempDir())
		require.Len(t, files, 1)
		assert.Equal(t, filepath.Join(main, "AGENTS.md"), files[0].Path)
		assert.Equal(t, "main rules", files[0].Content)
	})

	t.Run("symlinked worktree AGENTS.md loads exactly once at worktree path (pi f1b2e77f5)", func(t *testing.T) {
		t.Parallel()
		main := setupMainRepo(t, t.TempDir())
		wt := setupLinkedWorktree(t, main, "feat")
		mainFile := writeCtxFile(t, main, "AGENTS.md", "shared rules")
		require.NoError(t, os.Symlink(mainFile, filepath.Join(wt, "AGENTS.md")))

		files := LoadProjectContextFiles(wt, t.TempDir())
		require.Len(t, files, 1)
		assert.Equal(t, filepath.Join(wt, "AGENTS.md"), files[0].Path)
		assert.Equal(t, "shared rules", files[0].Content)
	})

	t.Run("different basenames both load", func(t *testing.T) {
		t.Parallel()
		main := setupMainRepo(t, t.TempDir())
		wt := setupLinkedWorktree(t, main, "feat")
		writeCtxFile(t, main, "CLAUDE.md", "main claude")
		writeCtxFile(t, wt, "AGENTS.md", "worktree agents")

		files := LoadProjectContextFiles(wt, t.TempDir())
		require.Len(t, files, 2)
		// Root-most first: main's file before the worktree's.
		assert.Equal(t, filepath.Join(main, "CLAUDE.md"), files[0].Path)
		assert.Equal(t, "main claude", files[0].Content)
		assert.Equal(t, filepath.Join(wt, "AGENTS.md"), files[1].Path)
		assert.Equal(t, "worktree agents", files[1].Content)
	})

	t.Run("context file above the main repo still loads", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		main := setupMainRepo(t, root)
		wt := setupLinkedWorktree(t, main, "feat")
		writeCtxFile(t, root, "AGENTS.md", "outer rules")
		writeCtxFile(t, wt, "AGENTS.md", "worktree rules")

		files := LoadProjectContextFiles(wt, t.TempDir())
		require.Len(t, files, 2)
		assert.Equal(t, filepath.Join(root, "AGENTS.md"), files[0].Path)
		assert.Equal(t, "outer rules", files[0].Content)
		assert.Equal(t, filepath.Join(wt, "AGENTS.md"), files[1].Path)
	})

	t.Run("bare layout is not shadowed", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		proj := filepath.Join(root, "proj")
		// Bare repo: proj/.bare/HEAD; checked-out worktree: proj/main with a .git
		// file pointing into the bare object store, plus a commondir pointing back.
		require.NoError(t, os.MkdirAll(filepath.Join(proj, ".bare"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(proj, ".bare", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
		main := filepath.Join(proj, "main")
		require.NoError(t, os.MkdirAll(main, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(main, ".git"), []byte("gitdir: "+filepath.Join(proj, ".bare")+"\n"), 0o644))
		writeCtxFile(t, proj, "AGENTS.md", "proj rules")
		writeCtxFile(t, main, "AGENTS.md", "main rules")

		files := LoadProjectContextFiles(main, t.TempDir())
		require.Len(t, files, 2)
		assert.Equal(t, filepath.Join(proj, "AGENTS.md"), files[0].Path)
		assert.Equal(t, "proj rules", files[0].Content)
		assert.Equal(t, filepath.Join(main, "AGENTS.md"), files[1].Path)
	})

	t.Run("sibling worktree is not shadowed", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		main := setupMainRepo(t, root)
		// Worktree as a sibling of the main repo dir, gitdir under main's .git.
		gitDir := filepath.Join(main, ".git", "worktrees", "feat")
		require.NoError(t, os.MkdirAll(gitDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/feat\n"), 0o644))
		wt := filepath.Join(root, "feat")
		require.NoError(t, os.MkdirAll(wt, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644))
		writeCtxFile(t, root, "AGENTS.md", "root rules")
		writeCtxFile(t, main, "AGENTS.md", "main rules")
		writeCtxFile(t, wt, "AGENTS.md", "worktree rules")

		// main is not an ancestor of the sibling worktree, so only the shared
		// root file and the worktree's own file load; neither is shadowed.
		files := LoadProjectContextFiles(wt, t.TempDir())
		require.Len(t, files, 2)
		assert.Equal(t, filepath.Join(root, "AGENTS.md"), files[0].Path)
		assert.Equal(t, "root rules", files[0].Content)
		assert.Equal(t, filepath.Join(wt, "AGENTS.md"), files[1].Path)
		assert.Equal(t, "worktree rules", files[1].Content)
	})

	t.Run("submodule-like gitdir under .git/modules without commondir is not shadowed", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		main := setupMainRepo(t, root)
		// Nested checkout whose gitdir lives under main/.git/modules and has no
		// commondir file, so commonGitDir is the module dir itself.
		moduleDir := filepath.Join(main, ".git", "modules", "sub")
		require.NoError(t, os.MkdirAll(moduleDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
		sub := filepath.Join(main, "vendor", "sub")
		require.NoError(t, os.MkdirAll(sub, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: "+moduleDir+"\n"), 0o644))
		writeCtxFile(t, main, "AGENTS.md", "main rules")
		writeCtxFile(t, sub, "AGENTS.md", "sub rules")

		files := LoadProjectContextFiles(sub, t.TempDir())
		require.Len(t, files, 2)
		assert.Equal(t, filepath.Join(main, "AGENTS.md"), files[0].Path)
		assert.Equal(t, "main rules", files[0].Content)
		assert.Equal(t, filepath.Join(sub, "AGENTS.md"), files[1].Path)
	})

	t.Run("cwd through a symlink alias still detects shadowing", func(t *testing.T) {
		t.Parallel()
		main := setupMainRepo(t, t.TempDir())
		wt := setupLinkedWorktree(t, main, "feat")
		writeCtxFile(t, main, "AGENTS.md", "main rules")
		writeCtxFile(t, wt, "AGENTS.md", "worktree rules")
		// Alias placed under the main repo: the walk reaches main/AGENTS.md via
		// a different raw path, and the realpath comparison must still skip it.
		alias := filepath.Join(main, "alias")
		require.NoError(t, os.Symlink(wt, alias))

		files := LoadProjectContextFiles(alias, t.TempDir())
		require.Len(t, files, 1)
		assert.Equal(t, filepath.Join(alias, "AGENTS.md"), files[0].Path)
		assert.Equal(t, "worktree rules", files[0].Content)
	})

	t.Run("malformed .git file pointing at gitdir without HEAD stops the search", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		main := setupMainRepo(t, root)
		// Nested checkout whose .git file points at a git dir without HEAD,
		// inside a plain repo ancestor: findGitPaths must stop, not fall through.
		broken := filepath.Join(main, "broken")
		require.NoError(t, os.MkdirAll(broken, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: "+filepath.Join(root, "nowhere")+"\n"), 0o644))
		writeCtxFile(t, main, "AGENTS.md", "main rules")
		writeCtxFile(t, broken, "AGENTS.md", "broken rules")

		files := LoadProjectContextFiles(broken, t.TempDir())
		require.Len(t, files, 2)
		assert.Equal(t, filepath.Join(main, "AGENTS.md"), files[0].Path)
		assert.Equal(t, "main rules", files[0].Content)
		assert.Equal(t, filepath.Join(broken, "AGENTS.md"), files[1].Path)
	})
}

func TestFindGitPaths(t *testing.T) {
	t.Parallel()

	t.Run("plain repo", func(t *testing.T) {
		t.Parallel()
		main := setupMainRepo(t, t.TempDir())
		paths := findGitPaths(main)
		require.NotNil(t, paths)
		assert.Equal(t, main, paths.repoDir)
		assert.Equal(t, filepath.Join(main, ".git"), paths.commonGitDir)
	})

	t.Run("linked worktree resolves commondir", func(t *testing.T) {
		t.Parallel()
		main := setupMainRepo(t, t.TempDir())
		wt := setupLinkedWorktree(t, main, "feat")
		paths := findGitPaths(wt)
		require.NotNil(t, paths)
		assert.Equal(t, wt, paths.repoDir)
		assert.Equal(t, filepath.Join(main, ".git"), paths.commonGitDir)
	})

	t.Run("no repo returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, findGitPaths(t.TempDir()))
	})
}
