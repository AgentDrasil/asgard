package accounts

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSID = "11111111-2222-3333-4444-555555555555"

func writeF(t *testing.T, p, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func setupClaude(t *testing.T, aliases ...string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(EnvAccountsDir, root)
	for _, a := range aliases {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "claude", a), 0o700))
	}
	return root
}

func TestCopyClaudeSession(t *testing.T) {
	root := setupClaude(t, "a", "b")
	a := filepath.Join(root, "claude", "a")
	b := filepath.Join(root, "claude", "b")
	writeF(t, filepath.Join(a, "projects", "-p1", testSID+".jsonl"), "log")
	writeF(t, filepath.Join(a, "projects", "-p1", testSID, "subagents", "x.jsonl"), "sub")
	writeF(t, filepath.Join(a, "projects", "-p1", "other.jsonl"), "other")
	writeF(t, filepath.Join(a, "file-history", testSID, "h"), "h")
	writeF(t, filepath.Join(a, "session-env", testSID, "e"), "e")
	writeF(t, filepath.Join(a, "todos", testSID+"-agent-"+testSID+".json"), "[]")
	writeF(t, filepath.Join(a, "todos", "zzzzzzzz-agent.json"), "[]")
	writeF(t, filepath.Join(a, ".credentials.json"), "secret")
	writeF(t, filepath.Join(a, ".claude.json"), "cfg")
	writeF(t, filepath.Join(a, "sessions", "1.json"), "lock")
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(a, "projects", "-p1", testSID, "link")))

	require.NoError(t, CopyClaudeSession("a", "b", testSID))

	for _, rel := range []string{
		"projects/-p1/" + testSID + ".jsonl",
		"projects/-p1/" + testSID + "/subagents/x.jsonl",
		"file-history/" + testSID + "/h",
		"session-env/" + testSID + "/e",
		"todos/" + testSID + "-agent-" + testSID + ".json",
	} {
		fi, err := os.Stat(filepath.Join(b, rel))
		require.NoError(t, err, rel)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), rel)
	}
	for _, rel := range []string{".credentials.json", ".claude.json", "sessions", "projects/-p1/other.jsonl", "todos/zzzzzzzz-agent.json", "projects/-p1/" + testSID + "/link"} {
		_, err := os.Lstat(filepath.Join(b, rel))
		assert.True(t, os.IsNotExist(err), rel)
	}
	fi, err := os.Stat(filepath.Join(b, "projects", "-p1"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
}

func TestCopyClaudeSession_NoOverwriteNewer(t *testing.T) {
	root := setupClaude(t, "a", "b")
	jl := "projects/-p/" + testSID + ".jsonl"
	writeF(t, filepath.Join(root, "claude", "a", jl), "short")
	writeF(t, filepath.Join(root, "claude", "b", jl), "longer existing")
	require.NoError(t, CopyClaudeSession("a", "b", testSID))
	got, err := os.ReadFile(filepath.Join(root, "claude", "b", jl))
	require.NoError(t, err)
	assert.Equal(t, "longer existing", string(got))

	writeF(t, filepath.Join(root, "claude", "a", jl), "a much longer source log")
	require.NoError(t, CopyClaudeSession("a", "b", testSID))
	got, _ = os.ReadFile(filepath.Join(root, "claude", "b", jl))
	assert.Equal(t, "a much longer source log", string(got))
}

func TestCopyClaudeSession_Invalid(t *testing.T) {
	root := setupClaude(t, "a", "b")
	writeF(t, filepath.Join(root, "claude", "a", "projects", "p", testSID+".jsonl"), "x")
	for _, sid := range []string{"", "short", "../../../etc/passwd", testSID + "/..", "a b c d e f g h", "ünicode-id-12345"} {
		assert.Error(t, CopyClaudeSession("a", "b", sid), sid)
	}
	assert.Error(t, CopyClaudeSession("../a", "b", testSID))
	assert.Error(t, CopyClaudeSession("a", "B", testSID))
	assert.Error(t, CopyClaudeSession("a", "a", testSID))
	assert.Error(t, CopyClaudeSession("a", "missing", testSID))
	assert.Error(t, CopyClaudeSession("b", "a", testSID), "session absent in source")
}

func TestCopyClaudeSession_SymlinkedProjectsNotFollowed(t *testing.T) {
	root := setupClaude(t, "a", "b")
	outside := t.TempDir()
	writeF(t, filepath.Join(outside, "p", testSID+".jsonl"), "x")
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "claude", "a", "projects")))
	assert.Error(t, CopyClaudeSession("a", "b", testSID))
	_, ok, err := FindClaudeSession(testSID)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestFindClaudeSession(t *testing.T) {
	root := setupClaude(t, "a", "b", "c")
	_, ok, err := FindClaudeSession(testSID)
	require.NoError(t, err)
	assert.False(t, ok)

	writeF(t, filepath.Join(root, "claude", "c", "projects", "p", testSID+".jsonl"), "x")
	writeF(t, filepath.Join(root, "claude", "b", "projects", "q", testSID+".jsonl"), "x")
	alias, ok, err := FindClaudeSession(testSID)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "b", alias)

	_, _, err = FindClaudeSession("../x")
	assert.Error(t, err)
}

