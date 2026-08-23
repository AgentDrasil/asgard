package simplest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

func TestClient_InterfaceContracts(t *testing.T) {
	t.Parallel()

	client := NewClient()

	var _ types.CLIClient = client
	var _ types.SandboxSpec = client

	assert.NotNil(t, client)
}

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
	assert.Contains(t, peerHeader, "send_message")
}

func TestClient_SandboxSpecPaths(t *testing.T) {
	t.Parallel()

	client := NewClient()
	home := "/home/testuser"

	assert.Equal(t, "/home/testuser/.config/simplest/AGENTS.md", client.SystemPromptConfigPath(home))
	assert.Equal(t, "/home/testuser/.config/simplest/skills", client.SkillsMountPath(home))
	assert.Equal(t, "/home/testuser/.config/simplest", client.AuthDirectory(home))
	assert.Equal(t, []string{"/home/testuser/.simplest", "/home/testuser/.config/simplest"}, client.MountDirectories(home))
	assert.Nil(t, client.ExtraArgs())
}
