package runnerfs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func TestTreePagingOrderAndFilters(t *testing.T) {
	fs, ws, outside := confinedFS(t)
	writeFile(t, filepath.Join(ws, "z.txt"), "z")
	writeFile(t, filepath.Join(ws, "b.go"), "package b")
	writeFile(t, filepath.Join(ws, "a", "x.txt"), "x")
	writeFile(t, filepath.Join(ws, "c", "y.txt"), "y")
	writeFile(t, filepath.Join(ws, ".git", "config"), "")
	writeFile(t, filepath.Join(ws, "node_modules", "p.json"), "{}")
	writeFile(t, filepath.Join(outside, "secret"), "s")
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "out-dir")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(ws, "out-file")))
	require.NoError(t, os.Symlink("a", filepath.Join(ws, "in-dir")))

	full, err := fs.Tree(t.Context(), TreeRequest{RunDir: ws})
	require.NoError(t, err)
	var names []string
	for _, e := range full.Entries {
		names = append(names, e.Name)
	}
	// Directories first, then by name; links leaving the root are plain
	// entries (never directories), links staying inside resolve.
	assert.Equal(t, []string{"a", "c", "in-dir", "b.go", "out-dir", "out-file", "z.txt"}, names)
	assert.Equal(t, "", full.Path)
	assert.Equal(t, "b.go", full.Entries[3].Path)
	assert.Equal(t, "go", full.Entries[3].Ext)

	// Paging yields the same sequence.
	var paged []string
	token := ""
	for pages := 0; ; pages++ {
		require.Less(t, pages, 20)
		res, err := fs.Tree(t.Context(), TreeRequest{RunDir: ws, Limit: 3, PageToken: token})
		require.NoError(t, err)
		assert.LessOrEqual(t, len(res.Entries), 3)
		for _, e := range res.Entries {
			paged = append(paged, e.Name)
		}
		if token = res.NextPageToken; token == "" {
			break
		}
	}
	assert.Equal(t, names, paged)

	sub, err := fs.Tree(t.Context(), TreeRequest{RunDir: ws, Path: "a"})
	require.NoError(t, err)
	require.Len(t, sub.Entries, 1)
	assert.Equal(t, "a/x.txt", sub.Entries[0].Path)
	assert.Equal(t, "a", sub.Path)

	_, err = fs.Tree(t.Context(), TreeRequest{RunDir: ws, PageToken: "bogus"})
	assert.Equal(t, CodeBadRequest, CodeOf(err))
}

func TestTreeScopeAndTraversal(t *testing.T) {
	fs, ws, outside := confinedFS(t)
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "out-dir")))
	writeFile(t, filepath.Join(ws, "file.txt"), "x")
	writeFile(t, filepath.Join(fs.base("tmp"), "t.txt"), "tmp")

	for _, p := range []string{"../x", "a/../../x", "out-dir", "out-dir/sub", "/etc"} {
		_, err := fs.Tree(t.Context(), TreeRequest{RunDir: ws, Path: p})
		assert.Error(t, err, p)
		assert.NotEqual(t, CodeInternal, CodeOf(err), p)
	}
	_, err := fs.Tree(t.Context(), TreeRequest{RunDir: ws, Path: "file.txt"})
	assert.Equal(t, CodeBadRequest, CodeOf(err))
	_, err = fs.Tree(t.Context(), TreeRequest{RunDir: ws, Path: "missing"})
	assert.Equal(t, CodeNotFound, CodeOf(err))
	// A run dir outside the allowed roots is refused.
	_, err = fs.Tree(t.Context(), TreeRequest{RunDir: outside})
	assert.Equal(t, CodeDenied, CodeOf(err))

	res, err := fs.Tree(t.Context(), TreeRequest{RunDir: ws, Path: "/tmp"})
	require.NoError(t, err)
	assert.Equal(t, "/tmp", res.Path)
	require.Len(t, res.Entries, 1)
	assert.Equal(t, "/tmp/t.txt", res.Entries[0].Path)
}

func TestSearch(t *testing.T) {
	fs, ws, outside := confinedFS(t)
	writeFile(t, filepath.Join(ws, "src", "Main.go"), "m")
	writeFile(t, filepath.Join(ws, "src", "util.go"), "u")
	writeFile(t, filepath.Join(ws, "node_modules", "x", "main.go"), "x")
	writeFile(t, filepath.Join(ws, ".git", "main.go"), "x")
	writeFile(t, filepath.Join(outside, "main-secret.go"), "s")
	require.NoError(t, os.Symlink(filepath.Join(outside, "main-secret.go"), filepath.Join(ws, "leak-main.go")))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "out-dir")))
	require.NoError(t, os.Symlink("src/Main.go", filepath.Join(ws, "alias-main.go")))
	writeFile(t, filepath.Join(fs.base("tmp"), "main.tmp"), "t")

	hits, err := fs.Search(t.Context(), SearchRequest{RunDir: ws, Query: "MAIN"})
	require.NoError(t, err)
	var got []string
	for _, h := range hits {
		got = append(got, h.Scope+":"+h.Path)
	}
	// alias-main.go sorts first and wins the dedupe against src/Main.go; the
	// link leaving the root, node_modules and .git are never listed.
	assert.Equal(t, []string{"workspace:alias-main.go", "tmp:/tmp/main.tmp"}, got)

	hits, err = fs.Search(t.Context(), SearchRequest{RunDir: ws, Limit: 1})
	require.NoError(t, err)
	assert.Len(t, hits, 1)
}

func TestSearchSessionExcludesTranscriptAndWorkflows(t *testing.T) {
	fs, ws, _ := confinedFS(t)
	sess := fs.base("session")
	writeFile(t, filepath.Join(sess, "notes.txt"), "n")
	writeFile(t, filepath.Join(sess, "messages.jsonl"), "{}")
	writeFile(t, filepath.Join(sess, "workflows", "run-1", "nodes", "node1.log"), "log")
	// The same names in the workspace are ordinary files.
	writeFile(t, filepath.Join(ws, "session", "workflows", "workflow.yaml"), "steps: []")
	writeFile(t, filepath.Join(ws, "session", "messages.jsonl"), "data")

	hits, err := fs.Search(t.Context(), SearchRequest{RunDir: ws})
	require.NoError(t, err)
	var got []string
	for _, h := range hits {
		got = append(got, h.Path)
	}
	assert.ElementsMatch(t, []string{"session/workflows/workflow.yaml", "session/messages.jsonl", "/session/notes.txt"}, got)
}

