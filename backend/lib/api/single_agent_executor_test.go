package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func TestSingleAgentExecutor_TokenHandling(t *testing.T) {
	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)

	server := &Server{
		conf:     &config.Config{},
		repo:     repo,
		eventHub: hub,
	}

	t.Run("records valid tokens and emits SSE message", func(t *testing.T) {
		chatID := "test-chat-token-1"
		require.NoError(t, repo.SaveSession(&dbmodels.Session{
			ChatID:       chatID,
			CurrentAgent: "test-agent",
		}))

		agent := &agentspec.Agent{
			Config: agentspec.AgentConfig{
				ID:   "test-agent",
				Name: "Test Agent",
				CLI: []agentspec.CLITarget{
					{CLI: "agy", Model: "gemini-3.7-flash-high"},
				},
			},
		}

		executor := NewSingleAgentExecutor(agent, &config.Config{}, repo, server, nil)

		subCh, _, cancel := hub.Subscribe(chatID, 0)
		t.Cleanup(cancel)

		resultJSON, err := json.Marshal(promptResult{
			SessionID:   "sess-123",
			InputTokens: 4555,
			MaxTokens:   1048576,
			LastContent: "Execution response",
		})
		require.NoError(t, err)

		resp, err := executor.handleFinalResult(resultJSON, chatID, optional.None[string](), optional.None[string](), "resume")
		require.NoError(t, err)
		assert.Equal(t, "Execution response", resp)

		// Check persisted message in repo
		session, err := repo.GetSession(chatID)
		require.NoError(t, err)
		require.NotEmpty(t, session.Messages)

		lastMsg := session.Messages[len(session.Messages)-1]
		assert.Equal(t, "assistant", lastMsg.Role)
		assert.Equal(t, "Execution response", lastMsg.Content)
		assert.Equal(t, 4555, lastMsg.InputTokens)
		assert.Equal(t, 1048576, lastMsg.MaxTokens)

		// Check SSE published event
		select {
		case ev := <-subCh:
			assert.Equal(t, "message", ev.Type)
			require.NotNil(t, ev.Message)
			assert.Equal(t, 4555, ev.Message.InputTokens)
			assert.Equal(t, 1048576, ev.Message.MaxTokens)
		case <-time.After(1 * time.Second):
			t.Fatal("timed out waiting for SSE message event")
		}
	})

	t.Run("falls back max_tokens using modelOpt", func(t *testing.T) {
		chatID := "test-chat-token-2"
		require.NoError(t, repo.SaveSession(&dbmodels.Session{
			ChatID:       chatID,
			CurrentAgent: "test-agent-2",
		}))

		agent := &agentspec.Agent{
			Config: agentspec.AgentConfig{
				ID:   "test-agent-2",
				Name: "Test Agent 2",
				CLI: []agentspec.CLITarget{
					{CLI: "opencode", Model: "opencode/big-pickle"},
				},
			},
		}

		executor := NewSingleAgentExecutor(agent, &config.Config{}, repo, server, nil)

		// Non-JSON raw string output where maxTokens will parse as 0
		rawOutput := []byte("Plain text non-json output")

		resp, err := executor.handleFinalResult(rawOutput, chatID, optional.None[string](), optional.Some("claude-sonnet-4-6"), "resume")
		require.NoError(t, err)
		assert.Equal(t, "Plain text non-json output", resp)

		session, err := repo.GetSession(chatID)
		require.NoError(t, err)
		require.NotEmpty(t, session.Messages)

		lastMsg := session.Messages[len(session.Messages)-1]
		assert.Equal(t, "Plain text non-json output", lastMsg.Content)
		assert.Equal(t, 0, lastMsg.InputTokens)
		// claude-sonnet-4-6 has 256K context window
		assert.Equal(t, 256000, lastMsg.MaxTokens)
	})

	t.Run("falls back max_tokens using agent config CLI model when modelOpt is empty", func(t *testing.T) {
		chatID := "test-chat-token-3"
		require.NoError(t, repo.SaveSession(&dbmodels.Session{
			ChatID:       chatID,
			CurrentAgent: "test-agent-3",
		}))

		agent := &agentspec.Agent{
			Config: agentspec.AgentConfig{
				ID:   "test-agent-3",
				Name: "Test Agent 3",
				CLI: []agentspec.CLITarget{
					{CLI: "opencode", Model: "opencode/big-pickle"},
				},
			},
		}

		executor := NewSingleAgentExecutor(agent, &config.Config{}, repo, server, nil)

		rawOutput := []byte("Raw output with default CLI model")

		resp, err := executor.handleFinalResult(rawOutput, chatID, optional.None[string](), optional.None[string](), "resume")
		require.NoError(t, err)
		assert.Equal(t, "Raw output with default CLI model", resp)

		session, err := repo.GetSession(chatID)
		require.NoError(t, err)
		require.NotEmpty(t, session.Messages)

		lastMsg := session.Messages[len(session.Messages)-1]
		// opencode/big-pickle has 200K context window
		assert.Equal(t, 200000, lastMsg.MaxTokens)
	})
}
