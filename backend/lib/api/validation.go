package api

import "regexp"

// chatIDRegex enforces alphanumeric characters, hyphens, and underscores up to 64 characters long.
var chatIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// IsValidChatID checks whether chatID is non-empty, matches regex rules, and is within maximum length (64 chars).
func IsValidChatID(chatID string) bool {
	return chatIDRegex.MatchString(chatID)
}
