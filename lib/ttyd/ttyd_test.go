package ttyd

import (
	"os"
	"testing"
)

func TestManagerLifecycle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ttyd-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	mgr, err := NewManager(tempDir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer mgr.Close()

	// Ensure socket directory exists
	if _, err := os.Stat(tempDir); os.IsNotExist(err) {
		t.Errorf("socket directory was not created")
	}

	// Clean up after test
	_ = mgr
}
