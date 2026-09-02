package dbmodels

import (
	"testing"
	"time"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
)

func TestSessionRepository(t *testing.T) {
	testDB := db.NewDBForTest(t)

	// Migrate the Session model
	err := testDB.AutoMigrate(&Session{})
	require.NoError(t, err)

	repo := NewSessionRepository(testDB)

	chatID := "test-chat-id"

	// 1. GetSession of non-existent session should return nil, nil
	sess, err := repo.GetSession(chatID)
	assert.NoError(t, err)
	assert.Nil(t, sess)

	// 2. UpdateAgentSession should create a session and save the agent with the session ID
	err = repo.UpdateAgentSession(chatID, "agent-1", "agy/gemini-flash", "session-1", optional.None[string]())
	assert.NoError(t, err)

	// Verify session was created
	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, chatID, sess.ChatID)
	assert.Equal(t, "agent-1", sess.CurrentAgent)
	require.Len(t, sess.Agents, 1)
	assert.Equal(t, "agent-1", sess.Agents[0].Name)
	assert.Equal(t, "session-1", sess.Agents[0].Sessions["agy/gemini-flash"])

	// 3. UpdateAgentSession for the same agent+cliKey should update the session ID
	err = repo.UpdateAgentSession(chatID, "agent-1", "agy/gemini-flash", "session-1-updated", optional.None[string]())
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "agent-1", sess.CurrentAgent)
	require.Len(t, sess.Agents, 1)
	assert.Equal(t, "agent-1", sess.Agents[0].Name)
	assert.Equal(t, "session-1-updated", sess.Agents[0].Sessions["agy/gemini-flash"])

	// 4. UpdateAgentSession for a different CLI key on the same agent should add an entry
	err = repo.UpdateAgentSession(chatID, "agent-1", "opencode/zai", "session-oc-1", optional.None[string]())
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.Agents, 1)
	assert.Equal(t, "session-1-updated", sess.Agents[0].Sessions["agy/gemini-flash"])
	assert.Equal(t, "session-oc-1", sess.Agents[0].Sessions["opencode/zai"])

	// 5. UpdateAgentSession for a different agent should append to the list
	err = repo.UpdateAgentSession(chatID, "agent-2", "agy/gemini-flash", "session-2", optional.None[string]())
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "agent-1", sess.CurrentAgent)
	require.Len(t, sess.Agents, 2)
	assert.Equal(t, "agent-1", sess.Agents[0].Name)
	assert.Equal(t, "agent-2", sess.Agents[1].Name)
	assert.Equal(t, "session-2", sess.Agents[1].Sessions["agy/gemini-flash"])

	// 6. UpdateAgentSession should update RunDir
	err = repo.UpdateAgentSession(chatID, "agent-2", "", "", optional.Some("/some/run/dir"))
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "/some/run/dir", sess.RunDir)

	// 7. GetAgentSessions returns the correct sessions map
	sessions2, err := repo.GetAgentSessions(chatID, "agent-1")
	assert.NoError(t, err)
	require.NotNil(t, sessions2)
	assert.Equal(t, "session-1-updated", sessions2["agy/gemini-flash"])
	assert.Equal(t, "session-oc-1", sessions2["opencode/zai"])

	// GetAgentSessions for non-existent agent returns nil
	sessions3, err := repo.GetAgentSessions(chatID, "nonexistent")
	assert.NoError(t, err)
	assert.Nil(t, sessions3)

	// Test GetSessions
	allSessions, err := repo.GetSessions()
	assert.NoError(t, err)
	assert.Len(t, allSessions, 1)
	assert.Equal(t, chatID, allSessions[0].ChatID)

	// Save session directly to test Title saving
	allSessions[0].Title = "Test Chat Title"
	err = repo.SaveSession(&allSessions[0])
	assert.NoError(t, err)

	// Test UpdateSessionTitle
	err = repo.UpdateSessionTitle(chatID, "Updated Chat Title")
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	assert.Equal(t, "Updated Chat Title", sess.Title)

	// 10. AppendArtifact should add deduplicated artifact paths
	err = repo.AppendArtifact(chatID, ".tmp/report.md")
	assert.NoError(t, err)
	err = repo.AppendArtifact(chatID, ".tmp/report.md") // duplicate
	assert.NoError(t, err)
	err = repo.AppendArtifact(chatID, "src/main.go")
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, Artifacts{".tmp/report.md", "src/main.go"}, sess.Artifacts)

	// AppendArtifact and AppendMessage on non-existent session should return an error
	err = repo.AppendArtifact("non-existent-id", "some/file.txt")
	assert.Error(t, err)
	err = repo.AppendMessage("non-existent-id", ChatMessage{Content: "hello"})
	assert.Error(t, err)

	// 11. DeleteSession
	err = repo.DeleteSession(chatID)
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	assert.Nil(t, sess)
}

