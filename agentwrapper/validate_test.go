package agentwrapper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAgySetup(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("ASGARD_ACCOUNTS_DIR", filepath.Join(tempDir, "accounts"))
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

func TestValidateAgySetup_AccountToken(t *testing.T) {
	tempDir := t.TempDir()
	accts := filepath.Join(tempDir, "accounts")
	t.Setenv("ASGARD_ACCOUNTS_DIR", accts)
	origHomeDirFn := homeDirFn
	homeDirFn = func() (string, error) { return tempDir, nil }
	t.Cleanup(func() { homeDirFn = origHomeDirFn })

	// Account dir without token does not pass.
	require.NoError(t, os.MkdirAll(filepath.Join(accts, "agy", "work"), 0o700))
	require.Error(t, ValidateAgySetup())

	require.NoError(t, os.WriteFile(filepath.Join(accts, "agy", "work", "antigravity-oauth-token"), []byte("t"), 0o600))
	require.NoError(t, ValidateAgySetup())
}

func TestValidateAgySetup_MissingToken_ErrorContainsDockerExecHint(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("ASGARD_ACCOUNTS_DIR", filepath.Join(tempDir, "accounts"))
	origHomeDirFn := homeDirFn
	homeDirFn = func() (string, error) {
		return tempDir, nil
	}
	t.Cleanup(func() {
		homeDirFn = origHomeDirFn
	})

	err := ValidateAgySetup()
	require.Error(t, err)
	expectedHint := "aw agy login <alias>"
	assert.True(t, strings.Contains(err.Error(), expectedHint), "expected error to contain %q, got %q", expectedHint, err.Error())
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

	t.Setenv("HOME", tempDir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("OPENCODE_AW_CONFIG", "")
	t.Setenv("AW_CONFIG", "")

	// 1. Missing opencode.db
	err := ValidateOpencodeSetup()
	if err == nil {
		t.Fatal("expected error when opencode.db is missing, got nil")
	}

	// Create opencode directory
	opencodeDir := filepath.Join(tempDir, ".local", "share", "opencode")
	if err := os.MkdirAll(opencodeDir, 0755); err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}

	// 2. Success with opencode.db
	dbPath := filepath.Join(opencodeDir, "opencode.db")
	if err := os.WriteFile(dbPath, []byte("test-db"), 0600); err != nil {
		t.Fatalf("failed to write db: %v", err)
	}

	err = ValidateOpencodeSetup()
	if err != nil {
		t.Fatalf("expected no error with opencode.db, got: %v", err)
	}

	// Remove db to test opencode.aw.yaml paths
	require.NoError(t, os.Remove(dbPath))

	// 3. Fallback when opencode.aw.yaml exists but only has comments/empty
	awDir := filepath.Join(tempDir, ".config", "aw")
	require.NoError(t, os.MkdirAll(awDir, 0755))
	awPath := filepath.Join(awDir, "opencode.aw.yaml")
	require.NoError(t, os.WriteFile(awPath, []byte("# empty config\n# env:\n"), 0600))
	err = ValidateOpencodeSetup()
	assert.Error(t, err, "expected error when opencode.aw.yaml only has comments and db is missing")

	// 4. Success when opencode.aw.yaml has keys
	require.NoError(t, os.WriteFile(awPath, []byte("env:\n  OPENCODE_ZAI_API_KEY: \"sk-test\"\n"), 0600))
	err = ValidateOpencodeSetup()
	assert.NoError(t, err, "expected success when opencode.aw.yaml has valid env keys")

	// 5. Success with OPENCODE_AW_CONFIG env var override
	customAWPath := filepath.Join(tempDir, "custom.aw.yaml")
	require.NoError(t, os.WriteFile(customAWPath, []byte("ZHIPU_API_KEY: \"test-zhipu\"\n"), 0600))
	t.Setenv("OPENCODE_AW_CONFIG", customAWPath)
	err = ValidateOpencodeSetup()
	assert.NoError(t, err, "expected success via OPENCODE_AW_CONFIG")

	// 6. Error when opencode.aw.yaml has invalid YAML syntax
	require.NoError(t, os.WriteFile(customAWPath, []byte("invalid: yaml: :"), 0600))
	err = ValidateOpencodeSetup()
	assert.Error(t, err, "expected error on malformed YAML")
	assert.Contains(t, err.Error(), "opencode setup validation failed")
}

func TestValidateOpencodeSetup_MissingAuth_ErrorContainsDockerExecHint(t *testing.T) {
	tempDir := t.TempDir()
	origHomeDirFn := homeDirFn
	homeDirFn = func() (string, error) {
		return tempDir, nil
	}
	t.Cleanup(func() {
		homeDirFn = origHomeDirFn
	})

	t.Setenv("HOME", tempDir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("OPENCODE_AW_CONFIG", "")
	t.Setenv("AW_CONFIG", "")

	err := ValidateOpencodeSetup()
	require.Error(t, err)
	expectedHint := "Please run 'docker exec -it <container_name> opencode auth login' to log in (find the container name via 'docker ps')."
	assert.True(t, strings.Contains(err.Error(), expectedHint), "expected error to contain %q, got %q", expectedHint, err.Error())
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

	_ = os.Unsetenv("SIMPLEST_CONFIG_PATH")
	_ = os.Unsetenv("GEMINI_API_KEY")
	_ = os.Unsetenv("OPENAI_API_KEY")

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
	_ = os.Unsetenv("GEMINI_API_KEY")

	// 3. No config file, but OPENAI_API_KEY set -> Success
	t.Setenv("OPENAI_API_KEY", "test-openai-key")
	if err := ValidateSimplestSetup(); err != nil {
		t.Fatalf("expected success with OPENAI_API_KEY, got: %v", err)
	}
	_ = os.Unsetenv("OPENAI_API_KEY")

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

	// 5. Config files exist and are valid -> Success
	keyFile := filepath.Join(cfgDir, "key.yaml")
	validKeyYAML := `
providers:
  gemini:
    apiKey: valid-key
`
	providersFile := filepath.Join(cfgDir, "providers.yaml")
	validProvidersYAML := `
providers:
  gemini:
    api: gemini
`
	modelsFile := filepath.Join(cfgDir, "models.yaml")
	validModelsYAML := `
models:
  - id: gemini-3.7-flash
    provider: gemini
`
	_ = os.Remove(cfgFile)
	if err := os.WriteFile(keyFile, []byte(validKeyYAML), 0600); err != nil {
		t.Fatalf("failed to write valid key: %v", err)
	}
	if err := os.WriteFile(providersFile, []byte(validProvidersYAML), 0600); err != nil {
		t.Fatalf("failed to write valid providers: %v", err)
	}
	if err := os.WriteFile(modelsFile, []byte(validModelsYAML), 0600); err != nil {
		t.Fatalf("failed to write valid models: %v", err)
	}
	if err := ValidateSimplestSetup(); err != nil {
		t.Fatalf("expected success with valid key YAML, got: %v", err)
	}

	// 6. Explicit SIMPLEST_KEY_PATH takes precedence
	customKey := filepath.Join(tempDir, "custom_key.yaml")
	if err := os.WriteFile(customKey, []byte("invalid: yaml: ["), 0600); err != nil {
		t.Fatalf("failed to write custom key: %v", err)
	}
	t.Setenv("SIMPLEST_KEY_PATH", customKey)
	if err := ValidateSimplestSetup(); err == nil {
		t.Fatal("expected error with corrupt custom key, got nil")
	}
}
