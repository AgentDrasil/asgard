package agy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClient_SystemPromptHeader(t *testing.T) {
	client := NewClient()
	header := client.SystemPromptHeader()

	assert.Contains(t, header, "/bin/ask-user")
	assert.Contains(t, header, "/bin/call-peer")
	assert.Contains(t, header, "long-running interactive tasks")
}
