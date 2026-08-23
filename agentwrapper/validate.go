package agentwrapper

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/AgentDrasil/asgard/simplest"
)

var homeDirFn = os.UserHomeDir

// ValidateAgySetup verifies that agy is correctly set up on the user's system.
// It checks that ~/.gemini/antigravity-cli/antigravity-oauth-token exists.
func ValidateAgySetup() error {
	home, err := homeDirFn()
	if err != nil {
		return fmt.Errorf("failed to get user home directory: %w", err)
	}

	tokenPath := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if fi, err := os.Stat(tokenPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("agy setup validation failed: oauth token file does not exist at %s", tokenPath)
		}
		return fmt.Errorf("failed to check oauth token: %w", err)
	} else if fi.IsDir() {
		return fmt.Errorf("agy setup validation failed: oauth token path %s is a directory", tokenPath)
	}

	return nil
}

// ValidateOpencodeSetup verifies that opencode is correctly set up on the user's system.
// It checks that ~/.local/share/opencode/auth.json exists.
func ValidateOpencodeSetup() error {
	home, err := homeDirFn()
	if err != nil {
		return fmt.Errorf("failed to get user home directory: %w", err)
	}

	authPath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	if fi, err := os.Stat(authPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("opencode setup validation failed: auth file does not exist at %s", authPath)
		}
		return fmt.Errorf("failed to check auth file: %w", err)
	} else if fi.IsDir() {
		return fmt.Errorf("opencode setup validation failed: auth path %s is a directory", authPath)
	}

	return nil
}

// ValidateSimplestSetup verifies that simplest is correctly set up on the user's system.
// It checks $SIMPLEST_CONFIG_PATH, ~/.config/simplest/config.yaml or ~/.simplest/config.yaml.
// If a config file is present, it validates that it can be parsed and loaded.
// If no config file is present, it checks for fallback environment variables (GEMINI_API_KEY / OPENAI_API_KEY).
func ValidateSimplestSetup() error {
	var candidates []string
	if envPath := os.Getenv("SIMPLEST_CONFIG_PATH"); envPath != "" {
		candidates = append(candidates, envPath)
	}

	home, err := homeDirFn()
	if err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".config", "simplest", "config.yaml"),
			filepath.Join(home, ".config", "simplest", "config.yml"),
			filepath.Join(home, ".simplest", "config.yaml"),
			filepath.Join(home, ".simplest", "config.yml"),
		)
	}

	var foundPath string
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			foundPath = p
			break
		}
	}

	if foundPath != "" {
		if _, err := simplest.LoadConfigFrom(foundPath); err != nil {
			return fmt.Errorf("simplest setup validation failed: error parsing config file at %s: %w", foundPath, err)
		}
		return nil
	}

	if os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("OPENAI_API_KEY") != "" {
		return nil
	}

	return fmt.Errorf("simplest setup validation failed: no valid configuration file found and neither GEMINI_API_KEY nor OPENAI_API_KEY is set")
}
