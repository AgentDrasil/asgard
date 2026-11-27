package bwrap

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveBindDests(t *testing.T) {
	// EvalSymlinks may canonicalize the temp dir itself.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dash := write("dash")
	bash := write("bash")
	sh := filepath.Join(dir, "sh")
	if err := os.Symlink("dash", sh); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")

	t.Run("symlink resolved", func(t *testing.T) {
		if got, want := resolveBindDests(sh), []string{dash}; !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	t.Run("regular files unchanged", func(t *testing.T) {
		if got, want := resolveBindDests(bash, dash), []string{bash, dash}; !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	t.Run("dedupe", func(t *testing.T) {
		alias := filepath.Join(dir, "alias")
		if err := os.Symlink("dash", alias); err != nil {
			t.Fatal(err)
		}
		if got, want := resolveBindDests(sh, alias, dash), []string{dash}; !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	t.Run("nonexistent kept", func(t *testing.T) {
		if got, want := resolveBindDests(missing, missing), []string{missing}; !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
}