func TestSubdirs(t *testing.T) {
	fs, ws, outside := confinedFS(t)
	for _, d := range []string{"dir_b/nested", "dir_a", ".hidden"} {
		require.NoError(t, os.MkdirAll(filepath.Join(ws, d), 0o755))
	}
	writeFile(t, filepath.Join(ws, "file.txt"), "x")
	require.NoError(t, os.Symlink("dir_a", filepath.Join(ws, "link_in")))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "link_out")))
	require.NoError(t, os.MkdirAll(filepath.Join(ws, ".git"), 0o755))
	// Subdirs lists beneath the allowed run dirs of the runner.
	fs.ChatID = ""

	res, err := fs.Subdirs(t.Context(), SubdirsRequest{Dir: ws})
	require.NoError(t, err)
	assert.Equal(t, []string{"dir_a", "dir_b", "link_in"}, res.Subdirs)
	assert.Equal(t, ws, res.GitRoot)

	var paged []string
	token := ""
	for {
		page, err := fs.Subdirs(t.Context(), SubdirsRequest{Dir: ws, Limit: 2, PageToken: token})
		require.NoError(t, err)
		paged = append(paged, page.Subdirs...)
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	assert.Equal(t, res.Subdirs, paged)

	nested, err := fs.Subdirs(t.Context(), SubdirsRequest{Dir: filepath.Join(ws, "dir_b", "nested")})
	require.NoError(t, err)
	assert.Empty(t, nested.Subdirs)
	assert.Equal(t, ws, nested.GitRoot, "git root found by walking up inside the allowed root")

	for _, tc := range []struct {
		dir  string
		code Code
	}{
		{outside, CodeDenied},
		{"/", CodeDenied},
		{filepath.Join(ws, "link_out"), CodeDenied},
		{filepath.Join(ws, "..", "x"), CodeDenied},
		{filepath.Join(ws, "missing"), CodeBadRequest},
		{filepath.Join(ws, "file.txt"), CodeBadRequest},
		{"relative", CodeBadRequest},
	} {
		_, err := fs.Subdirs(t.Context(), SubdirsRequest{Dir: tc.dir})
		assert.Equal(t, tc.code, CodeOf(err), tc.dir)
	}
}

func TestSubdirsDevModeUnconfined(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "x"), 0o755))
	res, err := NewLocal("").Subdirs(t.Context(), SubdirsRequest{Dir: root})
	require.NoError(t, err)
	assert.Equal(t, []string{"x"}, res.Subdirs)
}

// --- git ---

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

// gitWorkspace returns a confined Local whose workspace is a repo with one
// commit plus a modified, a staged-new, a deleted and an untracked file.
func gitWorkspace(t *testing.T) (Local, string) {
	t.Helper()
	fs, ws, _ := confinedFS(t)
	gitCmd(t, ws, "init")
	writeFile(t, filepath.Join(ws, "keep.txt"), "one\n")
	writeFile(t, filepath.Join(ws, "gone.txt"), "bye\n")
	gitCmd(t, ws, "add", ".")
	gitCmd(t, ws, "commit", "-m", "first")
	writeFile(t, filepath.Join(ws, "keep.txt"), "one\ntwo\n")
	writeFile(t, filepath.Join(ws, "staged.txt"), "s\n")
	gitCmd(t, ws, "add", "staged.txt")
	require.NoError(t, os.Remove(filepath.Join(ws, "gone.txt")))
	writeFile(t, filepath.Join(ws, "new.txt"), "n\n")
	return fs, ws
}

func collectDiff(t *testing.T, fs Local, req GitDiffRequest) (map[string]GitDiffFile, error) {
	t.Helper()
	files := map[string]GitDiffFile{}
	err := fs.GitDiff(t.Context(), req, func(f GitDiffFile) bool {
		files[f.NewPath] = f
		return true
	})
	return files, err
}

func TestGitDiffLogAndCommit(t *testing.T) {
	fs, ws := gitWorkspace(t)

	files, err := collectDiff(t, fs, GitDiffRequest{RunDir: ws})
	require.NoError(t, err)
	require.Len(t, files, 4)
	assert.Equal(t, "M", files["keep.txt"].Status)
	assert.Equal(t, "one\n", files["keep.txt"].OldContent)
	assert.Equal(t, "one\ntwo\n", files["keep.txt"].NewContent)
	require.Len(t, files["keep.txt"].Hunks, 1)
	assert.Contains(t, files["keep.txt"].Hunks[0], "+two")
	assert.Equal(t, "D", files["gone.txt"].Status)
	assert.Equal(t, "", files["gone.txt"].NewContent)
	assert.Equal(t, "A", files["staged.txt"].Status)
	assert.Equal(t, "A", files["new.txt"].Status)
	assert.Equal(t, "/dev/null", files["new.txt"].OldPath)
	assert.Contains(t, files["new.txt"].Hunks[0], "+n")

	logRes, err := fs.GitLog(t.Context(), ws, 5)
	require.NoError(t, err)
	require.Len(t, logRes.Commits, 1)
	assert.Equal(t, "first", logRes.Commits[0].Message)
	assert.Equal(t, "main", logRes.CurrentBranch)
	assert.Equal(t, 4, logRes.UnstashedCount)

	// Commit the pending changes, then diff that commit.
	gitCmd(t, ws, "add", "-A")
	gitCmd(t, ws, "commit", "-m", "second")
	hash := gitCmd(t, ws, "rev-parse", "HEAD")
	byCommit, err := collectDiff(t, fs, GitDiffRequest{RunDir: ws, Commit: hash})
	require.NoError(t, err)
	require.Contains(t, byCommit, "keep.txt")
	assert.Equal(t, "M", byCommit["keep.txt"].Status)
	assert.Equal(t, "one\n", byCommit["keep.txt"].OldContent)
	assert.Equal(t, "one\ntwo\n", byCommit["keep.txt"].NewContent)
	assert.Contains(t, byCommit["keep.txt"].Hunks[0], "+two")

	// A subdirectory of the repo works; the repo root is found beneath the root.
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "sub"), 0o755))
	files, err = collectDiff(t, fs, GitDiffRequest{RunDir: filepath.Join(ws, "sub")})
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestGitCommitCannotBeAnOption(t *testing.T) {
	fs, ws := gitWorkspace(t)
	target := filepath.Join(t.TempDir(), "out")
	for _, c := range []string{"--output=" + target, "-p", "HEAD --output=" + target, "$(id)", "nonexistent-ref"} {
		_, err := collectDiff(t, fs, GitDiffRequest{RunDir: ws, Commit: c})
		assert.Error(t, err, c)
	}
	assert.NoFileExists(t, target)
}

