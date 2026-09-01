package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// chatIDRegex enforces alphanumeric characters, hyphens, and underscores up to 64 characters long.
var chatIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// IsValidChatID checks whether chatID is non-empty, matches regex rules, and is within maximum length (64 chars).
func IsValidChatID(chatID string) bool {
	return chatIDRegex.MatchString(chatID)
}

// GetSessionTmpBaseDir returns the host temporary directory for the session without creating it.
func GetSessionTmpBaseDir(chatID string) string {
	if chatID == "" {
		chatID = "default"
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return filepath.Join(home, "tmp", chatID)
	}
	return filepath.Join(os.TempDir(), chatID)
}

// NormalizeSessionRunDir normalizes the given run directory for a session.
// If runDir is empty, ".", "/tmp", "/tmp/session-id", "/tmp/${session_id}", or "/tmp/<chatID>",
// it resolves to the session temporary directory ($HOME/tmp/<chatID> or /tmp/<chatID>).
// Subpaths under /tmp/session-id or /tmp/<chatID> (e.g. /tmp/session-id/subdir) are resolved relative to the session temp dir.
// Otherwise, it returns the cleaned absolute path.
func NormalizeSessionRunDir(runDir string, chatID string) string {
	if runDir == "" {
		if chatID == "" {
			return ""
		}
		return GetSessionTmpBaseDir(chatID)
	}

	clean := filepath.Clean(runDir)
	if chatID == "" {
		if clean == "/tmp" || clean == "." {
			return runDir
		}
		return clean
	}

	isTmp, sub := isSessionTmpPath(clean, chatID)
	if isTmp {
		baseTmp := GetSessionTmpBaseDir(chatID)
		sub = filepath.Clean(sub)
		if sub == "" || sub == "." {
			return baseTmp
		}
		return filepath.Join(baseTmp, sub)
	}

	return clean
}

func isSessionTmpPath(clean string, chatID string) (bool, string) {
	if clean == "" || clean == "." {
		return true, ""
	}
	return isExplicitSessionTmpPath(clean, chatID)
}

func isExplicitSessionTmpPath(clean string, chatID string) (bool, string) {
	if clean == "/tmp" || clean == ".tmp" {
		return true, ""
	}
	if strings.HasPrefix(clean, ".tmp/") {
		sub := strings.TrimPrefix(clean, ".tmp/")
		return true, stripSessionIDPrefix(sub, chatID)
	}
	if clean == "/tmp/session-id" || clean == "/tmp/${session_id}" {
		return true, ""
	}
	if strings.HasPrefix(clean, "/tmp/session-id/") {
		return true, strings.TrimPrefix(clean, "/tmp/session-id/")
	}
	if strings.HasPrefix(clean, "/tmp/${session_id}/") {
		return true, strings.TrimPrefix(clean, "/tmp/${session_id}/")
	}
	if chatID != "" {
		if clean == "/tmp/"+chatID {
			return true, ""
		}
		if strings.HasPrefix(clean, "/tmp/"+chatID+"/") {
			return true, strings.TrimPrefix(clean, "/tmp/"+chatID+"/")
		}
	}
	return false, ""
}

// ResolveSessionTmpPath checks if reqPath explicitly targets the session's tmp namespace (/tmp, .tmp, /tmp/session-id, etc.)
// and extracts the normalized subpath inside the session temporary directory.
func ResolveSessionTmpPath(reqPath, sessionID string) (bool, string) {
	clean := filepath.Clean(reqPath)
	if isTmp, sub := isExplicitSessionTmpPath(clean, sessionID); isTmp {
		sub = filepath.Clean(sub)
		if sub == "." {
			sub = ""
		}
		return true, sub
	}

	if strings.HasPrefix(clean, "/tmp/") {
		sub := strings.TrimPrefix(clean, "/tmp/")
		sub = stripSessionIDPrefix(sub, sessionID)
		sub = filepath.Clean(sub)
		if sub == "." {
			sub = ""
		}
		return true, sub
	}

	return false, ""
}

// NormalizeTmpPathForAuth returns canonical /tmp/... path representation for authorization checks.
func NormalizeTmpPathForAuth(reqPath, sessionID string) (bool, string) {
	if isTmp, sub := ResolveSessionTmpPath(reqPath, sessionID); isTmp {
		if sub == "" {
			return true, "/tmp"
		}
		return true, "/tmp/" + sub
	}
	return false, ""
}

func stripSessionIDPrefix(sub string, chatID string) string {
	if sub == chatID || sub == "session-id" || sub == "${session_id}" {
		return ""
	}
	if chatID != "" && strings.HasPrefix(sub, chatID+"/") {
		return strings.TrimPrefix(sub, chatID+"/")
	}
	if strings.HasPrefix(sub, "session-id/") {
		return strings.TrimPrefix(sub, "session-id/")
	}
	if strings.HasPrefix(sub, "${session_id}/") {
		return strings.TrimPrefix(sub, "${session_id}/")
	}
	return sub
}
