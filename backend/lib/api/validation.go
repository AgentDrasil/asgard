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
	return GetSessionScopedBaseDir("tmp", chatID)
}

// GetSessionScopedBaseDir returns the host base directory for the given session
// namespace ("tmp" or "session") without creating it (e.g. $HOME/tmp/<chatID>
// or $HOME/data/<chatID>; the "session" namespace is backed by the ~/data host
// directory that the sandbox binds as /session, see bwrap.setupSessionDir).
func GetSessionScopedBaseDir(ns string, chatID string) string {
	if chatID == "" {
		chatID = "default"
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return filepath.Join(home, scopedNsHostDir(ns), chatID)
	}
	return filepath.Join(os.TempDir(), chatID)
}

// scopedNsHostDir maps a sandbox namespace to its host directory name under
// $HOME: "tmp" → tmp, "session" → data.
func scopedNsHostDir(ns string) string {
	if ns == "session" {
		return "data"
	}
	return ns
}

// NormalizeSessionRunDir normalizes the given run directory for a session.
// If runDir is empty, ".", "/tmp", "/tmp/session-id", "/tmp/${session_id}", "/tmp/<chatID>",
// "tmp", "tmp/session-id", "tmp/${session_id}", or "tmp/<chatID>",
// it resolves to the session temporary directory ($HOME/tmp/<chatID> or /tmp/<chatID>).
// Analogous "session" forms (e.g. "/session", "session", "session/<chatID>/sub") resolve to
// the session namespace directory ($HOME/session/<chatID>).
// Subpaths under /tmp/session-id, /tmp/<chatID>, tmp/session-id, or tmp/<chatID> (e.g. /tmp/session-id/subdir)
// are resolved relative to the session temp dir.
// Otherwise, it returns the cleaned absolute path or cleaned relative path.
func NormalizeSessionRunDir(runDir string, chatID string) string {
	if runDir == "" {
		if chatID == "" {
			return ""
		}
		return GetSessionTmpBaseDir(chatID)
	}

	clean := filepath.Clean(runDir)
	if chatID == "" {
		if clean == "/tmp" || clean == "." || clean == "tmp" {
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

	if isSess, sub := isExplicitScopedPath(clean, chatID, "session"); isSess {
		return joinScopedBase("session", chatID, sub)
	}
	if isSess, sub := isRelativeScopedNarrowRunDir(clean, chatID, "session"); isSess {
		return joinScopedBase("session", chatID, sub)
	}

	return clean
}

func joinScopedBase(ns string, chatID string, sub string) string {
	base := GetSessionScopedBaseDir(ns, chatID)
	sub = filepath.Clean(sub)
	if sub == "" || sub == "." {
		return base
	}
	return filepath.Join(base, sub)
}

func isSessionTmpPath(clean string, chatID string) (bool, string) {
	return isSessionScopedPath(clean, chatID, "tmp")
}

func isSessionScopedPath(clean string, chatID string, ns string) (bool, string) {
	if clean == "" || clean == "." {
		return true, ""
	}
	if isExplicit, sub := isExplicitScopedPath(clean, chatID, ns); isExplicit {
		return true, sub
	}
	return isRelativeScopedNarrowRunDir(clean, chatID, ns)
}

// isRelativeScopedNarrowRunDir checks if clean matches relative narrow forms for runDir
// (<ns>, <ns>/session-id, <ns>/${session_id}, <ns>/<chatID> and their subpaths).
func isRelativeScopedNarrowRunDir(clean string, chatID string, ns string) (bool, string) {
	if clean == ns {
		return true, ""
	}
	if clean == ns+"/session-id" || clean == ns+"/${session_id}" {
		return true, ""
	}
	if strings.HasPrefix(clean, ns+"/session-id/") {
		return true, strings.TrimPrefix(clean, ns+"/session-id/")
	}
	if strings.HasPrefix(clean, ns+"/${session_id}/") {
		return true, strings.TrimPrefix(clean, ns+"/${session_id}/")
	}
	if chatID != "" {
		if clean == ns+"/"+chatID {
			return true, ""
		}
		if strings.HasPrefix(clean, ns+"/"+chatID+"/") {
			return true, strings.TrimPrefix(clean, ns+"/"+chatID+"/")
		}
	}
	return false, ""
}

// isRelativeTmpPrefixedPath checks if clean matches relative "tmp" or "tmp/..." prefix,
// stripping session-id, ${session_id}, or chatID placeholders, and extracting relative subpath.
func isRelativeTmpPrefixedPath(clean string, chatID string) (bool, string) {
	return isRelativeScopedPrefixedPath(clean, chatID, "tmp")
}

// isRelativeScopedPrefixedPath checks if clean matches relative "<ns>" or "<ns>/..." prefix,
// stripping session-id, ${session_id}, or chatID placeholders, and extracting relative subpath.
func isRelativeScopedPrefixedPath(clean string, chatID string, ns string) (bool, string) {
	if isNarrow, sub := isRelativeScopedNarrowRunDir(clean, chatID, ns); isNarrow {
		return true, sub
	}
	if strings.HasPrefix(clean, ns+"/") {
		sub := strings.TrimPrefix(clean, ns+"/")
		stripped := stripSessionIDPrefix(sub, chatID)
		return true, stripped
	}
	return false, ""
}

func isExplicitScopedPath(clean string, chatID string, ns string) (bool, string) {
	if clean == "/"+ns || clean == "."+ns {
		return true, ""
	}
	if strings.HasPrefix(clean, "."+ns+"/") {
		sub := strings.TrimPrefix(clean, "."+ns+"/")
		return true, stripSessionIDPrefix(sub, chatID)
	}
	if clean == "/"+ns+"/session-id" || clean == "/"+ns+"/${session_id}" {
		return true, ""
	}
	if strings.HasPrefix(clean, "/"+ns+"/session-id/") {
		return true, strings.TrimPrefix(clean, "/"+ns+"/session-id/")
	}
	if strings.HasPrefix(clean, "/"+ns+"/${session_id}/") {
		return true, strings.TrimPrefix(clean, "/"+ns+"/${session_id}/")
	}
	if chatID != "" {
		if clean == "/"+ns+"/"+chatID {
			return true, ""
		}
		if strings.HasPrefix(clean, "/"+ns+"/"+chatID+"/") {
			return true, strings.TrimPrefix(clean, "/"+ns+"/"+chatID+"/")
		}
	}
	return false, ""
}

// ResolveSessionTmpPath checks if reqPath explicitly targets the session's tmp namespace (/tmp, .tmp, /tmp/session-id, etc.)
// and extracts the normalized subpath inside the session temporary directory.
func ResolveSessionTmpPath(reqPath, sessionID string) (bool, string) {
	return ResolveSessionScopedPath(reqPath, sessionID, "tmp")
}

// ResolveSessionScopedPath checks if reqPath explicitly targets the session's namespace
// (/<ns>, .<ns>, /<ns>/session-id, etc.) and extracts the normalized subpath inside it.
func ResolveSessionScopedPath(reqPath, sessionID string, ns string) (bool, string) {
	clean := filepath.Clean(reqPath)
	if isTmp, sub := isExplicitScopedPath(clean, sessionID, ns); isTmp {
		sub = filepath.Clean(sub)
		if sub == "." {
			sub = ""
		}
		return true, sub
	}

	if strings.HasPrefix(clean, "/"+ns+"/") {
		sub := strings.TrimPrefix(clean, "/"+ns+"/")
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
	return NormalizeScopedPathForAuth(reqPath, sessionID, "tmp")
}

// NormalizeScopedPathForAuth returns canonical /<ns>/... path representation for authorization checks.
func NormalizeScopedPathForAuth(reqPath, sessionID string, ns string) (bool, string) {
	if isTmp, sub := ResolveSessionScopedPath(reqPath, sessionID, ns); isTmp {
		if sub == "" {
			return true, "/" + ns
		}
		return true, "/" + ns + "/" + sub
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