func TestGitRefusesDangerousRepoConfig(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"core.fsmonitor", "%s"},
		{"core.sshCommand", "%s"},
		{"core.pager", "%s"},
		{"diff.evil.textconv", "%s"},
		{"filter.evil.clean", "%s"},
		{"credential.helper", "!%s"},
		{"core.hooksPath", "/tmp"},
		{"core.worktree", "/etc"},
		{"include.path", "/tmp/other"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			fs, ws := gitWorkspace(t)
			marker := filepath.Join(t.TempDir(), "pwned")
			script := filepath.Join(t.TempDir(), "evil.sh")
			require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\ncat\n"), 0o755))
			value := tc.value
			if strings.Contains(value, "%s") {
				value = fmt.Sprintf(value, script)
			}
			gitCmd(t, ws, "config", "--local", tc.key, value)
			writeFile(t, filepath.Join(ws, ".gitattributes"), "* diff=evil filter=evil\n")

			_, err := collectDiff(t, fs, GitDiffRequest{RunDir: ws})
			assert.Equal(t, CodeDenied, CodeOf(err))
			_, err = fs.GitLog(t.Context(), ws, 5)
			assert.Equal(t, CodeDenied, CodeOf(err))
			_, err = fs.GitPush(t.Context(), ws, "")
			assert.Equal(t, CodeDenied, CodeOf(err))
			_, err = fs.GitPull(t.Context(), ws, "")
			assert.Equal(t, CodeDenied, CodeOf(err))
			assert.NoFileExists(t, marker)
		})
	}
}

func TestGitIgnoresHooksOfTheRepo(t *testing.T) {
	fs, ws := gitWorkspace(t)
	gitCmd(t, ws, "add", ".")
	gitCmd(t, ws, "commit", "-m", "second")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitCmd(t, filepath.Dir(remote), "init", "--bare", remote)
	fakeSSH(t)
	gitCmd(t, ws, "remote", "add", "origin", sshURL(remote))
	gitCmd(t, ws, "push", "-u", "origin", "main")
	for _, hook := range []string{"pre-push", "post-merge", "post-checkout", "pre-merge-commit"} {
		writeFile(t, filepath.Join(ws, ".git", "hooks", hook), "#!/bin/sh\ntouch "+marker+"\n")
		require.NoError(t, os.Chmod(filepath.Join(ws, ".git", "hooks", hook), 0o755))
	}

	// New commit upstream so the pull really merges.
	other := filepath.Join(t.TempDir(), "other")
	gitCmd(t, filepath.Dir(other), "clone", remote, other)
	writeFile(t, filepath.Join(other, "up.txt"), "u\n")
	gitCmd(t, other, "add", ".")
	gitCmd(t, other, "commit", "-m", "upstream")
	gitCmd(t, other, "push")

	pull, err := fs.GitPull(t.Context(), ws, "")
	require.NoError(t, err)
	assert.True(t, pull.Success, pull.Error)
	assert.FileExists(t, filepath.Join(ws, "up.txt"))

	writeFile(t, filepath.Join(ws, "mine.txt"), "m\n")
	gitCmd(t, ws, "add", ".")
	gitCmd(t, ws, "commit", "-m", "mine")
	push, err := fs.GitPush(t.Context(), ws, "")
	require.NoError(t, err)
	assert.True(t, push.Success, push.Error)
	assert.Equal(t, gitCmd(t, ws, "rev-parse", "HEAD"), gitCmd(t, remote, "rev-parse", "main"))
	assert.NoFileExists(t, marker, "repo hooks must not run on the runner")

	// A failing git command is reported in the result, not as an error.
	gitCmd(t, ws, "remote", "set-url", "origin", sshURL(filepath.Join(t.TempDir(), "missing.git")))
	push, err = fs.GitPush(t.Context(), ws, "")
	require.NoError(t, err)
	assert.False(t, push.Success)
	assert.NotEmpty(t, push.Error)
}

func TestGitRepoRootAndGitfileLimits(t *testing.T) {
	fs, ws, _ := confinedFS(t)

	// The repo encloses the allowed root: it is out of reach.
	repo := filepath.Dir(ws)
	gitCmd(t, repo, "init")
	_, err := collectDiff(t, fs, GitDiffRequest{RunDir: ws})
	assert.ErrorContains(t, err, "not inside a git repository")
	assert.Equal(t, CodeBadRequest, CodeOf(err))

	// A .git file (redirect to another gitdir) is refused.
	gitDir := filepath.Join(t.TempDir(), "elsewhere.git")
	gitCmd(t, filepath.Dir(gitDir), "init", "--bare", gitDir)
	writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+gitDir+"\n")
	_, err = collectDiff(t, fs, GitDiffRequest{RunDir: ws})
	assert.Equal(t, CodeDenied, CodeOf(err))
	require.NoError(t, os.Remove(filepath.Join(ws, ".git")))

	// A symlinked .git is not followed either.
	require.NoError(t, os.Symlink(gitDir, filepath.Join(ws, ".git")))
	_, err = collectDiff(t, fs, GitDiffRequest{RunDir: ws})
	assert.Equal(t, CodeDenied, CodeOf(err))
}

func TestGitDiffCapsAndUTF8(t *testing.T) {
	fs, ws, _ := confinedFS(t)
	gitCmd(t, ws, "init")
	writeFile(t, filepath.Join(ws, "a.txt"), "x\n")
	gitCmd(t, ws, "add", ".")
	gitCmd(t, ws, "commit", "-m", "c")

	big := strings.Repeat("é", MaxDiffSide) // 8 MiB of 2-byte runes
	writeFile(t, filepath.Join(ws, "big.txt"), big)
	writeFile(t, filepath.Join(ws, "bin.dat"), "\xff\xfe\x00bad\xc3")
	link := filepath.Join(ws, "link.txt")
	require.NoError(t, os.Symlink("a.txt", link))

	files, err := collectDiff(t, fs, GitDiffRequest{RunDir: ws})
	require.NoError(t, err)
	b := files["big.txt"]
	assert.True(t, b.ContentTruncated)
	assert.LessOrEqual(t, len(b.NewContent), MaxDiffSide)
	assert.Greater(t, len(b.NewContent), MaxDiffSide-4)
	assert.True(t, b.HunksTruncated)
	total := len(b.OldPath) + len(b.NewPath) + len(b.OldContent) + len(b.NewContent)
	for _, h := range b.Hunks {
		total += len(h)
	}
	assert.LessOrEqual(t, total, MaxDiffFrame)
	assert.True(t, utf8Valid(b.NewContent))
	assert.True(t, utf8Valid(files["bin.dat"].NewContent))
	// Symlinks are never followed when reading the work tree.
	assert.Equal(t, "", files["link.txt"].NewContent)
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "") == s }

