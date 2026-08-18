package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
	"github.com/AgentDrasil/asgard/lib/llm"
)

type fakeTitleClient struct {
	text  string
	calls int
}

func (f *fakeTitleClient) GenerateText(_ context.Context, _ llm.GenerateOptions) (string, error) {
	f.calls++
	return f.text, nil
}

func waitForTitle(t *testing.T, s *Server, chatID string) *dbmodels.Session {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		session, err := s.repo.GetSession(chatID)
		require.NoError(t, err)
		if session != nil && session.Title != "" {
			return session
		}
		if time.Now().After(deadline) {
			t.Fatalf("session title was not generated; session=%v", session)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkflowTitleGeneratesTitle(t *testing.T) {
	s, _, _ := newAskReplyTestServer(t)
	chatID := "chat-wf-title"
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))

	client := &fakeTitleClient{text: "Deploy Service Workflow"}
	goGenerateSessionTitle(context.Background(), s, client, s.repo, chatID, "please deploy the service to prod", "wf-agent", "runs a deploy workflow")

	session := waitForTitle(t, s, chatID)
	assert.Equal(t, "Deploy Service Workflow", session.Title)
	assert.Equal(t, 1, client.calls)
}

func TestWorkflowTitleSkipsExistingTitle(t *testing.T) {
	s, _, _ := newAskReplyTestServer(t)
	chatID := "chat-wf-title-exists"
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent", Title: "Existing Title"}))

	agent := &agents.Agent{Config: agents.AgentConfig{
		ID:          "wf-agent",
		Name:        "Workflow Agent",
		Description: "runs a deploy workflow",
	}}

	s.maybeGenerateWorkflowTitle(context.Background(), agent, chatID, "another request")

	time.Sleep(100 * time.Millisecond)
	session, err := s.repo.GetSession(chatID)
	require.NoError(t, err)
	assert.Equal(t, "Existing Title", session.Title)
}

func TestWorkflowTitleSkipsInvalidChatID(t *testing.T) {
	s, _, _ := newAskReplyTestServer(t)
	agent := &agents.Agent{Config: agents.AgentConfig{
		ID:          "wf-agent",
		Name:        "Workflow Agent",
		Description: "runs a deploy workflow",
	}}

	s.maybeGenerateWorkflowTitle(context.Background(), agent, "../invalid", "request")
	time.Sleep(100 * time.Millisecond)

	sess, err := s.repo.GetSession("../invalid")
	assert.NoError(t, err)
	assert.Nil(t, sess, "no session should be created or updated for invalid chat ID")
}
