package agentwrapper

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

var homeDirFn = os.UserHomeDir

// ValidateAgySetup verifies that agy is correctly set up on the user's system.
// It checks that ~/.gemini/antigravity-cli/antigravity-oauth-token exists,
// and that ~/.gemini/antigravity-cli/settings.json exists and has statusLine enabled.
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

	settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("agy setup validation failed: settings file does not exist at %s", settingsPath)
		}
		return fmt.Errorf("failed to read settings file: %w", err)
	}

	var config struct {
		StatusLine *struct {
			Enabled *bool `json:"enabled"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal(settingsData, &config); err != nil {
		return fmt.Errorf("failed to parse settings file: %w", err)
	}

	if config.StatusLine == nil || config.StatusLine.Enabled == nil || !*config.StatusLine.Enabled {
		return fmt.Errorf("agy setup validation failed: settings.json at %s does not have statusLine enabled", settingsPath)
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