func TestGitRefusesRedirectsOutOfTheRoot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, ws, other string)
	}{
		{"commondir", func(t *testing.T, ws, other string) {
			writeFile(t, filepath.Join(ws, ".git", "commondir"), filepath.Join(other, ".git")+"\n")
		}},
		{"gitdir file", func(t *testing.T, ws, other string) {
			writeFile(t, filepath.Join(ws, ".git", "gitdir"), filepath.Join(other, ".git")+"\n")
		}},
		{"alternates", func(t *testing.T, ws, other string) {
			writeFile(t, filepath.Join(ws, ".git", "objects", "info", "alternates"), filepath.Join(other, ".git", "objects")+"\n")
		}},
		{"http-alternates", func(t *testing.T, ws, other string) {
			writeFile(t, filepath.Join(ws, ".git", "objects", "info", "http-alternates"), "http://example.invalid/\n")
		}},
		{"symlinked objects", func(t *testing.T, ws, other string) {
			require.NoError(t, os.RemoveAll(filepath.Join(ws, ".git", "objects")))
			require.NoError(t, os.Symlink(filepath.Join(other, ".git", "objects"), filepath.Join(ws, ".git", "objects")))
		}},
		{"symlinked refs", func(t *testing.T, ws, other string) {
			require.NoError(t, os.RemoveAll(filepath.Join(ws, ".git", "refs")))
			require.NoError(t, os.Symlink(filepath.Join(other, ".git", "refs"), filepath.Join(ws, ".git", "refs")))
		}},
		{"symlinked HEAD", func(t *testing.T, ws, other string) {
			require.NoError(t, os.Remove(filepath.Join(ws, ".git", "HEAD")))
			require.NoError(t, os.Symlink(filepath.Join(other, ".git", "HEAD"), filepath.Join(ws, ".git", "HEAD")))
		}},
		{"symlinked ref entry", func(t *testing.T, ws, other string) {
			require.NoError(t, os.Symlink(filepath.Join(other, ".git", "refs", "heads", "main"), filepath.Join(ws, ".git", "refs", "heads", "evil")))
		}},
		{"hard-linked file", func(t *testing.T, ws, other string) {
			require.NoError(t, os.Link(filepath.Join(ws, ".git", "HEAD"), filepath.Join(ws, ".git", "HEAD2")))
		}},
		{"hard-linked COMMIT_EDITMSG", func(t *testing.T, ws, other string) {
			writeFile(t, filepath.Join(ws, ".git", "COMMIT_EDITMSG"), "m\n")
			require.NoError(t, os.Link(filepath.Join(ws, ".git", "COMMIT_EDITMSG"), filepath.Join(ws, ".git", "COMMIT_EDITMSG2")))
		}},
		{"hard-linked rebase-merge file", func(t *testing.T, ws, other string) {
			writeFile(t, filepath.Join(ws, ".git", "rebase-merge", "x"), "x\n")
			require.NoError(t, os.Link(filepath.Join(ws, ".git", "rebase-merge", "x"), filepath.Join(ws, ".git", "rebase-merge", "y")))
		}},
		{"hard-linked module FETCH_HEAD", func(t *testing.T, ws, other string) {
			writeFile(t, filepath.Join(ws, ".git", "modules", "n", "FETCH_HEAD"), "x\n")
			require.NoError(t, os.Link(filepath.Join(ws, ".git", "modules", "n", "FETCH_HEAD"), filepath.Join(ws, ".git", "modules", "n", "FETCH_HEAD2")))
		}},
		{"hard-linked logs file", func(t *testing.T, ws, other string) {
			require.NoError(t, os.Link(filepath.Join(ws, ".git", "logs", "HEAD"), filepath.Join(ws, ".git", "logs", "HEAD2")))
		}},
		{"hard-linked ref file", func(t *testing.T, ws, other string) {
			require.NoError(t, os.Link(filepath.Join(ws, ".git", "refs", "heads", "main"), filepath.Join(ws, ".git", "refs", "heads", "alias")))
		}},
		{"symlinked FETCH_HEAD", func(t *testing.T, ws, other string) {
			require.NoError(t, os.Symlink(filepath.Join(other, "victim"), filepath.Join(ws, ".git", "FETCH_HEAD")))
		}},
		{"symlinked logs entry", func(t *testing.T, ws, other string) {
			require.NoError(t, os.MkdirAll(filepath.Join(ws, ".git", "logs", "refs", "heads"), 0o755))
			_ = os.Remove(filepath.Join(ws, ".git", "logs", "refs", "heads", "main"))
			require.NoError(t, os.Symlink(filepath.Join(other, "victim"), filepath.Join(ws, ".git", "logs", "refs", "heads", "main")))
		}},
		{"symlinked MERGE_HEAD", func(t *testing.T, ws, other string) {
			require.NoError(t, os.Symlink(filepath.Join(other, "victim"), filepath.Join(ws, ".git", "MERGE_HEAD")))
		}},
		{"symlink deep in the tree", func(t *testing.T, ws, other string) {
			require.NoError(t, os.MkdirAll(filepath.Join(ws, ".git", "a", "b", "c", "d", "e"), 0o755))
			require.NoError(t, os.Symlink(other, filepath.Join(ws, ".git", "a", "b", "c", "d", "e", "x")))
		}},
		{"symlinked pack", func(t *testing.T, ws, other string) {
			require.NoError(t, os.Symlink(other, filepath.Join(ws, ".git", "objects", "pack", "x")))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs, ws := gitWorkspace(t)
			other := t.TempDir()
			gitCmd(t, other, "init")
			writeFile(t, filepath.Join(other, "secret.txt"), "secret\n")
			gitCmd(t, other, "add", ".")
			gitCmd(t, other, "commit", "-m", "other-repo-secret")
			victim := filepath.Join(other, "victim")
			writeFile(t, victim, "untouched\n")
			tc.setup(t, ws, other)

			_, err := collectDiff(t, fs, GitDiffRequest{RunDir: ws})
			assert.Equal(t, CodeDenied, CodeOf(err))
			lg, err := fs.GitLog(t.Context(), ws, 5)
			assert.Equal(t, CodeDenied, CodeOf(err))
			assert.Empty(t, lg.Commits)
			_, err = fs.GitPull(t.Context(), ws, "")
			assert.Equal(t, CodeDenied, CodeOf(err))
			got, _ := os.ReadFile(victim)
			assert.Equal(t, "untouched\n", string(got))
		})
	}
}

