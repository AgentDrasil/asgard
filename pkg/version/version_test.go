package version

import "testing"

func TestDefault(t *testing.T) {
	if Version != "dev" {
		t.Fatalf("Version = %q, want %q", Version, "dev")
	}
	if Get() != "dev" {
		t.Fatalf("Get() = %q, want %q", Get(), "dev")
	}
}
