package runnerfs

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

const testChat = "chat1"

// confinedFS returns a Local confined to a temp workspace plus an outside dir.
func confinedFS(t *testing.T) (fs Local, ws, outside string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ws = filepath.Join(t.TempDir(), "ws")
	outside = t.TempDir()
	require.NoError(t, os.MkdirAll(ws, 0o755))
	return NewConfined(testChat, func(bool) []string { return []string{ws} }, nil), ws, outside
}

func wref(ws, p string) PathRef { return PathRef{RunDir: ws, Path: p, Scope: "workspace"} }

func TestLocalSymlinkEscapes(t *testing.T) {
	fs, ws, outside := confinedFS(t)
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(ws, "final-existing")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "new"), filepath.Join(ws, "final-dangling")))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "dir-link")))
	require.NoError(t, os.Symlink("../../escape-rel", filepath.Join(ws, "rel-dangling")))

	for _, p := range []string{"final-existing", "final-dangling", "dir-link/new", "dir-link/secret", "rel-dangling", "dir-link/sub/new"} {
		t.Run(p, func(t *testing.T) {
			_, err := fs.WriteFile(t.Context(), WriteRequest{Ref: wref(ws, p), Mkdir: true}, strings.NewReader("pwn"))
			assert.Error(t, err)
			assert.Error(t, fs.AppendFile(t.Context(), AppendRequest{Ref: wref(ws, p), Mkdir: true, Data: []byte("pwn")}))
			_, err = fs.Open(t.Context(), ReadRequest{RunDir: ws, Path: p, Scope: "workspace"})
			assert.Error(t, err)
			// An atomic write may replace a link inside the root (it renames
			// over the link itself) but must never write through it; the
			// checks on the outside dir below prove that.
			_, _ = fs.WriteFile(t.Context(), WriteRequest{Ref: wref(ws, p), Mkdir: true, Atomic: true}, strings.NewReader("pwn"))
		})
	}
	// Nothing was created or modified outside.
	entries, _ := os.ReadDir(outside)
	assert.Len(t, entries, 1)
	got, _ := os.ReadFile(filepath.Join(outside, "secret"))
	assert.Equal(t, "s", string(got))
	assert.NoFileExists(t, filepath.Join(outside, "new"))
	assert.NoFileExists(t, filepath.Join(filepath.Dir(ws), "escape-rel"))

	// Remove deletes the link itself, never its target.
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(ws, "again")))
	// Links leading out of the workspace are refused by the shared resolver
	// (same as for reads); either way the targets stay untouched.
	assert.Error(t, fs.Remove(t.Context(), wref(ws, "again"), false, ""))
	assert.Error(t, fs.Remove(t.Context(), wref(ws, "dir-link"), true, ""))
	assert.FileExists(t, filepath.Join(outside, "secret"))
}

func TestLocalInternalSymlinksStillWork(t *testing.T) {
	fs, ws, _ := confinedFS(t)
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "real"), 0o755))
	require.NoError(t, os.Symlink("real", filepath.Join(ws, "alias")))
	_, err := fs.WriteFile(t.Context(), WriteRequest{Ref: wref(ws, "alias/f.txt")}, strings.NewReader("ok"))
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(ws, "real", "f.txt"))
}

