package dbmodels

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func saveSpawnChat(t *testing.T, repo *SessionRepository, id, parent string) {
	t.Helper()
	require.NoError(t, repo.SaveSession(&Session{ChatID: id, CurrentAgent: "a", ParentChatID: parent}))
}

func TestSpawnRootIDAndTree(t *testing.T) {
	repo, _ := setupTestQueueRepo(t)
	saveSpawnChat(t, repo, "root", "")
	saveSpawnChat(t, repo, "c1", "root")
	saveSpawnChat(t, repo, "c2", "root")
	saveSpawnChat(t, repo, "g1", "c1")
	saveSpawnChat(t, repo, "other", "")
	saveSpawnChat(t, repo, "other-kid", "other")
	saveSpawnChat(t, repo, "orphan", "gone")
	saveSpawnChat(t, repo, "orphan-kid", "orphan")

	for id, want := range map[string]string{"root": "root", "c1": "root", "g1": "root", "other-kid": "other", "orphan-kid": "orphan", "orphan": "orphan"} {
		got, err := repo.SpawnRootID(id)
		require.NoError(t, err)
		assert.Equal(t, want, got, id)
	}

	sessions, truncated, err := repo.SpawnTreeSessions("root", 100)
	require.NoError(t, err)
	assert.False(t, truncated)
	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.ChatID)
	}
	assert.ElementsMatch(t, []string{"root", "c1", "c2", "g1"}, ids)

	sessions, truncated, err = repo.SpawnTreeSessions("root", 2)
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.Len(t, sessions, 2)

	sessions, _, err = repo.SpawnTreeSessions("other", 100)
	require.NoError(t, err)
	assert.Len(t, sessions, 2)
}

func TestSpawnTreeSurvivesCycle(t *testing.T) {
	repo, _ := setupTestQueueRepo(t)
	saveSpawnChat(t, repo, "a", "b")
	saveSpawnChat(t, repo, "b", "a")
	root, err := repo.SpawnRootID("a")
	require.NoError(t, err)
	assert.NotEmpty(t, root)
	sessions, _, err := repo.SpawnTreeSessions("a", 100)
	require.NoError(t, err)
	assert.Len(t, sessions, 2, fmt.Sprint(sessions))
}
