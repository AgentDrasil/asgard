package dbmodels

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/backend/lib/db"
)

func TestSessionRepository(t *testing.T) {
	testDB := db.NewDBForTest(t)

	// Migrate the Session model
	err := testDB.AutoMigrate(&Session{})
	require.NoError(t, err)

	repo := NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})

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
	allSessions, err := repo.GetSessions(false)
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
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// 11. DeleteSession
	err = repo.DeleteSession(chatID)
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	assert.Nil(t, sess)
}

func TestDeleteSession_RemovesCABundleDir(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))

	repo := NewSessionRepository(testDB)
	sessionBase := t.TempDir()
	caBase := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(sessionBase, chatID)
	})
	repo.SetCABundleDirFunc(func(chatID string) string {
		return filepath.Join(caBase, chatID)
	})

	chatID := "test-ca-bundle-chat"
	require.NoError(t, repo.SaveSession(&Session{ChatID: chatID}))

	caDir := filepath.Join(caBase, chatID)
	require.NoError(t, os.MkdirAll(caDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(caDir, "merged-ca-certificates.crt"), []byte("cert"), 0644))

	require.NoError(t, repo.DeleteSession(chatID))

	_, err := os.Stat(caDir)
	assert.True(t, os.IsNotExist(err), "Per-chat CA bundle directory should be removed on session deletion")
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
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})

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
	assert.Equal(t, 1, sess.MessageCount)
	assert.True(t, sess.HasAskUserUnreplied)

	// 2. Append duplicate message with same ID but updated content/timestamp -> message_count does not increase
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
	assert.Equal(t, 1, sess.MessageCount, "MessageCount should not increment on in-place update")

	// 3. Mark as replied, then append duplicate without Replied flag -> should preserve replied state
	updatedMsg, err := repo.MarkAskUserReplied(chatID, "msg-1", "Approve")
	require.NoError(t, err)
	require.NotNil(t, updatedMsg)
	assert.True(t, updatedMsg.Replied)
	assert.Equal(t, "Approve", updatedMsg.ReplyText)

	sess, err = repo.GetSession(chatID)
	require.NoError(t, err)
	assert.False(t, sess.HasAskUserUnreplied)

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
	assert.False(t, sess.HasAskUserUnreplied)

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
	assert.Equal(t, 3, sess.MessageCount)
}

func TestUpdateAgentSession_DefaultStatus(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})

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
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})

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
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})

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

func TestSessionRepository_ArchiveAndFilter(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})

	now := time.Now()
	// Insert 2 active sessions and 1 archived session
	require.NoError(t, repo.SaveSession(&Session{
		ChatID:     "active-1",
		Title:      "Active Session 1",
		IsArchived: false,
		UpdatedAt:  now.Add(-2 * time.Minute),
	}))
	require.NoError(t, repo.SaveSession(&Session{
		ChatID:     "active-2",
		Title:      "Active Session 2",
		IsArchived: false,
		UpdatedAt:  now.Add(-1 * time.Minute),
	}))
	require.NoError(t, repo.SaveSession(&Session{
		ChatID:     "archived-1",
		Title:      "Archived Session 1",
		IsArchived: true,
		UpdatedAt:  now.Add(-3 * time.Minute),
	}))

	// 1. Default GetSessions(false) should return the 2 active sessions
	activeList, err := repo.GetSessions(false)
	require.NoError(t, err)
	require.Len(t, activeList, 2)
	assert.Equal(t, "active-2", activeList[0].ChatID)
	assert.Equal(t, "active-1", activeList[1].ChatID)
	assert.False(t, activeList[0].IsArchived)
	assert.False(t, activeList[1].IsArchived)

	// 2. GetSessions(false) should also return active sessions
	activeListExplicit, err := repo.GetSessions(false)
	require.NoError(t, err)
	require.Len(t, activeListExplicit, 2)

	// 3. GetSessions(true) should return the 1 archived session
	archivedList, err := repo.GetSessions(true)
	require.NoError(t, err)
	require.Len(t, archivedList, 1)
	assert.Equal(t, "archived-1", archivedList[0].ChatID)
	assert.True(t, archivedList[0].IsArchived)

	// 4. ArchiveSession should update IsArchived to true
	require.NoError(t, repo.ArchiveSession("active-1"))

	sess, err := repo.GetSession("active-1")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.True(t, sess.IsArchived)

	// Now GetSessions(false) should return 1 active, and GetSessions(true) should return 2 archived
	activeListAfter, err := repo.GetSessions(false)
	require.NoError(t, err)
	require.Len(t, activeListAfter, 1)
	assert.Equal(t, "active-2", activeListAfter[0].ChatID)

	archivedListAfter, err := repo.GetSessions(true)
	require.NoError(t, err)
	require.Len(t, archivedListAfter, 2)

	// 5. SearchSessions should not return archived sessions
	searchResults, err := repo.SearchSessions("Session", 10)
	require.NoError(t, err)
	require.Len(t, searchResults, 1)
	assert.Equal(t, "active-2", searchResults[0].ChatID)
}

