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

	// 2. UpdateAgentSession should create a session and save the agent
	err = repo.UpdateAgentSession(chatID, "agent-1", "session-1", optional.None[string]())
	assert.NoError(t, err)

	// Verify session was created
	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, chatID, sess.ChatID)
	assert.Equal(t, "agent-1", sess.CurrentAgent)
	require.Len(t, sess.Agents, 1)
	assert.Equal(t, "agent-1", sess.Agents[0].Name)
	assert.Equal(t, "session-1", sess.Agents[0].SessionID)

	// 3. UpdateAgentSession for the same agent should update the session ID
	err = repo.UpdateAgentSession(chatID, "agent-1", "session-1-updated", optional.None[string]())
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "agent-1", sess.CurrentAgent)
	require.Len(t, sess.Agents, 1)
	assert.Equal(t, "agent-1", sess.Agents[0].Name)
	assert.Equal(t, "session-1-updated", sess.Agents[0].SessionID)

	// 4. UpdateAgentSession for a different agent should append to the list
	err = repo.UpdateAgentSession(chatID, "agent-2", "session-2", optional.None[string]())
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "agent-1", sess.CurrentAgent)
	require.Len(t, sess.Agents, 2)
	assert.Equal(t, "agent-1", sess.Agents[0].Name)
	assert.Equal(t, "session-1-updated", sess.Agents[0].SessionID)
	assert.Equal(t, "agent-2", sess.Agents[1].Name)
	assert.Equal(t, "session-2", sess.Agents[1].SessionID)

	// 5. UpdateAgentSession should update RunDir
	err = repo.UpdateAgentSession(chatID, "agent-2", "session-2", optional.Some("/some/run/dir"))
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "/some/run/dir", sess.RunDir)

	// Test GetSessions
	sessions, err := repo.GetSessions()
	assert.NoError(t, err)
	assert.Len(t, sessions, 1)
	assert.Equal(t, chatID, sessions[0].ChatID)

	// Save session directly to test Title saving
	sessions[0].Title = "Test Chat Title"
	err = repo.SaveSession(&sessions[0])
	assert.NoError(t, err)

	// Test UpdateSessionTitle
	err = repo.UpdateSessionTitle(chatID, "Updated Chat Title")
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	assert.Equal(t, "Updated Chat Title", sess.Title)

	// Test DeleteSession
	err = repo.DeleteSession(chatID)
	assert.NoError(t, err)

	sess, err = repo.GetSession(chatID)
	assert.NoError(t, err)
	assert.Nil(t, sess)
}
