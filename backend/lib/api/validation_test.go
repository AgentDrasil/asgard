package api

import (
	"path/filepath"
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

func TestNormalizeSessionRunDir(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	chatID := "session-123"
	expectedSessionTmp := filepath.Join(tempHome, "tmp", chatID)

	// /tmp -> ~/tmp/<chatID>
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir("/tmp", chatID))
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir("/tmp/", chatID))

	// /tmp/session-id, /tmp/${session_id}, /tmp/<chatID> -> ~/tmp/<chatID>
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir("/tmp/session-id", chatID))
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir("/tmp/${session_id}", chatID))
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir("/tmp/"+chatID, chatID))
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir(".tmp/session-id", chatID))

	// Subpaths under /tmp/session-id -> ~/tmp/<chatID>/subdir
	assert.Equal(t, filepath.Join(expectedSessionTmp, "sub"), NormalizeSessionRunDir("/tmp/session-id/sub", chatID))
	assert.Equal(t, filepath.Join(expectedSessionTmp, "sub"), NormalizeSessionRunDir("/tmp/"+chatID+"/sub", chatID))
	assert.Equal(t, filepath.Join(expectedSessionTmp, "sub"), NormalizeSessionRunDir(".tmp/session-id/sub", chatID))
	assert.Equal(t, "/tmp/sub", NormalizeSessionRunDir("/tmp/sub", chatID))

	// Empty string with chatID -> ~/tmp/<chatID>
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir("", chatID))
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir(".", chatID))

	// Empty string without chatID -> empty string
	assert.Equal(t, "", NormalizeSessionRunDir("", ""))
	assert.Equal(t, "/tmp", NormalizeSessionRunDir("/tmp", ""))

	// Explicit custom path -> untouched clean path
	customPath := filepath.Join(tempHome, "src", "my-project")
	assert.Equal(t, customPath, NormalizeSessionRunDir(customPath, chatID))

	// Pure function check: directory should not have been created by NormalizeSessionRunDir
	assert.NoDirExists(t, expectedSessionTmp)
}

func TestResolveSessionTmpPath(t *testing.T) {
	chatID := "session-123"

	tests := []struct {
		input      string
		expectTmp  bool
		expectSub  string
		expectAuth string
	}{
		{"/tmp", true, "", "/tmp"},
		{".tmp", true, "", "/tmp"},
		{"/tmp/session-id", true, "", "/tmp"},
		{"/tmp/${session_id}", true, "", "/tmp"},
		{"/tmp/session-123", true, "", "/tmp"},
		{".tmp/session-id", true, "", "/tmp"},
		{"/tmp/session-id/plan.md", true, "plan.md", "/tmp/plan.md"},
		{"/tmp/plan.md", true, "plan.md", "/tmp/plan.md"},
		{".tmp/plan.md", true, "plan.md", "/tmp/plan.md"},
		{"/tmp/session-123/plan.md", true, "plan.md", "/tmp/plan.md"},
		{"/tmp/sub/file.txt", true, "sub/file.txt", "/tmp/sub/file.txt"},
		{"/tmp/sub/../x", true, "x", "/tmp/x"},
		{"/tmp/../../etc/passwd", false, "", ""},
		{".tmp/sub/../x", true, "x", "/tmp/x"},
		{".tmp/../../etc/passwd", false, "", ""},
		// Relative paths without /tmp or .tmp prefix are NOT session tmp paths
		{"tmp/plan.md", false, "", ""},
		{"src/main.go", false, "", ""},
		{"plan.md", false, "", ""},
	}

	for _, tt := range tests {
		isTmp, sub := ResolveSessionTmpPath(tt.input, chatID)
		assert.Equal(t, tt.expectTmp, isTmp, "ResolveSessionTmpPath(%s)", tt.input)
		if isTmp {
			assert.Equal(t, tt.expectSub, sub, "subpath for %s", tt.input)
		}

		isAuthTmp, normAuth := NormalizeTmpPathForAuth(tt.input, chatID)
		assert.Equal(t, tt.expectTmp, isAuthTmp, "NormalizeTmpPathForAuth(%s)", tt.input)
		if isAuthTmp {
			assert.Equal(t, tt.expectAuth, normAuth, "auth norm for %s", tt.input)
		}
	}
}

func TestIsRelativeTmpPrefixedPath(t *testing.T) {
	chatID := "session-123"

	tests := []struct {
		input     string
		expectTmp bool
		expectSub string
	}{
		{"tmp", true, ""},
		{"tmp/plan.md", true, "plan.md"},
		{"tmp/session-id", true, ""},
		{"tmp/${session_id}", true, ""},
		{"tmp/session-123", true, ""},
		{"tmp/session-id/plan.md", true, "plan.md"},
		{"tmp/${session_id}/plan.md", true, "plan.md"},
		{"tmp/session-123/plan.md", true, "plan.md"},
		{"tmp/sub/file.txt", true, "sub/file.txt"},
		// Negative cases
		{"tmpother/plan.md", false, ""},
		{"src/main.go", false, ""},
		{"plan.md", false, ""},
		{"/tmp/plan.md", false, ""},
	}

	for _, tt := range tests {
		isTmp, sub := isRelativeTmpPrefixedPath(tt.input, chatID)
		assert.Equal(t, tt.expectTmp, isTmp, "isRelativeTmpPrefixedPath(%s)", tt.input)
		if isTmp {
			assert.Equal(t, tt.expectSub, sub, "subpath for %s", tt.input)
		}
	}
}

func TestNormalizeSessionRunDir_Extended(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	chatID := "session-123"
	expectedSessionTmp := filepath.Join(tempHome, "tmp", chatID)

	// Relative narrow forms
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir("tmp", chatID))
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir("tmp/session-id", chatID))
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir("tmp/${session_id}", chatID))
	assert.Equal(t, expectedSessionTmp, NormalizeSessionRunDir("tmp/"+chatID, chatID))
	assert.Equal(t, filepath.Join(expectedSessionTmp, "sub"), NormalizeSessionRunDir("tmp/session-id/sub", chatID))

	// Arbitrary relative path under workspace
	assert.Equal(t, "tmp/arbitrary", NormalizeSessionRunDir("tmp/arbitrary", chatID))
	assert.Equal(t, "src/main", NormalizeSessionRunDir("src/main", chatID))

	// chatID == "" handling
	assert.Equal(t, "", NormalizeSessionRunDir("", ""))
	assert.Equal(t, ".", NormalizeSessionRunDir(".", ""))
	assert.Equal(t, "tmp", NormalizeSessionRunDir("tmp", ""))
	assert.Equal(t, "/tmp", NormalizeSessionRunDir("/tmp", ""))
	assert.Equal(t, "src/main", NormalizeSessionRunDir("src/main", ""))
}