func TestGitConfigBeyondTheVetLimitIsRefused(t *testing.T) {
	fs, ws := gitWorkspace(t)
	old := vetConfigCap
	vetConfigCap = 1 << 20
	t.Cleanup(func() { vetConfigCap = old })
	marker := filepath.Join(t.TempDir(), "pwned")
	var b strings.Builder
	b.WriteString("[pad]\n")
	for i := 0; b.Len() < 2<<20; i++ {
		fmt.Fprintf(&b, "\tk%d = %s\n", i, strings.Repeat("v", 60))
	}
	fmt.Fprintf(&b, "[filter \"x\"]\n\tclean = touch %s\n", marker)
	f, err := os.OpenFile(filepath.Join(ws, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(b.String())
	require.NoError(t, err)
	require.NoError(t, f.Close())
	writeFile(t, filepath.Join(ws, ".gitattributes"), "* filter=x\n")

	_, err = collectDiff(t, fs, GitDiffRequest{RunDir: ws})
	assert.Equal(t, CodeDenied, CodeOf(err))
	assert.ErrorContains(t, err, "could not be vetted")
	assert.NoFileExists(t, marker)

	// Within the cap the same hostile key is found and named instead.
	vetConfigCap = 64 << 20
	_, err = collectDiff(t, fs, GitDiffRequest{RunDir: ws})
	assert.Equal(t, CodeDenied, CodeOf(err))
	assert.ErrorContains(t, err, "filter.x.clean")
	assert.NoFileExists(t, marker)
}

func TestGitRefusesUnlistableConfig(t *testing.T) {
	fs, ws := gitWorkspace(t)
	writeFile(t, filepath.Join(ws, ".git", "config"), "[broken\n")
	_, err := collectDiff(t, fs, GitDiffRequest{RunDir: ws})
	assert.Equal(t, CodeDenied, CodeOf(err))
	assert.ErrorContains(t, err, "could not be vetted")
}

// fakeSSH points git at a stand-in ssh that runs the remote command locally,
// so tests can use ssh:// remotes (the only transports the runner allows).
func fakeSSH(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fakessh")
	script := "#!/bin/sh\nwhile [ \"${1#-}\" != \"$1\" ]; do\n  if [ \"$1\" = -o ] || [ \"$1\" = -p ]; then shift; fi\n  shift\ndone\nshift\nexec sh -c \"$1\"\n"
	require.NoError(t, os.WriteFile(p, []byte(script), 0o755))
	t.Setenv("GIT_SSH_COMMAND", p)
}

func sshURL(p string) string { return "ssh://localhost" + p }

func TestGitPushPullRefuseLocalAndNonNetworkRemotes(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  func(remote, marker string) string
		cfg  func(t *testing.T, ws, remote string)
	}{
		{"local path", func(remote, _ string) string { return remote }, nil},
		{"file url", func(remote, _ string) string { return "file://" + remote }, nil},
		{"relative path", func(remote, _ string) string { return "../" + filepath.Base(remote) }, nil},
		{"ext transport", func(_, marker string) string { return "ext::sh -c touch% " + marker }, nil},
		{"pushurl is a local path", func(remote, _ string) string { return "ssh://localhost/x" }, func(t *testing.T, ws, remote string) {
			gitCmd(t, ws, "config", "--local", "remote.origin.pushurl", remote)
		}},
		{"pushRemote names a local path", func(remote, _ string) string { return "ssh://localhost/x" }, func(t *testing.T, ws, remote string) {
			gitCmd(t, ws, "config", "--local", "branch.main.pushRemote", remote)
		}},
		{"pushDefault names a local path", func(remote, _ string) string { return "ssh://localhost/x" }, func(t *testing.T, ws, remote string) {
			gitCmd(t, ws, "config", "--local", "remote.pushDefault", remote)
		}},
		{"branch remote is dot", func(remote, _ string) string { return "ssh://localhost/x" }, func(t *testing.T, ws, remote string) {
			gitCmd(t, ws, "config", "--local", "branch.main.remote", ".")
		}},
		{"second remote is local", func(remote, _ string) string { return "ssh://localhost/x" }, func(t *testing.T, ws, remote string) {
			gitCmd(t, ws, "remote", "add", "other", remote)
		}},
		{"pushInsteadOf to a local path", func(remote, _ string) string { return "ssh://evil.invalid/p" }, func(t *testing.T, ws, remote string) {
			gitCmd(t, ws, "config", "--local", "url."+remote+".pushInsteadOf", "ssh://evil.invalid/p")
		}},
		{"insteadOf rewrite to a local path", func(remote, _ string) string { return "ssh://evil.invalid/x" }, func(t *testing.T, ws, remote string) {
			gitCmd(t, ws, "config", "--local", "url."+remote+".insteadOf", "ssh://evil.invalid/x")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs, ws := gitWorkspace(t)
			marker := filepath.Join(t.TempDir(), "hook-ran")
			remote := filepath.Join(t.TempDir(), "remote.git")
			gitCmd(t, filepath.Dir(remote), "init", "--bare", remote)
			for _, hook := range []string{"pre-receive", "update", "post-receive", "post-update", "pre-push"} {
				writeFile(t, filepath.Join(remote, "hooks", hook), "#!/bin/sh\ntouch "+marker+"\n")
				require.NoError(t, os.Chmod(filepath.Join(remote, "hooks", hook), 0o755))
			}
			gitCmd(t, ws, "remote", "add", "origin", tc.url(remote, marker))
			gitCmd(t, ws, "config", "--local", "branch.main.remote", "origin")
			gitCmd(t, ws, "config", "--local", "branch.main.merge", "refs/heads/main")
			if tc.cfg != nil {
				tc.cfg(t, ws, remote)
			}
			for name, op := range map[string]func() (GitOpResult, error){
				"push": func() (GitOpResult, error) { return fs.GitPush(t.Context(), ws, "") },
				"pull": func() (GitOpResult, error) { return fs.GitPull(t.Context(), ws, "") },
			} {
				res, err := op()
				if err == nil {
					assert.False(t, res.Success, name)
				} else {
					assert.Equal(t, CodeDenied, CodeOf(err), name)
				}
			}
			assert.NoFileExists(t, marker, "no remote-side hook may run on the runner")
			assert.Empty(t, gitCmd(t, remote, "for-each-ref"), "nothing was pushed")
		})
	}
}

func TestGitNoRemoteDoesNotTreatOriginAsAPath(t *testing.T) {
	fs, ws := gitWorkspace(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	// A directory named like the default remote, with hooks, inside the repo.
	gitCmd(t, ws, "init", "--bare", filepath.Join(ws, "origin"))
	writeFile(t, filepath.Join(ws, "origin", "hooks", "pre-receive"), "#!/bin/sh\ntouch "+marker+"\n")
	require.NoError(t, os.Chmod(filepath.Join(ws, "origin", "hooks", "pre-receive"), 0o755))
	for _, op := range []func() (GitOpResult, error){
		func() (GitOpResult, error) { return fs.GitPush(t.Context(), ws, "") },
		func() (GitOpResult, error) { return fs.GitPull(t.Context(), ws, "") },
	} {
		res, err := op()
		require.NoError(t, err)
		assert.False(t, res.Success)
		assert.Contains(t, res.Error, "no remote")
	}
	assert.NoFileExists(t, marker)
}

func TestGitRefusesRepoProtocolOverrides(t *testing.T) {
	for _, key := range []string{"protocol.file.allow", "protocol.allow", "protocol.ext.allow", "protocol.foo.allow"} {
		t.Run(key, func(t *testing.T) {
			fs, ws := gitWorkspace(t)
			gitCmd(t, ws, "config", "--local", key, "always")
			_, err := collectDiff(t, fs, GitDiffRequest{RunDir: ws})
			assert.Equal(t, CodeDenied, CodeOf(err))
			_, err = fs.GitPush(t.Context(), ws, "")
			assert.Equal(t, CodeDenied, CodeOf(err))
		})
	}
}

func TestRemoteURLAllowed(t *testing.T) {
	t.Parallel()
	for u, want := range map[string]bool{
		"https://github.com/a/b.git": true, "HTTP://h/x": true, "ssh://git@h/x": true, "git://h/x": true,
		"git@github.com:a/b.git": true, "h:path": true,
		"": false, "origin": false, ".": false, "..": false, "/srv/repo.git": false, "./repo": false, "../repo": false, "file:///x": false, "file:x": false,
		"ext::sh -c id": false, "-oProxyCommand=x": false, "C:/x": true,
	} {
		assert.Equal(t, want, remoteURLAllowed(u), u)
	}
}

func TestParseNameStatusZ(t *testing.T) {
	t.Parallel()
	got := parseNameStatus("M\x00a b.txt\x00A\x00t\tab\x00R100\x00old\nname\x00new \"q\"\x00D\x00gone\x00")
	assert.Equal(t, []nameStatusEntry{
		{"M", "a b.txt", "a b.txt"},
		{"A", "t\tab", "t\tab"},
		{"R", "old\nname", "new \"q\""},
		{"D", "gone", "gone"},
	}, got)
	// git show --pretty=format: -z starts with a stray separator.
	assert.Equal(t, []nameStatusEntry{{"M", "x", "x"}}, parseNameStatus("\x00M\x00x\x00"))
	assert.Empty(t, parseNameStatus(""))
	assert.Empty(t, parseNameStatus("R100\x00only-one"))
}

func TestGitDiffUnusualPathNames(t *testing.T) {
	fs, ws, _ := confinedFS(t)
	gitCmd(t, ws, "init")
	names := []string{"sp ace.txt", "tab\there.txt", "new\nline.txt", `qu"ote.txt`, "uni\u00e9.txt", "-dash.txt"}
	for _, n := range names {
		writeFile(t, filepath.Join(ws, n), "v1\n")
	}
	writeFile(t, filepath.Join(ws, "old name.txt"), "rename me\nline2\nline3\n")
	gitCmd(t, ws, "add", "-A")
	gitCmd(t, ws, "commit", "-m", "first")
	for _, n := range names {
		writeFile(t, filepath.Join(ws, n), "v2\n")
	}
	gitCmd(t, ws, "mv", "old name.txt", "new\tname \"x\".txt")
	writeFile(t, filepath.Join(ws, "untracked\nfile.txt"), "u\n")
	check := func(files map[string]GitDiffFile) {
		for _, n := range names {
			require.Contains(t, files, n)
			assert.Equal(t, "M", files[n].Status, n)
			assert.Equal(t, "v1\n", files[n].OldContent, n)
			assert.Equal(t, "v2\n", files[n].NewContent, n)
			require.NotEmpty(t, files[n].Hunks, n)
			assert.Contains(t, files[n].Hunks[0], "+v2", n)
		}
	}
	files, err := collectDiff(t, fs, GitDiffRequest{RunDir: ws})
	require.NoError(t, err)
	check(files)
	require.Contains(t, files, "new\tname \"x\".txt")
	assert.Equal(t, "R", files["new\tname \"x\".txt"].Status)
	assert.Equal(t, "old name.txt", files["new\tname \"x\".txt"].OldPath)
	require.Contains(t, files, "untracked\nfile.txt")
	assert.Equal(t, "A", files["untracked\nfile.txt"].Status)
	assert.Equal(t, "u\n", files["untracked\nfile.txt"].NewContent)

	gitCmd(t, ws, "add", "-A")
	gitCmd(t, ws, "commit", "-m", "second")
	byCommit, err := collectDiff(t, fs, GitDiffRequest{RunDir: ws, Commit: gitCmd(t, ws, "rev-parse", "HEAD")})
	require.NoError(t, err)
	check(byCommit)
}

func TestGitRunsThroughTheExecHook(t *testing.T) {
	fs, ws := gitWorkspace(t)
	var specs []GitExecSpec
	fs.GitExec = func(ctx context.Context, spec GitExecSpec) (*exec.Cmd, error) {
		specs = append(specs, spec)
		return exec.CommandContext(ctx, "git", "--version"), nil
	}
	_, _ = collectDiff(t, fs, GitDiffRequest{RunDir: ws})
	_, _ = fs.GitPull(t.Context(), ws, "")
	_, _ = fs.GitPush(t.Context(), ws, "")
	require.NotEmpty(t, specs)
	var sawPull, sawPush, sawRead bool
	for _, sp := range specs {
		assert.Contains(t, sp.Git, "--git-dir="+SandboxRepoDir+"/.git")
		assert.Contains(t, sp.Git, "--work-tree="+SandboxRepoDir)
		assert.Contains(t, sp.Git, "core.hooksPath=/dev/null")
		assert.Contains(t, sp.Git, "protocol.allow=never")
		require.NotNil(t, sp.WorkTree)
		assert.Equal(t, testChat, sp.ChatID)
		switch {
		case sp.ReadWrite && sp.Network:
			sawPull = true
		case !sp.ReadWrite && sp.Network:
			sawPush = true
		case !sp.ReadWrite && !sp.Network:
			sawRead = true
		case sp.ReadWrite && !sp.Network:
			// the config vet that precedes a pull
		default:
			t.Errorf("unexpected mode: rw=%v net=%v", sp.ReadWrite, sp.Network)
		}
	}
	for _, sp := range specs {
		if !sp.ReadWrite && !sp.Network {
			assert.Contains(t, sp.Env, "GIT_OPTIONAL_LOCKS=0", "read-only runs take no optional locks")
		}
	}
	assert.True(t, sawRead, "diff runs read-only without network")
	assert.True(t, sawPull, "pull runs writable with network")
	assert.True(t, sawPush, "push runs read-only with network")
}

func TestGitAcceptsHardLinkedLocalClone(t *testing.T) {
	fs, ws, _ := confinedFS(t)
	src := t.TempDir()
	gitCmd(t, src, "init")
	writeFile(t, filepath.Join(src, "a.txt"), "a\n")
	gitCmd(t, src, "add", ".")
	gitCmd(t, src, "commit", "-m", "from-src")
	// --local hard-links the object files into the clone.
	gitCmd(t, filepath.Dir(ws), "clone", "--local", src, ws)
	linked := 0
	_ = filepath.WalkDir(filepath.Join(ws, ".git", "objects"), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, ierr := d.Info(); ierr == nil && info.Sys().(*syscall.Stat_t).Nlink > 1 {
				linked++
			}
		}
		return nil
	})
	require.Positive(t, linked, "the clone must share object files with its source")

	lg, err := fs.GitLog(t.Context(), ws, 5)
	require.NoError(t, err)
	require.Len(t, lg.Commits, 1)
	assert.Equal(t, "from-src", lg.Commits[0].Message)
}

