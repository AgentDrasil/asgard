package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocPathPolicyResolveBasics(t *testing.T) {
	dir := t.TempDir()
	policy := DocPathPolicy{}

	// Relative .md resolves against cwd; non-.md rejected before resolution.
	resolved, err := policy.Resolve("docs/a.md", dir)
	if err != nil {
		t.Fatalf("relative .md should resolve, got: %v", err)
	}
	if want := filepath.Join(dir, "docs", "a.md"); resolved != want {
		t.Errorf("resolved = %q, want %q", resolved, want)
	}
	for _, p := range []string{"a.go", "a.md.txt", "a.MD.md/x"} {
		if _, err := policy.Resolve(p, dir); err == nil || !strings.Contains(err.Error(), "only markdown files ending in .md") {
			t.Errorf("path %q must be rejected as non-markdown, got: %v", p, err)
		}
	}
}

func TestDocPathPolicyDotDotEscape(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	policy := DocPathPolicy{AllowedDirs: []string{allowed}}

	for _, p := range []string{
		"../escape.md",
		"allowed/../../escape.md",
	} {
		_, err := policy.Resolve(p, allowed)
		if err == nil || !strings.Contains(err.Error(), "outside the allowed directories") {
			t.Errorf("path %q must escape and be rejected, got: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "escape.md")); err == nil {
		t.Error("nothing should have been written by Resolve")
	}
}

func TestDocPathPolicyPrefixNotSubstring(t *testing.T) {
	dir := t.TempDir()
	// /session-evil must not match an allowed /session prefix.
	policy := DocPathPolicy{AllowedDirs: []string{filepath.Join(dir, "session")}}

	if _, err := policy.Resolve("session/intend.md", dir); err != nil {
		t.Errorf("path under allowed dir must pass, got: %v", err)
	}
	_, err := policy.Resolve("session-evil/intend.md", dir)
	if err == nil || !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("sibling directory sharing the prefix must be rejected, got: %v", err)
	}
}

func TestDocPathPolicyAllowedDirSymlink(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "data", "chat")
	linkDir := filepath.Join(root, "link")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}

	// The configured allowed dir is itself a symlink: files reached through
	// the real path must still match (no false rejection).
	policy := DocPathPolicy{AllowedDirs: []string{linkDir}}
	if _, err := policy.Resolve(filepath.Join(realDir, "a.md"), root); err != nil {
		t.Errorf("real path under symlinked allowed dir must pass, got: %v", err)
	}
	if _, err := policy.Resolve(filepath.Join(linkDir, "b.md"), root); err != nil {
		t.Errorf("symlink path must pass, got: %v", err)
	}

	// Other directories still rejected.
	_, err := policy.Resolve(filepath.Join(root, "elsewhere", "c.md"), root)
	if err == nil || !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("unrelated directory must be rejected, got: %v", err)
	}
}

func TestDocPathPolicyAllowedDirMissingFallsBackToClean(t *testing.T) {
	root := t.TempDir()
	future := filepath.Join(root, "not-yet", "created")
	policy := DocPathPolicy{AllowedDirs: []string{future}}

	if _, err := policy.Resolve("not-yet/created/new.md", root); err != nil {
		t.Errorf("new file under not-yet-existing allowed dir must pass, got: %v", err)
	}
	_, err := policy.Resolve("elsewhere/new.md", root)
	if err == nil || !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("path outside missing dir must be rejected, got: %v", err)
	}
}

func TestDocPathPolicyDanglingSymlinkFinalComponent(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	// notes.md points at a target that does not exist yet: the policy must
	// reject it instead of treating it as a new file inside the allowed dir.
	if err := os.Symlink(filepath.Join(outside, "x.md"), filepath.Join(allowed, "notes.md")); err != nil {
		t.Fatal(err)
	}

	policy := DocPathPolicy{AllowedDirs: []string{allowed}}
	_, err := policy.Resolve("allowed/notes.md", root)
	if err == nil || !strings.Contains(err.Error(), "dangling symlink") {
		t.Fatalf("dangling final symlink must be rejected, got: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "x.md")); !os.IsNotExist(statErr) {
		t.Error("target must not have been created")
	}
}

func TestDocPathPolicyDanglingSymlinkParent(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	// linkdir points at a missing directory.
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(allowed, "linkdir")); err != nil {
		t.Fatal(err)
	}

	policy := DocPathPolicy{AllowedDirs: []string{allowed}}
	_, err := policy.Resolve("allowed/linkdir/new.md", root)
	if err == nil || !strings.Contains(err.Error(), "dangling symlink") {
		t.Fatalf("dangling parent symlink must be rejected, got: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "missing")); !os.IsNotExist(statErr) {
		t.Error("dangling target must not have been created")
	}
}

func TestDocPathPolicyAllowedDirIgnoresEmptyAndRelativeEntries(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}

	// Empty and relative entries must be skipped, not interpreted as the
	// process working directory (".").
	policy := DocPathPolicy{AllowedDirs: []string{"", "   ", allowed}}
	if _, err := policy.Resolve("allowed/a.md", root); err != nil {
		t.Errorf("valid entry must still work, got: %v", err)
	}
	if _, err := policy.Resolve("other/a.md", root); err == nil {
		t.Error("path outside the valid entry must be rejected")
	}

	// Only empty/relative entries: nothing matches, everything is rejected.
	onlyBad := DocPathPolicy{AllowedDirs: []string{"", "docs"}}
	if _, err := onlyBad.Resolve("a.md", root); err == nil || !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("empty/relative-only config must reject everything, got: %v", err)
	}
}
