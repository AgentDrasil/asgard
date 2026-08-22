package agy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClient_SystemPromptHeader(t *testing.T) {
	t.Parallel()

	client := NewClient()
	header := client.SystemPromptHeader()

	assert.Contains(t, header, "/bin/ask-user")
	assert.Contains(t, header, "Protocol and Tool Restrictions")
	assert.Contains(t, header, "ask_question")
	assert.NotContains(t, header, "call-peer")

	peerHeader := client.SystemPromptPeerHeader()
	assert.Contains(t, peerHeader, "/bin/call-peer")
	assert.Contains(t, peerHeader, "invoke_subagent")
}

func TestClient_ExtraArgs(t *testing.T) {
	t.Parallel()

	client := NewClient()
	assert.Equal(t, []string{"--add-tmp-to-dir"}, client.ExtraArgs())
}
