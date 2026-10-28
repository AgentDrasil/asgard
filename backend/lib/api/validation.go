package api

import "github.com/AgentDrasil/asgard/backend/lib/agents"

// The session path helpers live in package agents so the runner shares one
// implementation; these wrappers keep the api package's names.

// IsValidChatID checks whether chatID is non-empty, matches regex rules, and is within maximum length (64 chars).
func IsValidChatID(chatID string) bool { return agents.IsValidChatID(chatID) }

// GetSessionTmpBaseDir returns the host temporary directory for the session without creating it.
func GetSessionTmpBaseDir(chatID string) string { return agents.GetSessionTmpBaseDir(chatID) }

// GetSessionScopedBaseDir returns the host base directory for the given session namespace.
func GetSessionScopedBaseDir(ns string, chatID string) string {
	return agents.GetSessionScopedBaseDir(ns, chatID)
}

// NormalizeSessionRunDir normalizes the given run directory for a session.
func NormalizeSessionRunDir(runDir string, chatID string) string {
	return agents.NormalizeSessionRunDir(runDir, chatID)
}

func isRelativeTmpPrefixedPath(clean string, chatID string) (bool, string) {
	return agents.RelativeTmpPrefixedPath(clean, chatID)
}

func isRelativeScopedPrefixedPath(clean string, chatID string, ns string) (bool, string) {
	return agents.RelativeScopedPrefixedPath(clean, chatID, ns)
}

// ResolveSessionTmpPath checks if reqPath explicitly targets the session's tmp namespace.
func ResolveSessionTmpPath(reqPath, sessionID string) (bool, string) {
	return agents.ResolveSessionTmpPath(reqPath, sessionID)
}

// ResolveSessionScopedPath checks if reqPath explicitly targets the session's namespace.
func ResolveSessionScopedPath(reqPath, sessionID string, ns string) (bool, string) {
	return agents.ResolveSessionScopedPath(reqPath, sessionID, ns)
}

// NormalizeTmpPathForAuth returns canonical /tmp/... path representation for authorization checks.
func NormalizeTmpPathForAuth(reqPath, sessionID string) (bool, string) {
	return agents.NormalizeTmpPathForAuth(reqPath, sessionID)
}

// NormalizeScopedPathForAuth returns canonical /<ns>/... path representation for authorization checks.
func NormalizeScopedPathForAuth(reqPath, sessionID string, ns string) (bool, string) {
	return agents.NormalizeScopedPathForAuth(reqPath, sessionID, ns)
}