func TestSessionRepository_GetSessionsLimit(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})

	now := time.Now()
	// Batch insert 25 active sessions
	for i := 1; i <= 25; i++ {
		require.NoError(t, repo.SaveSession(&Session{
			ChatID:     fmt.Sprintf("sess-%02d", i),
			Title:      fmt.Sprintf("Session %02d", i),
			IsArchived: false,
			UpdatedAt:  now.Add(time.Duration(i) * time.Minute),
		}))
	}

	// 1. Default GetSessions(false) should return all 25 sessions (breaking the old 20 limit)
	allSessions, err := repo.GetSessions(false)
	require.NoError(t, err)
	assert.Len(t, allSessions, 25)
	assert.Equal(t, "sess-25", allSessions[0].ChatID)

	// 2. Custom limit GetSessions(false, 10) should accurately return 10 sessions
	limitedSessions, err := repo.GetSessions(false, 10)
	require.NoError(t, err)
	assert.Len(t, limitedSessions, 10)
	assert.Equal(t, "sess-25", limitedSessions[0].ChatID)
	assert.Equal(t, "sess-16", limitedSessions[9].ChatID)

	// 3. Limit > 1000 clamped to 1000
	maxSessions, err := repo.GetSessions(false, 2000)
	require.NoError(t, err)
	assert.Len(t, maxSessions, 25)
}

func TestNormalizeSessionLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    int
		expected int
	}{
		{"zero defaults to DefaultSessionLimit", 0, DefaultSessionLimit},
		{"negative defaults to DefaultSessionLimit", -10, DefaultSessionLimit},
		{"positive below max returns itself", 42, 42},
		{"at default limit returns default", DefaultSessionLimit, DefaultSessionLimit},
		{"at max boundary returns max", MaxSessionLimit, MaxSessionLimit},
		{"above max limit clamped to max", 2000, MaxSessionLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, NormalizeSessionLimit(tt.input))
		})
	}
}

func TestSession_HasUnrepliedAskUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		session  Session
		expected bool
	}{
		{
			name: "no messages",
			session: Session{
				Messages: Messages{},
			},
			expected: false,
		},
		{
			name: "only user and assistant messages",
			session: Session{
				Messages: Messages{
					{Role: "user", Content: "hello"},
					{Role: "assistant", Content: "world"},
				},
			},
			expected: false,
		},
		{
			name: "unreplied ask_user message",
			session: Session{
				Messages: Messages{
					{Role: "user", Content: "hello"},
					{Role: "ask_user", Content: "approve?", Replied: false},
				},
			},
			expected: true,
		},
		{
			name: "replied ask_user message",
			session: Session{
				Messages: Messages{
					{Role: "user", Content: "hello"},
					{Role: "ask_user", Content: "approve?", Replied: true, ReplyText: "yes"},
				},
			},
			expected: false,
		},
		{
			name: "multiple ask_user messages, last one unreplied",
			session: Session{
				Messages: Messages{
					{Role: "ask_user", Content: "approve 1?", Replied: true, ReplyText: "yes"},
					{Role: "ask_user", Content: "approve 2?", Replied: false},
				},
			},
			expected: true,
		},
		{
			name: "metadata flag HasAskUserUnreplied true even if Messages slice is empty",
			session: Session{
				HasAskUserUnreplied: true,
				Messages:            Messages{},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, tt.session.HasUnrepliedAskUser())
		})
	}
}