func TestGitAcceptsHardLinkedModuleObjects(t *testing.T) {
	assert.True(t, gitImmutableFile("objects/ab/cdef"))
	assert.True(t, gitImmutableFile("modules/n/objects/pack/p.pack"))
	assert.True(t, gitImmutableFile("modules/a/b/objects/ab/cd"))
	for _, rel := range []string{"COMMIT_EDITMSG", "modules/n/FETCH_HEAD", "rebase-merge/x", "refs/heads/main", "logs/HEAD", "objectsx/y"} {
		assert.False(t, gitImmutableFile(rel), rel)
	}
}

// pushRepo returns a repo with an ssh remote "origin" (bare), main tracking it.
func pushRepo(t *testing.T) (Local, string, string) {
	t.Helper()
	fs, ws := gitWorkspace(t)
	gitCmd(t, ws, "add", "-A")
	gitCmd(t, ws, "commit", "-m", "second")
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitCmd(t, filepath.Dir(remote), "init", "--bare", remote)
	fakeSSH(t)
	gitCmd(t, ws, "remote", "add", "origin", sshURL(remote))
	gitCmd(t, ws, "config", "--local", "branch.main.remote", "origin")
	gitCmd(t, ws, "config", "--local", "branch.main.merge", "refs/heads/main")
	gitCmd(t, ws, "config", "--local", "push.default", "current")
	return fs, ws, remote
}

