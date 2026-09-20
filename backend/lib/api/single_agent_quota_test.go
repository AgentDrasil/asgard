package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func quotaTestConfig() agentspec.AgentConfig {
	return agentspec.AgentConfig{
		ID:   "quota-agent",
		Name: "Quota Agent",
		CLI: []agentspec.CLITarget{
			{CLI: "agy", Model: "model-one"},
			{CLI: "opencode", Model: "model-two"},
		},
	}
}

func newQuotaTestRepo(t *testing.T) *dbmodels.SessionRepository {
	t.Helper()
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)
	base := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string { return filepath.Join(base, chatID) })
	return repo
}

func newQuotaTestServer(t *testing.T, repo *dbmodels.SessionRepository) *Server {
	t.Helper()
	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)
	return &Server{conf: &config.Config{}, repo: repo, eventHub: hub}
}

func TestResolveModel(t *testing.T) {
	cfg := quotaTestConfig()

	t.Run("explicit param wins", func(t *testing.T) {
		assert.Equal(t, "requested", resolveModel(SingleAgentRunParams{Model: "requested"}, nil, cfg).Unwrap())
	})

	t.Run("metadata model used when no param", func(t *testing.T) {
		m := resolveModel(SingleAgentRunParams{Metadata: map[string]any{"model": "meta-model"}}, nil, cfg)
		assert.Equal(t, "meta-model", m.Unwrap())
	})

	t.Run("per-agent stored model is inherited", func(t *testing.T) {
		session := &dbmodels.Session{Agents: []dbmodels.Agent{{Name: cfg.ID, Model: "model-two"}}}
		assert.Equal(t, "model-two", resolveModel(SingleAgentRunParams{}, session, cfg).Unwrap())
	})

	t.Run("foreign agent stored model is ignored", func(t *testing.T) {
		session := &dbmodels.Session{Agents: []dbmodels.Agent{{Name: "someone-else", Model: "model-one"}}}
		assert.True(t, resolveModel(SingleAgentRunParams{}, session, cfg).IsNone())
	})

	t.Run("model removed from config is ignored", func(t *testing.T) {
		session := &dbmodels.Session{Agents: []dbmodels.Agent{{Name: cfg.ID, Model: "retired-model"}}}
		assert.True(t, resolveModel(SingleAgentRunParams{}, session, cfg).IsNone())
	})

	t.Run("assistant message model is never inherited", func(t *testing.T) {
		session := &dbmodels.Session{Messages: []dbmodels.ChatMessage{{Role: "assistant", Model: "model-one"}}}
		assert.True(t, resolveModel(SingleAgentRunParams{}, session, cfg).IsNone())
	})
}

func TestSingleAgentExecutor_QuotaSuspensionCancel(t *testing.T) {
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)

	chatID := "test-chat-quota-cancel"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "quota-agent"}))

	agent := &agentspec.Agent{Config: quotaTestConfig()}
	executor := NewSingleAgentExecutor(agent, &config.Config{}, repo, server, nil)

	var calls int
	executor.suspendQuota = func(_ context.Context, gotChatID, agentName, prompt string, options []string) (string, error) {
		calls++
		assert.Equal(t, chatID, gotChatID)
		assert.Equal(t, "Quota Agent", agentName)
		assert.NotEmpty(t, prompt)
		assert.Contains(t, options, "Cancel run")
		return "Cancel run", nil
	}

	_, err := executor.Execute(t.Context(), SingleAgentRunParams{ChatID: chatID, Prompt: "hello"})
	require.Error(t, err)
	assert.Equal(t, 1, calls)

	session, err := repo.GetSession(chatID)
	require.NoError(t, err)
	var cancelled bool
	for _, m := range session.Messages {
		if m.Role == "activity" && m.ActivityType == "CANCELLED" {
			cancelled = true
		}
	}
	assert.True(t, cancelled, "quota cancellation should be recorded as an activity message")
}

func TestSingleAgentExecutor_QuotaSuspensionRetriesThenCancels(t *testing.T) {
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)

	chatID := "test-chat-quota-retry"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "quota-agent"}))

	agent := &agentspec.Agent{Config: quotaTestConfig()}
	executor := NewSingleAgentExecutor(agent, &config.Config{}, repo, server, nil)

	replies := []string{"Wait for quota recovery, then continue", "Cancel run"}
	var calls int
	executor.suspendQuota = func(context.Context, string, string, string, []string) (string, error) {
		reply := replies[calls]
		calls++
		return reply, nil
	}

	_, err := executor.Execute(t.Context(), SingleAgentRunParams{ChatID: chatID, Prompt: "hello"})
	require.Error(t, err)
	assert.Equal(t, 2, calls, "a wait decision should re-suspend before the cancel")
}

