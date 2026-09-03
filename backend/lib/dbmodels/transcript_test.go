package dbmodels

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranscript_AppendAndRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	msg1 := ChatMessage{ID: "m1", Role: "user", Content: "hello"}
	appended, err := AppendMessage(dir, msg1)
	require.NoError(t, err)
	assert.True(t, appended)

	msg2 := ChatMessage{ID: "m2", Role: "assistant", Content: "world"}
	appended, err = AppendMessage(dir, msg2)
	require.NoError(t, err)
	assert.True(t, appended)

	// Append message with empty ID
	msg3 := ChatMessage{Role: "user", Content: "no id message"}
	appended, err = AppendMessage(dir, msg3)
	require.NoError(t, err)
	assert.True(t, appended)

	readMsgs, err := ReadMessages(dir)
	require.NoError(t, err)
	require.Len(t, readMsgs, 3)
	assert.Equal(t, "m1", readMsgs[0].ID)
	assert.Equal(t, "hello", readMsgs[0].Content)
	assert.Equal(t, "m2", readMsgs[1].ID)
	assert.Equal(t, "world", readMsgs[1].Content)
	assert.Equal(t, "", readMsgs[2].ID)
	assert.Equal(t, "no id message", readMsgs[2].Content)
}

func TestTranscript_UpdateInPlace_InheritReplied(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Initial message
	msg := ChatMessage{
		ID:      "ask-1",
		Role:    "ask_user",
		Content: "Can you confirm?",
	}
	appended, err := AppendMessage(dir, msg)
	require.NoError(t, err)
	assert.True(t, appended)

	// Mark as replied
	updated, hasUnreplied, err := MarkAskUserReplied(dir, "ask-1", "yes I confirm")
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.True(t, updated.Replied)
	assert.Equal(t, "yes I confirm", updated.ReplyText)
	assert.False(t, hasUnreplied)

	// In-place replace: new message with same ID but Replied=false
	newMsg := ChatMessage{
		ID:      "ask-1",
		Role:    "ask_user",
		Content: "Can you confirm updated?",
		Replied: false,
	}
	appended, err = AppendMessage(dir, newMsg)
	require.NoError(t, err)
	assert.False(t, appended, "in-place replace should return appended = false")

	// Verify the replied state was inherited
	readMsgs, err := ReadMessages(dir)
	require.NoError(t, err)
	require.Len(t, readMsgs, 1)
	assert.Equal(t, "ask-1", readMsgs[0].ID)
	assert.Equal(t, "Can you confirm updated?", readMsgs[0].Content)
	assert.True(t, readMsgs[0].Replied, "replied state must be inherited")
	assert.Equal(t, "yes I confirm", readMsgs[0].ReplyText)
}

func TestTranscript_MarkAskUserReplied_ThreeLevelFallback(t *testing.T) {
	t.Parallel()

	t.Run("Priority1_ExactMatchByID", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		// Add multiple messages including non-ask_user and replied
		require.NoError(t, func() error {
			_, err := AppendMessage(dir, ChatMessage{ID: "u1", Role: "user", Content: "user prompt"})
			return err
		}())
		require.NoError(t, func() error {
			_, err := AppendMessage(dir, ChatMessage{ID: "ask1", Role: "ask_user", Content: "q1"})
			return err
		}())
		require.NoError(t, func() error {
			_, err := AppendMessage(dir, ChatMessage{ID: "ask2", Role: "ask_user", Content: "q2"})
			return err
		}())

		// Exact match by ID on u1 (even though role is user)
		updated, hasUnreplied, err := MarkAskUserReplied(dir, "u1", "user replied")
		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, "u1", updated.ID)
		assert.True(t, updated.Replied)
		assert.Equal(t, "user replied", updated.ReplyText)
		assert.True(t, hasUnreplied, "ask1 and ask2 are still unreplied")
	})

	t.Run("Priority2_EmptyID_MatchesFirstUnrepliedAskUser", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		require.NoError(t, func() error {
			_, err := AppendMessage(dir, ChatMessage{ID: "ask1", Role: "ask_user", Content: "question 1"})
			return err
		}())
		require.NoError(t, func() error {
			_, err := AppendMessage(dir, ChatMessage{ID: "ask2", Role: "ask_user", Content: "question 2"})
			return err
		}())

		// messageID == "" should match first unreplied ask_user (ask1)
		updated, hasUnreplied, err := MarkAskUserReplied(dir, "", "answer 1")
		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, "ask1", updated.ID)
		assert.True(t, updated.Replied)
		assert.Equal(t, "answer 1", updated.ReplyText)
		assert.True(t, hasUnreplied, "ask2 is still unreplied")

		// Next empty ID should match ask2
		updated2, hasUnreplied2, err := MarkAskUserReplied(dir, "", "answer 2")
		require.NoError(t, err)
		require.NotNil(t, updated2)
		assert.Equal(t, "ask2", updated2.ID)
		assert.False(t, hasUnreplied2, "all are replied now")
	})

	t.Run("Priority3_FallbackToLastUnrepliedAskUser", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		require.NoError(t, func() error {
			_, err := AppendMessage(dir, ChatMessage{ID: "ask1", Role: "ask_user", Content: "question 1"})
			return err
		}())
		require.NoError(t, func() error {
			_, err := AppendMessage(dir, ChatMessage{ID: "ask2", Role: "ask_user", Content: "question 2"})
			return err
		}())

		// Non-existent ID that doesn't match anything -> falls back to match last unreplied ask_user (ask2)
		updated, hasUnreplied, err := MarkAskUserReplied(dir, "non-existent-id", "fallback reply")
		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, "ask2", updated.ID)
		assert.True(t, updated.Replied)
		assert.Equal(t, "fallback reply", updated.ReplyText)
		assert.True(t, hasUnreplied, "ask1 is still unreplied")
	})
}

func TestTranscript_TornLineRecovery(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	msg1 := ChatMessage{ID: "m1", Role: "user", Content: "valid message 1"}
	msg2 := ChatMessage{ID: "m2", Role: "assistant", Content: "valid message 2"}
	_, err := AppendMessage(dir, msg1)
	require.NoError(t, err)
	_, err = AppendMessage(dir, msg2)
	require.NoError(t, err)

	// Inject torn/corrupt JSON line at end
	path := TranscriptFilePath(dir)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	_, err = f.WriteString("{\"id\":\"torn-msg\",\"role\":\"user\",\"conte\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// ReadMessages should recover valid messages and skip torn line
	messages, err := ReadMessages(dir)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "m1", messages[0].ID)
	assert.Equal(t, "m2", messages[1].ID)
}

func TestTranscript_ConcurrentAppend(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const count = 50

	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(idx int) {
			defer wg.Done()
			_, err := AppendMessage(dir, ChatMessage{
				Role:    "user",
				Content: fmt.Sprintf("message %d", idx),
			})
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()

	messages, err := ReadMessages(dir)
	require.NoError(t, err)
	assert.Len(t, messages, count)
}
