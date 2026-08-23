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

func TestValidateSimplestSetup(t *testing.T) {
	tempDir := t.TempDir()
	origHomeDirFn := homeDirFn
	homeDirFn = func() (string, error) {
		return tempDir, nil
	}
	t.Cleanup(func() {
		homeDirFn = origHomeDirFn
	})

	t.Setenv("SIMPLEST_CONFIG_PATH", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	// 1. Completely missing configuration and API keys -> Error
	err := ValidateSimplestSetup()
	if err == nil {
		t.Fatal("expected error when no config or API key is set, got nil")
	}

	// 2. No config file, but GEMINI_API_KEY set -> Success
	t.Setenv("GEMINI_API_KEY", "test-gemini-key")
	if err := ValidateSimplestSetup(); err != nil {
		t.Fatalf("expected success with GEMINI_API_KEY, got: %v", err)
	}
	t.Setenv("GEMINI_API_KEY", "")

	// 3. No config file, but OPENAI_API_KEY set -> Success
	t.Setenv("OPENAI_API_KEY", "test-openai-key")
	if err := ValidateSimplestSetup(); err != nil {
		t.Fatalf("expected success with OPENAI_API_KEY, got: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "")

	// 4. Config file exists at ~/.config/simplest/config.yaml but is corrupt YAML -> Error
	cfgDir := filepath.Join(tempDir, ".config", "simplest")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	cfgFile := filepath.Join(cfgDir, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte("invalid: yaml: ["), 0600); err != nil {
		t.Fatalf("failed to write corrupt config: %v", err)
	}
	if err := ValidateSimplestSetup(); err == nil {
		t.Fatal("expected error with corrupted config YAML, got nil")
	}

	// 5. Config file exists and is valid -> Success
	validYAML := `
providers:
  google:
    api: google-generative-ai
    apiKey: valid-key
models:
  - id: gemini-3.7-flash
    provider: google
`
	if err := os.WriteFile(cfgFile, []byte(validYAML), 0600); err != nil {
		t.Fatalf("failed to write valid config: %v", err)
	}
	if err := ValidateSimplestSetup(); err != nil {
		t.Fatalf("expected success with valid config YAML, got: %v", err)
	}

	// 6. Explicit SIMPLEST_CONFIG_PATH takes precedence
	customCfg := filepath.Join(tempDir, "custom.yaml")
	if err := os.WriteFile(customCfg, []byte("invalid: yaml: ["), 0600); err != nil {
		t.Fatalf("failed to write custom config: %v", err)
	}
	t.Setenv("SIMPLEST_CONFIG_PATH", customCfg)
	if err := ValidateSimplestSetup(); err == nil {
		t.Fatal("expected error with corrupt custom config, got nil")
	}
}