func TestLocalTraversalAndRunDirs(t *testing.T) {
	fs, ws, outside := confinedFS(t)
	for _, p := range []string{"../x", "a/../../x", "/etc/passwd", "/usr/x"} {
		_, err := fs.WriteFile(t.Context(), WriteRequest{Ref: PathRef{RunDir: ws, Path: p}, Mkdir: true}, strings.NewReader("x"))
		assert.Equal(t, CodeDenied, CodeOf(err), p)
	}
	require.NoError(t, os.WriteFile(filepath.Join(outside, "x"), []byte("x"), 0o644))
	for _, dir := range []string{"", "/", "rel/dir", outside, filepath.Dir(ws), "../.."} {
		_, err := fs.Open(t.Context(), ReadRequest{RunDir: dir, Path: "x"})
		assert.Error(t, err, "run dir %q", dir)
		_, err = fs.Stat(t.Context(), PathRef{RunDir: dir, Path: "x"})
		assert.Error(t, err, "run dir %q", dir)
	}
	// Chat tmp/session namespaces stay reachable without any allowed dir.
	bare := NewConfined(testChat, nil, nil)
	_, err := bare.WriteFile(t.Context(), WriteRequest{Ref: PathRef{Path: "/tmp/a.txt", Scope: "tmp"}, Mkdir: true}, strings.NewReader("ok"))
	require.NoError(t, err)
	st, err := bare.Stat(t.Context(), PathRef{Path: "/tmp/a.txt"})
	require.NoError(t, err)
	assert.True(t, st.Exists)
}

func TestLocalSizeCaps(t *testing.T) {
	fs, ws, _ := confinedFS(t)
	big := make([]byte, MaxWriteSize+1)
	for _, req := range []WriteRequest{
		{Ref: wref(ws, "a")}, {Ref: wref(ws, "b"), Atomic: true}, {Ref: wref(ws, "c"), SkipIfUnchanged: true},
	} {
		_, err := fs.WriteFile(t.Context(), req, bytes.NewReader(big))
		assert.Equal(t, CodeTooLarge, CodeOf(err), req.Ref.Path)
	}
	entries, _ := os.ReadDir(ws)
	assert.Empty(t, entries)
	assert.Equal(t, CodeTooLarge, CodeOf(fs.AppendFile(t.Context(), AppendRequest{Ref: wref(ws, "d"), Data: big})))

	_, err := fs.PutAttachment(t.Context(), "", "f.bin", bytes.NewReader(make([]byte, MaxAttachmentSize+1)))
	assert.Equal(t, CodeTooLarge, CodeOf(err))
	_, err = fs.PutAttachment(t.Context(), "", "ok.bin", bytes.NewReader(make([]byte, MaxAttachmentSize)))
	assert.NoError(t, err)
}

func TestLocalRefusesNonRegularFiles(t *testing.T) {
	fs, ws, _ := confinedFS(t)
	require.NoError(t, os.Mkdir(filepath.Join(ws, "d"), 0o755))
	_, err := fs.Open(t.Context(), ReadRequest{RunDir: ws, Path: "d", Scope: "workspace"})
	assert.Equal(t, CodeIsDir, CodeOf(err))
}

func TestLocalFallbackWithoutOpenat2(t *testing.T) {
	noOpenat2.Store(true)
	t.Cleanup(func() { noOpenat2.Store(false) })
	fs, ws, outside := confinedFS(t)
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "dir-link")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "new"), filepath.Join(ws, "dangling")))

	_, err := fs.WriteFile(t.Context(), WriteRequest{Ref: wref(ws, "ok/f.txt"), Mkdir: true}, strings.NewReader("ok"))
	require.NoError(t, err)
	f, err := fs.Open(t.Context(), ReadRequest{RunDir: ws, Path: "ok/f.txt", Scope: "workspace"})
	require.NoError(t, err)
	_ = f.Close()
	for _, p := range []string{"dir-link/new", "dangling"} {
		_, err = fs.WriteFile(t.Context(), WriteRequest{Ref: wref(ws, p), Mkdir: true}, strings.NewReader("pwn"))
		assert.Error(t, err, p)
		assert.Error(t, fs.AppendFile(t.Context(), AppendRequest{Ref: wref(ws, p), Data: []byte("x")}), p)
	}
	assert.NoFileExists(t, filepath.Join(outside, "new"))
}