func TestGitPushOutputAndTrackingRefWrittenByTheRunner(t *testing.T) {
	fs, ws, remote := pushRepo(t)
	// git cannot update the tracking ref (stand-in for the read-only mount): a
	// stale lock makes it print the expected complaint. The runner writes the
	// ref itself and hides the complaint.
	require.NoError(t, os.MkdirAll(filepath.Join(ws, ".git", "refs", "remotes", "origin"), 0o755))
	writeFile(t, filepath.Join(ws, ".git", "refs", "remotes", "origin", "main.lock"), "")

	res, err := fs.GitPush(t.Context(), ws, "")
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error+res.Output)
	assert.Contains(t, res.Output, "To "+sshURL(remote))
	assert.Contains(t, res.Output, "main -> main", "git's own human-readable output")
	assert.NotContains(t, res.Output, "cannot lock ref 'refs/remotes/")
	assert.NotContains(t, res.Output, "update_ref failed")
	head := gitCmd(t, ws, "rev-parse", "HEAD")
	assert.Equal(t, head, gitCmd(t, remote, "rev-parse", "main"))
	assert.Equal(t, head, gitCmd(t, ws, "rev-parse", "refs/remotes/origin/main"))
}

func TestGitPushOutputCannotChangeWhatIsRecorded(t *testing.T) {
	fs, ws, remote := pushRepo(t)
	// A transport that prints porcelain-looking and To-looking lines.
	p := filepath.Join(t.TempDir(), "evilssh")
	script := "#!/bin/sh\nprintf 'To ssh://localhost/evil\\n*\\trefs/heads/main:refs/heads/../../evil\\t[new branch]\\n!\\trefs/heads/x:refs/heads/x\\t[rejected]\\nDone\\n' >&2\n" +
		"while [ \"${1#-}\" != \"$1\" ]; do shift; done\nshift\nexec sh -c \"$1\"\n"
	require.NoError(t, os.WriteFile(p, []byte(script), 0o755))
	t.Setenv("GIT_SSH_COMMAND", p)
	require.NoError(t, os.MkdirAll(filepath.Join(ws, ".git", "refs", "remotes", "origin"), 0o755))
	writeFile(t, filepath.Join(ws, ".git", "refs", "remotes", "origin", "main.lock"), "")

	res, err := fs.GitPush(t.Context(), ws, "")
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error+res.Output)
	assert.Equal(t, gitCmd(t, ws, "rev-parse", "HEAD"), gitCmd(t, ws, "rev-parse", "refs/remotes/origin/main"))
	entries, _ := os.ReadDir(filepath.Join(ws, ".git", "refs", "remotes"))
	require.Len(t, entries, 1)
	assert.Equal(t, "origin", entries[0].Name())
	assert.NoFileExists(t, filepath.Join(ws, ".git", "evil"))
	assert.NoDirExists(t, filepath.Join(filepath.Dir(ws), "evil"))
	_ = remote
}

