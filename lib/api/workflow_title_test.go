package api

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
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

func newTitleTestExecutor(s *Server, client llm.Client) *workflowTitleExecutor {
	return &workflowTitleExecutor{
		server: s,
		agent: &agents.Agent{Config: agents.AgentConfig{
			ID:          "wf-agent",
			Name:        "Workflow Agent",
			Description: "runs a deploy workflow",
		}},
		llmClient: client,
	}
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

func TestWorkflowTitleExecutorGeneratesTitle(t *testing.T) {
	s, _, _ := newAskReplyTestServer(t)
	chatID := "chat-wf-title"
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent"}))

	client := &fakeTitleClient{text: "Deploy Service Workflow"}
	executor := newTitleTestExecutor(s, client)
	executor.maybeGenerateTitle(context.Background(), &a2asrv.ExecutorContext{
		ContextID: chatID,
		Message:   a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("please deploy the service to prod")),
	})

	session := waitForTitle(t, s, chatID)
	assert.Equal(t, "Deploy Service Workflow", session.Title)
	assert.Equal(t, 1, client.calls)
}

func TestWorkflowTitleExecutorSkipsExistingTitle(t *testing.T) {
	s, _, _ := newAskReplyTestServer(t)
	chatID := "chat-wf-title-exists"
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-agent", Title: "Existing Title"}))

	client := &fakeTitleClient{}
	executor := newTitleTestExecutor(s, client)
	executor.maybeGenerateTitle(context.Background(), &a2asrv.ExecutorContext{
		ContextID: chatID,
		Message:   a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("another request")),
	})

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, client.calls, "title generation must be skipped when a title exists")
}

func TestWorkflowTitleExecutorSkipsInvalidChatID(t *testing.T) {
	s, _, _ := newAskReplyTestServer(t)
	client := &fakeTitleClient{}
	executor := newTitleTestExecutor(s, client)
	executor.maybeGenerateTitle(context.Background(), &a2asrv.ExecutorContext{
		ContextID: "../invalid",
		Message:   a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("request")),
	})

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, client.calls, "title generation must be skipped for invalid chat IDs")
}

type fakeInnerExecutor struct {
	onExec func()
}

func (f *fakeInnerExecutor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if f.onExec != nil {
			f.onExec()
		}
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil)
	}
}

func (f *fakeInnerExecutor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {}
}

func TestWorkflowTitleExecutor_AgentStatusLifecycle(t *testing.T) {
	s, _, _ := newAskReplyTestServer(t)
	chatID := "chat-wf-lifecycle"
	require.NoError(t, s.repo.SaveSession(&dbmodels.Session{
		ChatID:       chatID,
		CurrentAgent: "wf-agent",
		Agents: dbmodels.Agents{
			{Name: "wf-agent", Status: dbmodels.AgentStatusCompleted},
		},
	}))

	var statusDuringExec dbmodels.AgentStatus
	inner := &fakeInnerExecutor{
		onExec: func() {
			sess, err := s.repo.GetSession(chatID)
			require.NoError(t, err)
			require.NotNil(t, sess)
			require.Len(t, sess.Agents, 1)
			statusDuringExec = sess.Agents[0].Status
		},
	}

	executor := &workflowTitleExecutor{
		inner:  inner,
		server: s,
		agent: &agents.Agent{Config: agents.AgentConfig{
			ID:   "wf-agent",
			Name: "Workflow Agent",
		}},
	}

	execCtx := &a2asrv.ExecutorContext{
		ContextID: chatID,
		Message:   a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("run workflow")),
	}

	for _, err := range executor.Execute(context.Background(), execCtx) {
		require.NoError(t, err)
	}

	assert.Equal(t, dbmodels.AgentStatusRunning, statusDuringExec, "agent should be marked Running during execution")

	finalSess, err := s.repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotNil(t, finalSess)
	require.Len(t, finalSess.Agents, 1)
	assert.Equal(t, dbmodels.AgentStatusCompleted, finalSess.Agents[0].Status, "agent should be marked Completed after execution exits")
	assert.False(t, finalSess.IsRunning())
}