func TestLocalRunDirSwappedForSymlinkBetweenCheckAndUse(t *testing.T) {
	fs, ws, outside := confinedFS(t)
	proj := filepath.Join(ws, "proj")
	require.NoError(t, os.MkdirAll(filepath.Join(proj, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644))

	testHookBeforeRunDirOpen = func() {
		// The attacker replaces a run dir component with a symlink out.
		_ = os.RemoveAll(proj)
		_ = os.Symlink(outside, proj)
	}
	t.Cleanup(func() { testHookBeforeRunDirOpen = nil })

	runDir := filepath.Join(proj, "sub")
	_, err := fs.Open(t.Context(), ReadRequest{RunDir: runDir, Path: "secret", Scope: "workspace"})
	assert.Equal(t, CodeDenied, CodeOf(err))
	_, err = fs.WriteFile(t.Context(), WriteRequest{Ref: PathRef{RunDir: runDir, Path: "new", Scope: "workspace"}}, strings.NewReader("x"))
	assert.Equal(t, CodeDenied, CodeOf(err))
	assert.NoFileExists(t, filepath.Join(outside, "new"))
}

func TestLocalMultipleRootsAndNamespacesWithoutRunDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Not under /tmp: absolute /tmp/... paths mean the chat's tmp namespace.
	a, b, ro := localDir(t), localDir(t), localDir(t)
	fs := NewConfined(testChat, func(write bool) []string {
		if write {
			return []string{a, b}
		}
		return []string{a, b, ro}
	}, func() []string { return []string{ro} })
	require.NoError(t, os.WriteFile(filepath.Join(b, "f.txt"), []byte("in b"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ro, "r.txt"), []byte("ro"), 0o644))

	// Run dir in a, absolute path in another allowed root b.
	f, err := fs.Open(t.Context(), ReadRequest{RunDir: a, Path: filepath.Join(b, "f.txt")})
	require.NoError(t, err)
	_ = f.Close()
	// Readonly mounts: readable, not writable.
	f, err = fs.Open(t.Context(), ReadRequest{RunDir: a, Path: filepath.Join(ro, "r.txt")})
	require.NoError(t, err)
	_ = f.Close()
	_, err = fs.WriteFile(t.Context(), WriteRequest{Ref: PathRef{RunDir: a, Path: filepath.Join(ro, "w.txt")}}, strings.NewReader("x"))
	assert.Equal(t, CodeDenied, CodeOf(err))
	_, err = fs.WriteFile(t.Context(), WriteRequest{Ref: PathRef{RunDir: a, Path: filepath.Join(b, "w.txt")}}, strings.NewReader("x"))
	assert.NoError(t, err)

	// Namespaced paths need no run dir at all.
	_, err = fs.WriteFile(t.Context(), WriteRequest{Ref: PathRef{Path: "/tmp/n.txt"}, Mkdir: true}, strings.NewReader("ns"))
	require.NoError(t, err)
	f, err = fs.Open(t.Context(), ReadRequest{Path: "/tmp/n.txt"})
	require.NoError(t, err)
	assert.Equal(t, "/tmp/n.txt", f.Path)
	_ = f.Close()
	_, err = fs.Open(t.Context(), ReadRequest{Path: "/etc/passwd"})
	assert.Equal(t, CodeDenied, CodeOf(err))
}

func TestValidAttachmentName(t *testing.T) {
	for name, want := range map[string]bool{
		"a.txt": true, "a..b": true, "..hidden": true, "x...": true, "..": false, ".": false, "": false,
		"a/b": false, `a\b`: false, "a\x00b": false, strings.Repeat("a", 256): false, strings.Repeat("a", 255): true,
	} {
		assert.Equal(t, want, ValidAttachmentName(name), "%q", name)
	}
}

// localDir creates a temp dir under the package dir (outside /tmp).
func localDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp(".", "tmpdir-")
	require.NoError(t, err)
	abs, err := filepath.Abs(d)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	return abs
}

