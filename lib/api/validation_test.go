package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValidChatID(t *testing.T) {
	validIDs := []string{
		"b98684d9-1343-41f9-82f1-38c7f15608cc",
		"chat-123",
		"session_456",
		"a1b2c3d4",
	}

	invalidIDs := []string{
		"",
		"../etc/passwd",
		"chat;DROP TABLE sessions;--",
		"chat ID with spaces",
		"<script>alert(1)</script>",
		"this-is-a-very-long-chat-id-that-exceeds-the-maximum-allowed-length-of-64-characters-which-is-invalid",
	}

	for _, id := range validIDs {
		assert.True(t, IsValidChatID(id), "expected valid: %s", id)
	}

	for _, id := range invalidIDs {
		assert.False(t, IsValidChatID(id), "expected invalid: %s", id)
	}
}
