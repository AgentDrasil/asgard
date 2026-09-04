package dbmodels

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
)

func setupTestQueueRepo(t *testing.T) (*SessionRepository, string) {
	t.Helper()
	testDB := db.NewDBForTest(t)
	err := AutoMigrate(testDB)
	require.NoError(t, err)

	repo := NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
	return repo, tempDir
}

func TestQueuedMessage_CRUD(t *testing.T) {
	t.Parallel()
	repo, _ := setupTestQueueRepo(t)
	chatID := "chat-crud-1"

	// 1. Initially empty
	msgs, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Empty(t, msgs)

	// Non-existent message lookup
	_, err = repo.GetQueuedMessage(chatID, "non-existent-id")
	assert.ErrorIs(t, err, ErrQueuedMessageNotFound)

	// 2. Enqueue first message
	msg1, err := repo.EnqueueMessage(chatID, "Hello world", "gemini-2.5-flash")
	require.NoError(t, err)
	require.NotNil(t, msg1)
	assert.Equal(t, chatID, msg1.ChatID)
	assert.Equal(t, "Hello world", msg1.Prompt)
	assert.Equal(t, "gemini-2.5-flash", msg1.Model)
	assert.NotEmpty(t, msg1.ID)

	// 3. Enqueue second message
	msg2, err := repo.EnqueueMessage(chatID, "Second prompt", "")
	require.NoError(t, err)
	require.NotNil(t, msg2)

	// 4. GetQueuedMessages returns ordered
	msgs, err = repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, msg1.ID, msgs[0].ID)
	assert.Equal(t, msg2.ID, msgs[1].ID)

	// 5. GetQueuedMessage by ID
	fetched, err := repo.GetQueuedMessage(chatID, msg1.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, msg1.Prompt, fetched.Prompt)

	// 6. UpdateQueuedMessage
	updated, err := repo.UpdateQueuedMessage(chatID, msg1.ID, "Updated prompt content")
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, "Updated prompt content", updated.Prompt)

	// Verify update in DB
	fetchedUpdated, err := repo.GetQueuedMessage(chatID, msg1.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated prompt content", fetchedUpdated.Prompt)

	// Update non-existent returns ErrQueuedMessageNotFound
	_, err = repo.UpdateQueuedMessage(chatID, "non-existent-id", "new prompt")
	assert.ErrorIs(t, err, ErrQueuedMessageNotFound)

	// 7. Delete single message
	err = repo.DeleteQueuedMessage(chatID, msg1.ID)
	require.NoError(t, err)

	// Verify deletion
	_, err = repo.GetQueuedMessage(chatID, msg1.ID)
	assert.ErrorIs(t, err, ErrQueuedMessageNotFound)

	msgs, err = repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, msg2.ID, msgs[0].ID)

	// Delete non-existent returns ErrQueuedMessageNotFound
	err = repo.DeleteQueuedMessage(chatID, msg1.ID)
	assert.ErrorIs(t, err, ErrQueuedMessageNotFound)
}

func TestQueuedMessage_PeekNextQueuedMessage(t *testing.T) {
	t.Parallel()
	repo, _ := setupTestQueueRepo(t)
	chatID := "chat-peek-1"

	// Empty queue peek returns nil, nil
	peeked, err := repo.PeekNextQueuedMessage(chatID)
	require.NoError(t, err)
	assert.Nil(t, peeked)

	// Enqueue 2 messages
	msg1, err := repo.EnqueueMessage(chatID, "msg-1", "m1")
	require.NoError(t, err)
	msg2, err := repo.EnqueueMessage(chatID, "msg-2", "m2")
	require.NoError(t, err)

	// Peek repeatedly: should return first message without removing it
	for i := 0; i < 3; i++ {
		p, err := repo.PeekNextQueuedMessage(chatID)
		require.NoError(t, err)
		require.NotNil(t, p)
		assert.Equal(t, msg1.ID, p.ID)
		assert.Equal(t, "msg-1", p.Prompt)
	}

	msgs, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Len(t, msgs, 2)

	// Pop msg1
	popped, err := repo.PopNextQueuedMessage(chatID)
	require.NoError(t, err)
	require.NotNil(t, popped)
	assert.Equal(t, msg1.ID, popped.ID)

	// Next peek returns msg2
	p2, err := repo.PeekNextQueuedMessage(chatID)
	require.NoError(t, err)
	require.NotNil(t, p2)
	assert.Equal(t, msg2.ID, p2.ID)
}

func TestQueuedMessage_CapacityLimit(t *testing.T) {
	t.Parallel()
	repo, _ := setupTestQueueRepo(t)
	chatID := "chat-cap-1"

	// Enqueue up to MaxQueuedMessagesPerSession (3)
	for i := 1; i <= MaxQueuedMessagesPerSession; i++ {
		msg, err := repo.EnqueueMessage(chatID, fmt.Sprintf("prompt-%d", i), "model")
		require.NoError(t, err)
		require.NotNil(t, msg)
	}

	// 4th enqueue should fail with ErrQueueFull
	msg4, err := repo.EnqueueMessage(chatID, "prompt-4", "model")
	assert.ErrorIs(t, err, ErrQueueFull)
	assert.Nil(t, msg4)

	// Verify total count is still 3
	msgs, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Len(t, msgs, 3)

	// Delete 1 message
	err = repo.DeleteQueuedMessage(chatID, msgs[0].ID)
	require.NoError(t, err)

	// Enqueue another message succeeds now
	msgAfterDelete, err := repo.EnqueueMessage(chatID, "prompt-after-delete", "model")
	require.NoError(t, err)
	require.NotNil(t, msgAfterDelete)

	msgs, err = repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Len(t, msgs, 3)
}