func TestLocalReadonlyMountVetoesWritesInsideWritableRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rw := localDir(t)
	ro := filepath.Join(rw, "docs")
	require.NoError(t, os.MkdirAll(ro, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ro, "r.txt"), []byte("r"), 0o644))
	require.NoError(t, os.Symlink("docs", filepath.Join(rw, "alias")))
	fs := NewConfined(testChat, func(bool) []string { return []string{rw, ro} }, func() []string { return []string{ro} })

	ref := func(p string) PathRef { return PathRef{RunDir: rw, Path: p} }
	for _, p := range []string{filepath.Join(ro, "r.txt"), filepath.Join(ro, "new.txt"), filepath.Join(ro, "sub", "n.txt"), "alias/new.txt", "docs/new.txt"} {
		_, err := fs.WriteFile(t.Context(), WriteRequest{Ref: ref(p), Mkdir: true}, strings.NewReader("x"))
		assert.Equal(t, CodeDenied, CodeOf(err), "write %s", p)
		assert.Equal(t, CodeDenied, CodeOf(fs.AppendFile(t.Context(), AppendRequest{Ref: ref(p), Mkdir: true, Data: []byte("x")})), "append %s", p)
	}
	// Existing readonly file: remove is vetoed (a missing one is a no-op).
	assert.Equal(t, CodeDenied, CodeOf(fs.Remove(t.Context(), ref(filepath.Join(ro, "r.txt")), false, "")))
	got, _ := os.ReadFile(filepath.Join(ro, "r.txt"))
	assert.Equal(t, "r", string(got))
	assert.NoFileExists(t, filepath.Join(ro, "new.txt"))
	// Reads of the readonly mount and writes elsewhere in rw still work.
	f, err := fs.Open(t.Context(), ReadRequest{RunDir: rw, Path: filepath.Join(ro, "r.txt")})
	require.NoError(t, err)
	_ = f.Close()
	_, err = fs.WriteFile(t.Context(), WriteRequest{Ref: ref("ok.txt")}, strings.NewReader("x"))
	assert.NoError(t, err)
}

func TestLocalNestedRootsOutermostFirstNoFallThrough(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	outer := localDir(t)
	outside := localDir(t)
	inner := filepath.Join(outer, "inner")
	require.NoError(t, os.MkdirAll(inner, 0o755))
	fs := NewConfined(testChat, func(bool) []string { return []string{outer, inner} }, nil)

	// The inner root's directory entry is replaced by a symlink from the
	// writable outer root.
	require.NoError(t, os.RemoveAll(inner))
	require.NoError(t, os.Symlink(outside, inner))

	_, err := fs.WriteFile(t.Context(), WriteRequest{Ref: PathRef{RunDir: outer, Path: filepath.Join(inner, "pwn.txt")}}, strings.NewReader("x"))
	assert.Error(t, err)
	assert.NoFileExists(t, filepath.Join(outside, "pwn.txt"))
	_, err = fs.Open(t.Context(), ReadRequest{RunDir: outer, Path: filepath.Join(inner, "pwn.txt")})
	assert.Error(t, err)
	// Using the inner path as a run dir must not succeed through the inner root either.
	_, err = fs.WriteFile(t.Context(), WriteRequest{Ref: PathRef{RunDir: inner, Path: "pwn.txt", Scope: "workspace"}}, strings.NewReader("x"))
	assert.Error(t, err)
	assert.NoFileExists(t, filepath.Join(outside, "pwn.txt"))
}

func TestLocalRootUnderSymlinkIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	real := localDir(t)
	link := real + "-link"
	require.NoError(t, os.Symlink(real, link))
	t.Cleanup(func() { _ = os.Remove(link) })
	require.NoError(t, os.MkdirAll(filepath.Join(real, "sub"), 0o755))

	for _, root := range []string{link, filepath.Join(link, "sub")} {
		fs := NewConfined(testChat, func(bool) []string { return []string{root} }, nil)
		target := filepath.Join(root, "f.txt")
		_, err := fs.WriteFile(t.Context(), WriteRequest{Ref: PathRef{RunDir: root, Path: target}}, strings.NewReader("x"))
		assert.Equal(t, CodeDenied, CodeOf(err), root)
		assert.ErrorContains(t, err, "symlink")
	}
	assert.NoFileExists(t, filepath.Join(real, "f.txt"))
}