func TestSession_MessagesRemovedFromDB(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))

	// Assert that sessions table does NOT have messages column
	hasColumn := testDB.Migrator().HasColumn(&Session{}, "messages")
	assert.False(t, hasColumn, "sessions table must NOT have messages column")

	hasMessageCount := testDB.Migrator().HasColumn(&Session{}, "message_count")
	assert.True(t, hasMessageCount, "sessions table must have message_count column")

	hasAskUser := testDB.Migrator().HasColumn(&Session{}, "has_ask_user_unreplied")
	assert.True(t, hasAskUser, "sessions table must have has_ask_user_unreplied column")

	hasSummary := testDB.Migrator().HasColumn(&Session{}, "last_message_summary")
	assert.True(t, hasSummary, "sessions table must have last_message_summary column")
}

func TestSession_AppendMessage_NonExistentSession(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})

	err := repo.AppendMessage("non-existent-session-id", ChatMessage{ID: "m1", Role: "user", Content: "hello"})
	require.Error(t, err)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Verify transcript was NOT created
	msgs, err := ReadMessages(filepath.Join(tempDir, "non-existent-session-id"))
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

func TestSession_MarkAskUserReplied_Transcript(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})

	// Non-existent session returns (nil, nil)
	updatedMsg, err := repo.MarkAskUserReplied("non-existent", "m1", "reply")
	require.NoError(t, err)
	assert.Nil(t, updatedMsg)

	// Existing session
	chatID := "chat-replied-test"
	require.NoError(t, repo.SaveSession(&Session{ChatID: chatID}))

	require.NoError(t, repo.AppendMessage(chatID, ChatMessage{
		ID:      "q1",
		Role:    "ask_user",
		Content: "Shall we proceed?",
	}))

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.True(t, sess.HasAskUserUnreplied)

	updatedMsg, err = repo.MarkAskUserReplied(chatID, "q1", "Yes, proceed!")
	require.NoError(t, err)
	require.NotNil(t, updatedMsg)
	assert.True(t, updatedMsg.Replied)
	assert.Equal(t, "Yes, proceed!", updatedMsg.ReplyText)

	sess, err = repo.GetSession(chatID)
	require.NoError(t, err)
	assert.False(t, sess.HasAskUserUnreplied)
}

func TestSession_GetSessions_MetadataOnly(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})

	chatID := "metadata-only-chat"
	require.NoError(t, repo.SaveSession(&Session{ChatID: chatID, Title: "Metadata Chat"}))
	require.NoError(t, repo.AppendMessage(chatID, ChatMessage{ID: "m1", Role: "user", Content: "some long content"}))

	sessions, err := repo.GetSessions(false)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, chatID, sessions[0].ChatID)
	assert.Equal(t, 1, sessions[0].MessageCount)
	assert.Equal(t, "some long content", sessions[0].LastMessageSummary)
	assert.Empty(t, sessions[0].Messages, "GetSessions should only load metadata without messages")
}

func TestSession_SaveSession_FullFlushAtomicAndCountResync(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})

	chatID := "flush-chat"
	sess := &Session{
		ChatID: chatID,
		Title:  "Flush Chat",
		Messages: Messages{
			{ID: "m1", Role: "user", Content: "first message"},
			{ID: "m2", Role: "ask_user", Content: "question?"},
		},
	}
	require.NoError(t, repo.SaveSession(sess))

	loaded, err := repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, 2, loaded.MessageCount)
	assert.True(t, loaded.HasAskUserUnreplied)
	assert.Equal(t, "question?", loaded.LastMessageSummary)
	require.Len(t, loaded.Messages, 2)

	// Test concurrent AppendMessage and SaveSession
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = repo.AppendMessage(chatID, ChatMessage{ID: "m3", Role: "user", Content: "concurrent append"})
	}()
	go func() {
		defer wg.Done()
		_ = repo.SaveSession(&Session{
			ChatID: chatID,
			Title:  "Flush Chat Updated",
			Messages: Messages{
				{ID: "m1", Role: "user", Content: "first message"},
				{ID: "m2", Role: "ask_user", Content: "question?"},
				{ID: "m4", Role: "user", Content: "flush item"},
			},
		})
	}()
	wg.Wait()

	finalSess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	require.NotNil(t, finalSess)
	assert.NotEmpty(t, finalSess.Messages)
}
