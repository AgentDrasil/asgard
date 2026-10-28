package version

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLdflagsStamp(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	const want = "2026-01-02-03-04-05:deadbeef"
	bin := filepath.Join(t.TempDir(), "printver")
	build := exec.Command(goBin, "build",
		"-ldflags", "-X github.com/AgentDrasil/asgard/pkg/version.Version="+want,
		"-o", bin, "./testdata/printver")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if string(out) != want {
		t.Fatalf("stamped version = %q, want %q", out, want)
	}
}