func TestRunDirRefMapsChatDirsAcrossHomes(t *testing.T) {
	for _, home := range []string{t.TempDir(), t.TempDir()} {
		t.Setenv("HOME", home)
		tmp := filepath.Join(home, "asgard", "data", "tmp", testChat)
		sess := filepath.Join(home, "asgard", "data", "sessions", testChat)
		ref := RunDirRef(testChat, tmp)
		assert.Equal(t, runnerpb.FileScope_FILE_SCOPE_TMP, ref.GetScope())
		assert.Empty(t, ref.GetRelOrPath())
		ref = RunDirRef(testChat, filepath.Join(sess, "sub"))
		assert.Equal(t, runnerpb.FileScope_FILE_SCOPE_SESSION, ref.GetScope())
		assert.Equal(t, "sub", ref.GetRelOrPath())
		// Another chat's dir, or a foreign host path, stays absolute.
		other := RunDirRef(testChat, filepath.Join(home, "asgard", "data", "tmp", "other"))
		assert.Equal(t, runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, other.GetScope())
		assert.Nil(t, RunDirRef(testChat, ""))
	}
}

func TestLocalRecursiveRemoveSparesReadonlyMountBelow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rw := localDir(t)
	sub := filepath.Join(rw, "sub")
	docs := filepath.Join(sub, "docs")
	require.NoError(t, os.MkdirAll(docs, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(docs, "keep.txt"), []byte("k"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "other.txt"), []byte("o"), 0o644))
	require.NoError(t, os.Symlink("sub", filepath.Join(rw, "alias")))
	fs := NewConfined(testChat, func(bool) []string { return []string{rw, docs} }, func() []string { return []string{docs} })

	for _, p := range []string{sub, "sub", "alias/docs", filepath.Join(rw, "alias", "docs")} {
		err := fs.Remove(t.Context(), PathRef{RunDir: rw, Path: p}, true, "")
		assert.Equal(t, CodeDenied, CodeOf(err), p)
	}
	assert.FileExists(t, filepath.Join(docs, "keep.txt"))
	assert.FileExists(t, filepath.Join(sub, "other.txt"), "refused up front, nothing removed")

	// mkdir through an alias into the readonly mount creates nothing.
	_, err := fs.WriteFile(t.Context(), WriteRequest{Ref: PathRef{RunDir: rw, Path: "alias/docs/new/x.txt"}, Mkdir: true}, strings.NewReader("x"))
	assert.Equal(t, CodeDenied, CodeOf(err))
	assert.NoDirExists(t, filepath.Join(docs, "new"))
	// Removing the alias link itself only unlinks it and spares its target.
	require.NoError(t, fs.Remove(t.Context(), PathRef{RunDir: rw, Path: "alias"}, true, ""))
	assert.FileExists(t, filepath.Join(docs, "keep.txt"))
	// A sibling without the mount below can be removed recursively.
	require.NoError(t, os.MkdirAll(filepath.Join(rw, "plain", "d"), 0o755))
	require.NoError(t, fs.Remove(t.Context(), PathRef{RunDir: rw, Path: "plain"}, true, ""))
	assert.NoDirExists(t, filepath.Join(rw, "plain"))
}

