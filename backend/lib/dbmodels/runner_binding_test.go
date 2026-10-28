package dbmodels

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
)

func newBindingRepo(t *testing.T) *SessionRepository {
	t.Helper()
	testDB := db.NewDBForTest(t)
	require.NoError(t, AutoMigrate(testDB))
	repo := NewSessionRepository(testDB)
	dir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string { return filepath.Join(dir, chatID) })
	return repo
}

func TestBindRunner_SetOnceAndNeverChanged(t *testing.T) {
	repo := newBindingRepo(t)

	bound, err := repo.BindRunner("chat-1", "r1")
	require.NoError(t, err)
	assert.Equal(t, "r1", bound, "a new chat is created bound")

	bound, err = repo.BindRunner("chat-1", "r2")
	require.NoError(t, err)
	assert.Equal(t, "r1", bound, "an existing binding wins")

	// An existing unbound session gets bound once.
	require.NoError(t, repo.UpsertSession(&Session{ChatID: "chat-2", CurrentAgent: "a"}))
	bound, err = repo.BindRunner("chat-2", "r2")
	require.NoError(t, err)
	assert.Equal(t, "r2", bound)
	sess, err := repo.GetSession("chat-2")
	require.NoError(t, err)
	assert.Equal(t, "r2", sess.RunnerNodeID)
	assert.Equal(t, "a", sess.CurrentAgent)
}

func TestUpsertSession_KeepsBindingAndFillsEmptyOne(t *testing.T) {
	repo := newBindingRepo(t)
	require.NoError(t, repo.UpsertSession(&Session{ChatID: "c", CurrentAgent: "a", RunnerNodeID: "r1"}))
	require.NoError(t, repo.UpsertSession(&Session{ChatID: "c", CurrentAgent: "a", RunnerNodeID: "r2"}))
	sess, err := repo.GetSession("c")
	require.NoError(t, err)
	assert.Equal(t, "r1", sess.RunnerNodeID)

	require.NoError(t, repo.UpsertSession(&Session{ChatID: "d", CurrentAgent: "a"}))
	require.NoError(t, repo.UpsertSession(&Session{ChatID: "d", CurrentAgent: "a", RunnerNodeID: "r2"}))
	sess, err = repo.GetSession("d")
	require.NoError(t, err)
	assert.Equal(t, "r2", sess.RunnerNodeID)
}

func TestBackfillRunnerNodeID(t *testing.T) {
	testDB := db.NewDBForTest(t)
	// A pre-binding schema: the sessions table without runner_node_id.
	require.NoError(t, testDB.Exec(`CREATE TABLE sessions (chat_id VARCHAR(64) PRIMARY KEY, current_agent TEXT)`).Error)
	require.NoError(t, testDB.Exec(`INSERT INTO sessions (chat_id, current_agent) VALUES ('old-1', 'a'), ('old-2', 'a'), ('wf-cron-nightly', 'wf')`).Error)

	require.NoError(t, AutoMigrate(testDB))
	repo := NewSessionRepository(testDB)
	dir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string { return filepath.Join(dir, chatID) })

	_, err := repo.BindRunner("bound", "other")
	require.NoError(t, err)

	require.NoError(t, BackfillRunnerNodeID(testDB, "first"))
	require.NoError(t, BackfillRunnerNodeID(testDB, "second"), "backfill is idempotent")

	for id, want := range map[string]string{"old-1": "first", "old-2": "first", "bound": "other"} {
		sess, err := repo.GetSession(id)
		require.NoError(t, err)
		require.NotNil(t, sess, id)
		assert.Equal(t, want, sess.RunnerNodeID, id)
	}
	cron, err := repo.GetSession("wf-cron-nightly")
	require.NoError(t, err)
	assert.Empty(t, cron.RunnerNodeID, "cron sessions take their runner from the definition, not the backfill")
	require.NoError(t, BackfillRunnerNodeID(testDB, ""), "no configured runner is a no-op")
}

func TestChatIDsForRunnerWithQueue(t *testing.T) {
	repo := newBindingRepo(t)
	for chat, runner := range map[string]string{"a": "r1", "b": "r1", "c": "r2"} {
		_, err := repo.BindRunner(chat, runner)
		require.NoError(t, err)
	}
	_, err := repo.EnqueueMessage("a", "p", "")
	require.NoError(t, err)
	_, err = repo.EnqueueMessage("c", "p", "")
	require.NoError(t, err)

	ids, err := repo.ChatIDsForRunnerWithQueue("r1")
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, ids)
}

func TestRequeueFrontRestoresHead(t *testing.T) {
	repo := newBindingRepo(t)
	first, err := repo.EnqueueMessage("c", "first", "m")
	require.NoError(t, err)
	_, err = repo.EnqueueMessage("c", "second", "")
	require.NoError(t, err)

	popped, err := repo.PopNextQueuedMessage("c")
	require.NoError(t, err)
	require.Equal(t, first.ID, popped.ID)
	require.NoError(t, repo.RequeueFront(popped))

	head, err := repo.PeekNextQueuedMessage("c")
	require.NoError(t, err)
	assert.Equal(t, first.ID, head.ID)
	assert.Equal(t, "m", head.Model)
}