func TestAskUserQuota_DeliversAndWaitsForReply(t *testing.T) {
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)

	chatID := "test-chat-ask-quota"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "quota-agent"}))

	agent := &agentspec.Agent{Config: quotaTestConfig()}
	executor := NewSingleAgentExecutor(agent, &config.Config{}, repo, server, nil)

	msgID := quotaAskMessageID(chatID, agent.Config.ID, 1)
	go func() {
		for i := 0; i < 200; i++ {
			askWaitersMu.Lock()
			w, ok := askWaiters[msgID]
			askWaitersMu.Unlock()
			if ok {
				w.replyCh <- "Use agy model-one"
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	reply, err := executor.askUserQuota(t.Context(), chatID, "no quota", []string{"Wait", "Cancel run"}, 1)
	require.NoError(t, err)
	assert.Equal(t, "Use agy model-one", reply)

	session, err := repo.GetSession(chatID)
	require.NoError(t, err)
	var askMsg *dbmodels.ChatMessage
	for i := range session.Messages {
		if session.Messages[i].ID == msgID {
			askMsg = &session.Messages[i]
		}
	}
	require.NotNil(t, askMsg)
	assert.Equal(t, "ask_user", askMsg.Role)
	assert.Contains(t, askMsg.Content, "Options: Wait / Cancel run")

	var waiting bool
	for _, a := range session.Agents {
		if a.Name == agent.Config.ID && a.Status == dbmodels.AgentStatusWaitingHuman {
			waiting = true
		}
	}
	assert.True(t, waiting, "agent should be WaitingHuman while suspended")
}

func TestSingleAgentExecutor_UpdateAgentModel(t *testing.T) {
	repo := newQuotaTestRepo(t)

	chatID := "test-chat-update-model"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "quota-agent"}))
	require.NoError(t, repo.UpdateAgentModel(chatID, "quota-agent", "model-two"))

	session, err := repo.GetSession(chatID)
	require.NoError(t, err)
	require.Len(t, session.Agents, 1)
	assert.Equal(t, "model-two", session.Agents[0].Model)
}

func TestSingleAgentExecutor_NoCrossCLIFallback(t *testing.T) {
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)

	chatID := "test-chat-no-cross-cli"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "quota-agent"}))

	// Config with agy first and opencode second.
	// When agy has no quota, it must not automatically fall back to opencode,
	// but suspend and wait for quota decision.
	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "quota-agent",
			Name: "Quota Agent",
			CLI: []agentspec.CLITarget{
				{CLI: "agy", Model: "agy-model-one"},
				{CLI: "agy", Model: "agy-model-two"},
				{CLI: "opencode", Model: "opencode-model"},
			},
		},
	}
	executor := NewSingleAgentExecutor(agent, &config.Config{}, repo, server, nil)

	var suspendedPrompt string
	executor.suspendQuota = func(_ context.Context, gotChatID, agentName, prompt string, options []string) (string, error) {
		suspendedPrompt = prompt
		return "Cancel run", nil
	}

	_, err := executor.Execute(t.Context(), SingleAgentRunParams{ChatID: chatID, Prompt: "hello"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "execution cancelled by user")
	// Verify suspension prompt only contains candidates for agy, not opencode
	assert.Contains(t, suspendedPrompt, "agy agy-model-one")
	assert.Contains(t, suspendedPrompt, "agy agy-model-two")
	assert.NotContains(t, suspendedPrompt, "opencode")
}

func TestSingleAgentExecutor_ForcedCrossCLITargetRebindsCandidates(t *testing.T) {
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)

	chatID := "test-chat-forced-cross-cli"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "quota-agent"}))

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "quota-agent",
			Name: "Quota Agent",
			CLI: []agentspec.CLITarget{
				{CLI: "agy", Model: "agy-model-one"},
				{CLI: "opencode", Model: "opencode-model"},
			},
		},
	}
	executor := NewSingleAgentExecutor(agent, &config.Config{}, repo, server, nil)

	// Both CLIs report no quota in the test environment, so after the user
	// forces the cross-CLI opencode target the run re-suspends. The second
	// prompt must now be anchored to opencode candidates only.
	prompts := []string{
		"Use opencode opencode-model",
		"Cancel run",
	}
	var got []string
	executor.suspendQuota = func(_ context.Context, _, _, prompt string, _ []string) (string, error) {
		got = append(got, prompt)
		reply := prompts[len(got)-1]
		return reply, nil
	}

	_, err := executor.Execute(t.Context(), SingleAgentRunParams{ChatID: chatID, Prompt: "hello"})
	require.Error(t, err)
	require.Len(t, got, 2, "expected re-suspension after the forced cross-CLI pick")

	// First prompt: restricted to agy (same-CLI fallback only).
	assert.Contains(t, got[0], "agy agy-model-one")
	assert.NotContains(t, got[0], "opencode opencode-model")

	// Second prompt: re-anchored to opencode (deliberate user choice).
	assert.Contains(t, got[1], "opencode opencode-model")
	assert.NotContains(t, got[1], "agy agy-model-one")
}
