package api

import (
	"encoding/json"
	"path/filepath"
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
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
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

func TestSingleAgentExecutor_Attachments(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)

	server := &Server{
		conf:     &config.Config{},
		repo:     repo,
		eventHub: hub,
	}

	chatID := "test-chat-executor-attachments"
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

	attachments := []dbmodels.Attachment{
		{
			Name:     "data.csv",
			Path:     "/malicious/client/path/data.csv",
			Size:     1024,
			MimeType: "text/csv",
		},
		{
			Name:     "photo.png",
			Path:     "/other/path/photo.png",
			Size:     2048,
			MimeType: "image/png",
		},
	}

	// When Execute runs, it persists the userMsg with prompt and params.Attachments
	// Since run.Run will try to run CLI without mock backend, we can test the pre-run persistence and parameters
	// Or we can invoke Execute and let it finish or fail, then verify the DB state
	_, _ = executor.Execute(t.Context(), SingleAgentRunParams{
		ChatID:      chatID,
		Prompt:      "Analyze user files",
		Attachments: attachments,
	})

	// Verify DB message persistence
	session, err := repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotEmpty(t, session.Messages)

	var userMsg *dbmodels.ChatMessage
	for _, m := range session.Messages {
		if m.Role == "user" {
			userMsg = &m
			break
		}
	}
	require.NotNil(t, userMsg)
	// Content must remain raw prompt without [Attached Files] injection
	assert.Equal(t, "Analyze user files", userMsg.Content)
	// Attachments must be preserved
	require.Len(t, userMsg.Attachments, 2)
	assert.Equal(t, "data.csv", userMsg.Attachments[0].Name)
	assert.Equal(t, int64(1024), userMsg.Attachments[0].Size)
	assert.Equal(t, "photo.png", userMsg.Attachments[1].Name)
	assert.Equal(t, int64(2048), userMsg.Attachments[1].Size)

	// Verify SSE broadcast included raw content and attachments
	select {
	case ev := <-subCh:
		assert.Equal(t, "message", ev.Type)
		require.NotNil(t, ev.Message)
		assert.Equal(t, "Analyze user files", ev.Message.Content)
		require.Len(t, ev.Message.Attachments, 2)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for SSE user message event with attachments")
	}
}

func TestSingleAgentExecutor_Execute_TmpDirPreCreation(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
	chatID := "test-chat-tmp-precreate"

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:      "test-agent-tmp",
			Name:    "Test Agent Tmp",
			RunDirs: []string{"tmp"},
		},
	}

	executor := NewSingleAgentExecutor(agent, &config.Config{}, repo, nil, nil)

	sessionTmpDir := filepath.Join(tempHome, "tmp", chatID)
	assert.NoDirExists(t, sessionTmpDir)

	// Execute will validate run_dir. Since run.Run fails later without real CLI, Execute will proceed past os.Stat
	_, _ = executor.Execute(t.Context(), SingleAgentRunParams{
		ChatID: chatID,
		Prompt: "Test tmp run_dir creation",
		RunDir: "tmp",
	})

	// The session tmp directory should have been automatically created before os.Stat
	assert.DirExists(t, sessionTmpDir)
}
