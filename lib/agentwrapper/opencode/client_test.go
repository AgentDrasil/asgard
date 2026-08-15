package opencode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClient_SystemPromptHeader(t *testing.T) {
	t.Parallel()

	client := NewClient()
	header := client.SystemPromptHeader()

	assert.Contains(t, header, "/bin/ask-user")
	assert.Contains(t, header, "/bin/call-peer")
	assert.Contains(t, header, "Protocol and Tool Restrictions")
	assert.Contains(t, header, "question")
	assert.Contains(t, header, "task")
}
