package dbmodels

import (
	"testing"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/db"
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