func TestSessionIsRunning(t *testing.T) {
	s := &Session{
		Agents: Agents{
			{Name: "agent-1", Status: AgentStatusCompleted},
			{Name: "agent-2", Status: AgentStatusRunning},
		},
	}
	assert.True(t, s.IsRunning())

	s.Agents[1].Status = AgentStatusCompleted
	assert.False(t, s.IsRunning())
}

func TestAppendMessage_Deduplication(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)

	chatID := "test-dedup-chat"
	require.NoError(t, repo.SaveSession(&Session{ChatID: chatID}))

	// 1. Append first message with ID
	msg1 := ChatMessage{
		ID:        "msg-1",
		Role:      "ask_user",
		Content:   "Please approve plan",
		Timestamp: 1000,
	}
	require.NoError(t, repo.AppendMessage(chatID, msg1))

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.Messages, 1)
	assert.Equal(t, "Please approve plan", sess.Messages[0].Content)

	// 2. Append duplicate message with same ID but updated content/timestamp
	msg1Updated := ChatMessage{
		ID:        "msg-1",
		Role:      "ask_user",
		Content:   "Please approve updated plan",
		Timestamp: 1005,
	}
	require.NoError(t, repo.AppendMessage(chatID, msg1Updated))

	sess, err = repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.Messages, 1, "Duplicate ID should update in-place instead of appending")
	assert.Equal(t, "Please approve updated plan", sess.Messages[0].Content)
	assert.Equal(t, int64(1005), sess.Messages[0].Timestamp)

	// 3. Mark as replied, then append duplicate without Replied flag -> should preserve replied state
	updatedMsg, err := repo.MarkAskUserReplied(chatID, "msg-1", "Approve")
	require.NoError(t, err)
	require.NotNil(t, updatedMsg)
	assert.True(t, updatedMsg.Replied)
	assert.Equal(t, "Approve", updatedMsg.ReplyText)
	require.NoError(t, repo.AppendMessage(chatID, ChatMessage{
		ID:        "msg-1",
		Role:      "ask_user",
		Content:   "Please approve updated plan",
		Timestamp: 1010,
	}))

	sess, err = repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.Messages, 1)
	assert.True(t, sess.Messages[0].Replied)
	assert.Equal(t, "Approve", sess.Messages[0].ReplyText)

	// 4. Append message without ID -> should append normally
	require.NoError(t, repo.AppendMessage(chatID, ChatMessage{
		Role:    "user",
		Content: "normal message",
	}))
	require.NoError(t, repo.AppendMessage(chatID, ChatMessage{
		Role:    "user",
		Content: "another message",
	}))

	sess, err = repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.Messages, 3)
}

func TestUpdateAgentSession_DefaultStatus(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)

	chatID := "test-status-chat"
	err := repo.UpdateAgentSession(chatID, "agent-1", "cli/model", "sess-1", optional.None[string]())
	require.NoError(t, err)

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.Agents, 1)
	assert.Equal(t, AgentStatusCompleted, sess.Agents[0].Status)
	assert.False(t, sess.IsRunning())
}

