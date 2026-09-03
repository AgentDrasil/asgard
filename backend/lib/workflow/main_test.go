package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates HOME for the whole package so engine defaults that derive
// per-session directories from the user home (DefaultTmpDir → ~/tmp/<id>,
// DefaultSessionDir → ~/data/<id>, see bwrap.setupTmpDir/setupSessionDir) land
// in a throwaway directory instead of polluting the developer's home. Go tool
// dirs (GOPATH/GOCACHE/GOMODCACHE) are pinned to their pre-override values so
// builds and module caches are neither relocated nor deleted on cleanup.
// Tests that need their own sandboxed home still override HOME via t.Setenv.
func TestMain(m *testing.M) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		os.Exit(m.Run())
	}

	pinGoEnv := func(key, def string) {
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, def)
		}
	}
	pinGoEnv("GOPATH", filepath.Join(home, "go"))
	pinGoEnv("GOCACHE", filepath.Join(home, ".cache", "go-build"))
	pinGoEnv("GOMODCACHE", filepath.Join(home, "go", "pkg", "mod"))

	tmpHome, err := os.MkdirTemp("", "asgard-workflow-test-home-")
	if err != nil {
		os.Exit(m.Run())
	}
	_ = os.Setenv("HOME", tmpHome)

	code := m.Run()
	_ = os.RemoveAll(tmpHome)
	os.Exit(code)
}