func TestLocalWriteThenReadRoundTripsForEveryPathForm(t *testing.T) {
	fs, ws, _ := confinedFS(t)
	tests := []struct {
		name, path, scope string
		display           string
	}{
		{"explicit tmp", "/tmp/a.txt", "", "/tmp/a.txt"},
		{"explicit tmp scope tmp", "/tmp/b.txt", "tmp", "/tmp/b.txt"},
		{"relative tmp prefix, scope tmp", "tmp/c.txt", "tmp", "/tmp/c.txt"},
		{"relative tmp prefix, scope workspace", "tmp/d.txt", "workspace", "tmp/d.txt"},
		{"relative plain, scope tmp", "e.txt", "tmp", "e.txt"},
		{"explicit session", "/session/f.txt", "", "/session/f.txt"},
		{"relative session prefix, scope session", "session/g.txt", "session", "/session/g.txt"},
		{"relative plain, scope session", "h.txt", "session", "h.txt"},
		{"workspace relative", "i.txt", "workspace", "i.txt"},
		{"workspace relative, no scope", "sub/j.txt", "", "sub/j.txt"},
		{"workspace absolute", filepath.Join(ws, "k.txt"), "workspace", filepath.Join(ws, "k.txt")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "content of " + tt.name
			_, err := fs.WriteFile(t.Context(), WriteRequest{Ref: PathRef{RunDir: ws, Path: tt.path, Scope: tt.scope}, Mkdir: true}, strings.NewReader(content))
			require.NoError(t, err)
			f, err := fs.Open(t.Context(), ReadRequest{RunDir: ws, Path: tt.path, Scope: tt.scope})
			require.NoError(t, err)
			t.Cleanup(func() { _ = f.Close() })
			b, err := io.ReadAll(f)
			require.NoError(t, err)
			assert.Equal(t, content, string(b))
			st, err := fs.Stat(t.Context(), PathRef{RunDir: ws, Path: tt.path, Scope: tt.scope})
			require.NoError(t, err)
			assert.True(t, st.Exists)
			assert.EqualValues(t, len(content), st.Size)
			assert.Equal(t, tt.display, f.Path)
		})
	}
}

func TestLocalMkdir(t *testing.T) {
	fs, ws, outside := confinedFS(t)

	require.NoError(t, fs.Mkdir(t.Context(), wref(ws, "a/b/c"), "r1"))
	assert.DirExists(t, filepath.Join(ws, "a", "b", "c"))
	// Idempotent.
	require.NoError(t, fs.Mkdir(t.Context(), wref(ws, "a/b/c"), "r1"))

	// A file in the way is an error.
	require.NoError(t, os.WriteFile(filepath.Join(ws, "f"), nil, 0o644))
	assert.Error(t, fs.Mkdir(t.Context(), wref(ws, "f/sub"), ""))

	// Namespace dirs are created on demand, including the root itself.
	require.NoError(t, fs.Mkdir(t.Context(), PathRef{RunDir: ws, Path: "/tmp"}, ""))
	require.NoError(t, fs.Mkdir(t.Context(), PathRef{RunDir: ws, Path: "/session/x/y"}, ""))
	assert.DirExists(t, filepath.Join(os.Getenv("HOME"), "asgard", "data", "sessions", testChat, "x", "y"))

	// Confinement: escapes, symlinks and outside roots are refused.
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "link")))
	for _, ref := range []PathRef{
		wref(ws, "../escape"),
		wref(ws, "link/new"),
		{RunDir: outside, Path: "new", Scope: "workspace"},
		{RunDir: ws, Path: ""},
	} {
		assert.Error(t, fs.Mkdir(t.Context(), ref, ""), ref.Path)
	}
	assert.NoDirExists(t, filepath.Join(outside, "new"))
	assert.NoDirExists(t, filepath.Join(filepath.Dir(ws), "escape"))
}

func TestLocalMkdirReadonlyVeto(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rw, ro := t.TempDir(), t.TempDir()
	fs := NewConfined(testChat, func(bool) []string { return []string{rw, ro} }, func() []string { return []string{ro} })
	assert.Error(t, fs.Mkdir(t.Context(), PathRef{RunDir: ro, Path: "new", Scope: "workspace"}, ""))
	assert.NoDirExists(t, filepath.Join(ro, "new"))
	require.NoError(t, fs.Mkdir(t.Context(), PathRef{RunDir: rw, Path: "new", Scope: "workspace"}, ""))
}