func TestResetAllRunningAgents(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)

	// Session 1: running agent
	require.NoError(t, repo.SaveSession(&Session{
		ChatID: "chat-1",
		Agents: Agents{
			{Name: "agent-1", Status: AgentStatusRunning},
		},
	}))
	// Session 2: already completed agent
	require.NoError(t, repo.SaveSession(&Session{
		ChatID: "chat-2",
		Agents: Agents{
			{Name: "agent-2", Status: AgentStatusCompleted},
		},
	}))
	// Session 3: multiple agents with mixed status
	require.NoError(t, repo.SaveSession(&Session{
		ChatID: "chat-3",
		Agents: Agents{
			{Name: "agent-3a", Status: AgentStatusRunning},
			{Name: "agent-3b", Status: AgentStatusCompleted},
		},
	}))

	require.NoError(t, repo.ResetAllRunningAgents())

	sess1, err := repo.GetSession("chat-1")
	require.NoError(t, err)
	assert.Equal(t, AgentStatusCompleted, sess1.Agents[0].Status)
	assert.False(t, sess1.IsRunning())

	sess2, err := repo.GetSession("chat-2")
	require.NoError(t, err)
	assert.Equal(t, AgentStatusCompleted, sess2.Agents[0].Status)

	sess3, err := repo.GetSession("chat-3")
	require.NoError(t, err)
	assert.Equal(t, AgentStatusCompleted, sess3.Agents[0].Status)
	assert.Equal(t, AgentStatusCompleted, sess3.Agents[1].Status)
	assert.False(t, sess3.IsRunning())
}

func TestSessionRepository_SearchSessions(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)

	// Seed sessions
	now := time.Now()
	sessionsToSeed := []Session{
		{
			ChatID:    "sess-1",
			Title:     "Refactor Authentication Flow",
			UpdatedAt: now.Add(-10 * time.Minute),
		},
		{
			ChatID:    "sess-2",
			Title:     "Fix Bug in auth_controller",
			UpdatedAt: now.Add(-5 * time.Minute),
		},
		{
			ChatID:    "sess-3",
			Title:     "Deploy to 100% Canary Cluster",
			UpdatedAt: now.Add(-2 * time.Minute),
		},
		{
			ChatID:    "sess-4",
			Title:     "Canary_Release_v2",
			UpdatedAt: now.Add(-1 * time.Minute),
		},
		{
			ChatID:    "sess-5",
			Title:     "Canary1Release",
			UpdatedAt: now.Add(-30 * time.Second),
		},
	}

	for _, s := range sessionsToSeed {
		sess := s
		require.NoError(t, repo.SaveSession(&sess))
	}

	tests := []struct {
		name        string
		query       string
		limit       int
		expectedIDs []string
	}{
		{
			name:        "Case-insensitive match single",
			query:       "refactor",
			limit:       10,
			expectedIDs: []string{"sess-1"},
		},
		{
			name:        "Case-insensitive match multiple ordered by updated_at desc",
			query:       "AUTH",
			limit:       10,
			expectedIDs: []string{"sess-2", "sess-1"},
		},
		{
			name:        "Wildcard percent literal search",
			query:       "100%",
			limit:       10,
			expectedIDs: []string{"sess-3"},
		},
		{
			name:        "Wildcard underscore literal search",
			query:       "Canary_",
			limit:       10,
			expectedIDs: []string{"sess-4"},
		},
		{
			name:        "Empty query returns empty slice",
			query:       "",
			limit:       10,
			expectedIDs: []string{},
		},
		{
			name:        "Whitespace query returns empty slice",
			query:       "   \t\n ",
			limit:       10,
			expectedIDs: []string{},
		},
		{
			name:        "No matching sessions returns empty slice",
			query:       "nonexistent-keyword-xyz",
			limit:       10,
			expectedIDs: []string{},
		},
		{
			name:        "Limit parameter truncates results",
			query:       "Canary",
			limit:       2,
			expectedIDs: []string{"sess-5", "sess-4"},
		},
		{
			name:        "Limit <= 0 defaults to 20",
			query:       "Canary",
			limit:       0,
			expectedIDs: []string{"sess-5", "sess-4", "sess-3"},
		},
		{
			name:        "Limit negative defaults to 20",
			query:       "Canary",
			limit:       -5,
			expectedIDs: []string{"sess-5", "sess-4", "sess-3"},
		},
		{
			name:        "Limit > 50 clamped to 50",
			query:       "Canary",
			limit:       100,
			expectedIDs: []string{"sess-5", "sess-4", "sess-3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := repo.SearchSessions(tt.query, tt.limit)
			require.NoError(t, err)
			require.NotNil(t, results, "Results slice should not be nil")

			resultIDs := make([]string, 0, len(results))
			for _, r := range results {
				resultIDs = append(resultIDs, r.ChatID)
			}
			assert.Equal(t, tt.expectedIDs, resultIDs)
		})
	}
}
