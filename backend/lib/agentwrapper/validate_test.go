package agentwrapper

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAgySetup(t *testing.T) {
	tempDir := t.TempDir()
	origHomeDirFn := homeDirFn
	homeDirFn = func() (string, error) {
		return tempDir, nil
	}
	t.Cleanup(func() {
		homeDirFn = origHomeDirFn
	})

	// 1. Missing token
	err := ValidateAgySetup()
	if err == nil {
		t.Fatal("expected error when token is missing, got nil")
	}

	// Create gemini cli directory
	cliDir := filepath.Join(tempDir, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(cliDir, 0755); err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}

	// Create token
	tokenPath := filepath.Join(cliDir, "antigravity-oauth-token")
	if err := os.WriteFile(tokenPath, []byte("test-token"), 0600); err != nil {
		t.Fatalf("failed to write token: %v", err)
	}

	// 2. Success
	err = ValidateAgySetup()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateOpencodeSetup(t *testing.T) {
	tempDir := t.TempDir()
	origHomeDirFn := homeDirFn
	homeDirFn = func() (string, error) {
		return tempDir, nil
	}
	t.Cleanup(func() {
		homeDirFn = origHomeDirFn
	})

	// 1. Missing auth.json
	err := ValidateOpencodeSetup()
	if err == nil {
		t.Fatal("expected error when auth.json is missing, got nil")
	}

	// Create opencode directory
	opencodeDir := filepath.Join(tempDir, ".local", "share", "opencode")
	if err := os.MkdirAll(opencodeDir, 0755); err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}

	// Create auth.json
	authPath := filepath.Join(opencodeDir, "auth.json")
	if err := os.WriteFile(authPath, []byte("test-auth"), 0600); err != nil {
		t.Fatalf("failed to write auth: %v", err)
	}

	// 2. Success
	err = ValidateOpencodeSetup()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}