func TestQueuedMessage_FIFOOrderingAndPop(t *testing.T) {
	t.Parallel()
	repo, _ := setupTestQueueRepo(t)
	chatID := "chat-fifo-1"

	// Pop on empty queue returns nil, nil
	poppedEmpty, err := repo.PopNextQueuedMessage(chatID)
	require.NoError(t, err)
	assert.Nil(t, poppedEmpty)

	// Insert messages with explicit timestamps to test created_at ASC, id ASC tie breaking
	sameTime := time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC)

	// Direct DB creation to test tie-breaker on identical created_at
	tieMsgB := QueuedMessage{
		ID:        "qmsg-bbb",
		ChatID:    chatID,
		Prompt:    "Prompt B",
		CreatedAt: sameTime,
		UpdatedAt: sameTime,
	}
	tieMsgA := QueuedMessage{
		ID:        "qmsg-aaa",
		ChatID:    chatID,
		Prompt:    "Prompt A",
		CreatedAt: sameTime,
		UpdatedAt: sameTime,
	}
	laterMsg := QueuedMessage{
		ID:        "qmsg-ccc",
		ChatID:    chatID,
		Prompt:    "Prompt C",
		CreatedAt: sameTime.Add(time.Second),
		UpdatedAt: sameTime.Add(time.Second),
	}

	require.NoError(t, repo.db.Create(&tieMsgB).Error)
	require.NoError(t, repo.db.Create(&tieMsgA).Error)
	require.NoError(t, repo.db.Create(&laterMsg).Error)

	// FIFO pop 1: should be tieMsgA (same timestamp, "qmsg-aaa" < "qmsg-bbb")
	p1, err := repo.PopNextQueuedMessage(chatID)
	require.NoError(t, err)
	require.NotNil(t, p1)
	assert.Equal(t, "qmsg-aaa", p1.ID)

	// FIFO pop 2: should be tieMsgB
	p2, err := repo.PopNextQueuedMessage(chatID)
	require.NoError(t, err)
	require.NotNil(t, p2)
	assert.Equal(t, "qmsg-bbb", p2.ID)

	// FIFO pop 3: should be laterMsg
	p3, err := repo.PopNextQueuedMessage(chatID)
	require.NoError(t, err)
	require.NotNil(t, p3)
	assert.Equal(t, "qmsg-ccc", p3.ID)

	// Now empty
	p4, err := repo.PopNextQueuedMessage(chatID)
	require.NoError(t, err)
	assert.Nil(t, p4)
}

func TestQueuedMessage_GetChatIDsWithQueuedMessages(t *testing.T) {
	t.Parallel()
	repo, _ := setupTestQueueRepo(t)

	// Initially empty
	chatIDs, err := repo.GetChatIDsWithQueuedMessages()
	require.NoError(t, err)
	assert.Empty(t, chatIDs)

	// Enqueue messages across chats
	_, err = repo.EnqueueMessage("chat-A", "prompt A1", "")
	require.NoError(t, err)
	_, err = repo.EnqueueMessage("chat-A", "prompt A2", "")
	require.NoError(t, err)
	_, err = repo.EnqueueMessage("chat-B", "prompt B1", "")
	require.NoError(t, err)

	chatIDs, err = repo.GetChatIDsWithQueuedMessages()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"chat-A", "chat-B"}, chatIDs)

	// Clear chat-A
	_, err = repo.ClearQueuedMessages("chat-A")
	require.NoError(t, err)

	chatIDs, err = repo.GetChatIDsWithQueuedMessages()
	require.NoError(t, err)
	assert.Equal(t, []string{"chat-B"}, chatIDs)
}

func TestQueuedMessage_ClearQueuedMessages(t *testing.T) {
	t.Parallel()
	repo, _ := setupTestQueueRepo(t)
	chatID := "chat-clear-1"

	_, err := repo.EnqueueMessage(chatID, "msg1", "")
	require.NoError(t, err)
	_, err = repo.EnqueueMessage(chatID, "msg2", "")
	require.NoError(t, err)

	deleted, err := repo.ClearQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)

	msgs, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Empty(t, msgs)

	// Clear empty returns 0
	deleted, err = repo.ClearQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)
}

func TestQueuedMessage_CascadeOnDeleteSession(t *testing.T) {
	t.Parallel()
	repo, _ := setupTestQueueRepo(t)
	chatID := "chat-cascade-1"

	// Create session
	err := repo.SaveSession(&Session{
		ChatID:    chatID,
		Title:     "Cascade test session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Enqueue 2 messages
	_, err = repo.EnqueueMessage(chatID, "q1", "")
	require.NoError(t, err)
	_, err = repo.EnqueueMessage(chatID, "q2", "")
	require.NoError(t, err)

	msgs, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Len(t, msgs, 2)

	// Delete session
	err = repo.DeleteSession(chatID)
	require.NoError(t, err)

	// Verify session is deleted
	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.Nil(t, sess)

	// Verify queued messages were cascade deleted
	msgs, err = repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Empty(t, msgs)
}