func TestFindClaudeSession_Newest(t *testing.T) {
	root := setupClaude(t, "a", "b", "c")
	jl := "projects/p/" + testSID + ".jsonl"
	writeF(t, filepath.Join(root, "claude", "a", jl), "short")
	writeF(t, filepath.Join(root, "claude", "b", jl), "much longer log")
	alias, ok, err := FindClaudeSession(testSID)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "b", alias, "largest wins")

	// Equal size: newest mtime wins, then alias order.
	writeF(t, filepath.Join(root, "claude", "c", jl), "much longer log")
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(root, "claude", "b", jl), old, old))
	alias, _, _ = FindClaudeSession(testSID)
	assert.Equal(t, "c", alias)
	require.NoError(t, os.Chtimes(filepath.Join(root, "claude", "c", jl), old, old))
	alias, _, _ = FindClaudeSession(testSID)
	assert.Equal(t, "b", alias)
}

func TestCopyClaudeSession_DstSymlinksRefused(t *testing.T) {
	for _, tc := range []struct{ name, link string }{
		{"projects", "projects"},
		{"proj", "projects/-p1"},
		{"file-history", "file-history"},
		{"session-env", "session-env"},
		{"todos", "todos"},
		{"sid-dir", "projects/-p1/" + testSID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := setupClaude(t, "a", "b")
			a := filepath.Join(root, "claude", "a")
			b := filepath.Join(root, "claude", "b")
			writeF(t, filepath.Join(a, "projects", "-p1", testSID+".jsonl"), "log")
			writeF(t, filepath.Join(a, "projects", "-p1", testSID, "x"), "sub")
			writeF(t, filepath.Join(a, "file-history", testSID, "h"), "h")
			writeF(t, filepath.Join(a, "session-env", testSID, "e"), "e")
			writeF(t, filepath.Join(a, "todos", testSID+"-agent.json"), "[]")
			outside := t.TempDir()
			if filepath.Dir(tc.link) != "." {
				require.NoError(t, os.MkdirAll(filepath.Join(b, filepath.Dir(tc.link)), 0o700))
			}
			require.NoError(t, os.Symlink(outside, filepath.Join(b, tc.link)))
			assert.Error(t, CopyClaudeSession("a", "b", testSID))
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			assert.Empty(t, entries, "nothing written outside")
		})
	}
}

func TestCopyClaudeSession_JsonlLast(t *testing.T) {
	root := setupClaude(t, "a", "b")
	a := filepath.Join(root, "claude", "a")
	b := filepath.Join(root, "claude", "b")
	writeF(t, filepath.Join(a, "projects", "-p1", testSID+".jsonl"), "log")
	writeF(t, filepath.Join(a, "file-history", testSID, "h"), "h")
	// Ancillary copy fails: destination file-history is a regular file.
	writeF(t, filepath.Join(b, "file-history"), "not a dir")
	assert.Error(t, CopyClaudeSession("a", "b", testSID))
	m, _ := filepath.Glob(filepath.Join(b, "projects", "*", "*.jsonl"))
	assert.Empty(t, m)
}

func TestCopyClaudeSession_SourceSymlinkJsonlSkipped(t *testing.T) {
	root := setupClaude(t, "a", "b")
	a := filepath.Join(root, "claude", "a")
	outside := filepath.Join(t.TempDir(), "x.jsonl")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(a, "projects", "p"), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(a, "projects", "p", testSID+".jsonl")))
	assert.Error(t, CopyClaudeSession("a", "b", testSID))
}
