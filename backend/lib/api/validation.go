package api

import (
	"os"
	"path/filepath"
	"regexp"
)

// chatIDRegex enforces alphanumeric characters, hyphens, and underscores up to 64 characters long.
var chatIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// IsValidChatID checks whether chatID is non-empty, matches regex rules, and is within maximum length (64 chars).
func IsValidChatID(chatID string) bool {
	return chatIDRegex.MatchString(chatID)
}

// NormalizeSessionRunDir normalizes the given run directory for a session.
// If runDir is empty or "/tmp", it resolves to $HOME/tmp/<chatID> (and ensures the directory exists).
// Otherwise, it returns the cleaned absolute path.
func NormalizeSessionRunDir(runDir string, chatID string) string {
	clean := filepath.Clean(runDir)
	if clean == "" || clean == "/tmp" || clean == "." {
		if chatID == "" {
			return runDir
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return runDir
		}
		sessionTmpDir := filepath.Join(home, "tmp", chatID)
		_ = os.MkdirAll(sessionTmpDir, 0755)
		return sessionTmpDir
	}
	return clean
}