func TestGitPushWithAFailingPushURLFails(t *testing.T) {
	fs, ws, remote := pushRepo(t)
	gitCmd(t, ws, "config", "--local", "--add", "remote.origin.pushurl", sshURL(remote))
	gitCmd(t, ws, "config", "--local", "--add", "remote.origin.pushurl", sshURL(filepath.Join(t.TempDir(), "missing.git")))
	require.NoError(t, os.MkdirAll(filepath.Join(ws, ".git", "refs", "remotes", "origin"), 0o755))
	writeFile(t, filepath.Join(ws, ".git", "refs", "remotes", "origin", "main.lock"), "")

	res, err := fs.GitPush(t.Context(), ws, "")
	require.NoError(t, err)
	assert.False(t, res.Success, "one pushurl failed: git exits non-zero")
	assert.NotEmpty(t, res.Error)
	assert.NoFileExists(t, filepath.Join(ws, ".git", "refs", "remotes", "origin", "main"), "no tracking ref after a failed push")
}

func TestGitPushWithCredentialedURLStillRecordsTheTrackingRef(t *testing.T) {
	fs, ws, remote := pushRepo(t)
	gitCmd(t, ws, "remote", "set-url", "origin", "ssh://user@localhost"+remote)
	require.NoError(t, os.MkdirAll(filepath.Join(ws, ".git", "refs", "remotes", "origin"), 0o755))
	writeFile(t, filepath.Join(ws, ".git", "refs", "remotes", "origin", "main.lock"), "")

	res, err := fs.GitPush(t.Context(), ws, "")
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error+res.Output)
	assert.Equal(t, gitCmd(t, ws, "rev-parse", "HEAD"), gitCmd(t, ws, "rev-parse", "refs/remotes/origin/main"))
}

func TestGitPushWithoutAResolvablePushRefLeavesTrackingAlone(t *testing.T) {
	fs, ws, remote := pushRepo(t)
	gitCmd(t, ws, "config", "--local", "push.default", "nothing")
	res, err := fs.GitPush(t.Context(), ws, "")
	require.NoError(t, err)
	assert.False(t, res.Success, "push.default nothing pushes nothing")
	assert.Empty(t, gitCmd(t, remote, "for-each-ref"))
}

func TestGitPushReadonlyMountSkipsTheTrackingWrite(t *testing.T) {
	fs, ws, _ := pushRepo(t)
	fs.ReadonlyDirs = func() []string { return []string{ws} }
	require.NoError(t, os.MkdirAll(filepath.Join(ws, ".git", "refs", "remotes", "origin"), 0o755))
	writeFile(t, filepath.Join(ws, ".git", "refs", "remotes", "origin", "main.lock"), "")
	res, err := fs.GitPush(t.Context(), ws, "")
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error+res.Output)
	assert.NoFileExists(t, filepath.Join(ws, ".git", "refs", "remotes", "origin", "main"), "vetoed: the ref stays stale")
}

func TestGitPushWithPushRefspecsDoesNotGuessTheTrackingRef(t *testing.T) {
	for _, tc := range []struct {
		name, spec string
		success    bool
	}{
		{"mapping to another branch", "refs/heads/main:refs/heads/other", true},
		{"wildcard mapping", "refs/heads/*:refs/heads/mirror/*", true},
		{"spec not covering the branch", "refs/heads/nomatch:refs/heads/nomatch", false},
		{"delete only", ":refs/heads/main", true}, // git accepts deleting a missing ref
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs, ws, _ := pushRepo(t)
			gitCmd(t, ws, "config", "--local", "remote.origin.push", tc.spec)
			res, err := fs.GitPush(t.Context(), ws, "")
			require.NoError(t, err)
			assert.Equal(t, tc.success, res.Success, res.Error+res.Output)
			assert.NoFileExists(t, filepath.Join(ws, ".git", "refs", "remotes", "origin", "main"),
				"nothing was pushed to main, so main's tracking ref must not be recorded")
		})
	}
}

func TestGitPushFallbackRefusesAmbiguousConfig(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, ws string)
	}{
		{"upstream with two merge values", func(t *testing.T, ws string) {
			gitCmd(t, ws, "config", "--local", "push.default", "upstream")
			gitCmd(t, ws, "config", "--local", "branch.main.merge", "refs/heads/first")
			gitCmd(t, ws, "config", "--local", "--add", "branch.main.merge", "refs/heads/main")
		}},
		{"standard fetch refspec not alone", func(t *testing.T, ws string) {
			gitCmd(t, ws, "config", "--local", "--replace-all", "remote.origin.fetch", "+refs/heads/main:refs/remotes/origin/elsewhere")
			gitCmd(t, ws, "config", "--local", "--add", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
		}},
		{"standard fetch refspec twice", func(t *testing.T, ws string) {
			gitCmd(t, ws, "config", "--local", "--add", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs, ws, _ := pushRepo(t)
			tc.setup(t, ws)
			res, err := fs.GitPush(t.Context(), ws, "")
			require.NoError(t, err)
			_ = res // the push result is git's own either way
			// git may write tracking refs itself unsandboxed; the runner must not
			// have guessed one when its own derivation is ambiguous.
			r, err := fs.openRepo(t.Context(), ws, false)
			require.NoError(t, err)
			t.Cleanup(r.close)
			r.knownRemotes = map[string]bool{"origin": true}
			assert.Empty(t, r.configuredPushRef(t.Context()))
		})
	}
}

func TestGitPushFallbackAcceptsTheSingleStandardSetup(t *testing.T) {
	fs, ws, _ := pushRepo(t)
	gitCmd(t, ws, "config", "--local", "push.default", "upstream")
	r, err := fs.openRepo(t.Context(), ws, false)
	require.NoError(t, err)
	t.Cleanup(r.close)
	r.knownRemotes = map[string]bool{"origin": true}
	assert.Equal(t, "refs/remotes/origin/main", r.configuredPushRef(t.Context()))
}
