package dbmodels

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
)

func newAccessRepo(t *testing.T) *RunnerAccessRepository {
	t.Helper()
	testDB := db.NewDBForTest(t)
	require.NoError(t, AutoMigrate(testDB))
	return NewRunnerAccessRepository(testDB)
}

func TestRunnerAccess_NothingAssignedByDefault(t *testing.T) {
	t.Parallel()
	repo := newAccessRepo(t)

	ok, err := repo.IsAssigned("runner-a", "u1")
	require.NoError(t, err)
	assert.False(t, ok)

	got, err := repo.RunnersForUser("u1")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestRunnerAccess_UpsertUserKeepsFirstSeen(t *testing.T) {
	t.Parallel()
	repo := newAccessRepo(t)

	require.NoError(t, repo.UpsertUser("u1", "Old", "old@x.io"))
	first, err := repo.ListUsers()
	require.NoError(t, err)
	require.Len(t, first, 1)

	require.NoError(t, repo.UpsertUser("u1", "New", "new@x.io"))
	second, err := repo.ListUsers()
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, "New", second[0].Name)
	assert.True(t, first[0].FirstSeen.Equal(second[0].FirstSeen))

	require.Error(t, repo.UpsertUser("", "x", "y"))
}

func TestRunnerAccess_SetAssignments(t *testing.T) {
	t.Parallel()
	repo := newAccessRepo(t)

	added, removed, err := repo.SetAssignments("runner-a", []string{"u1", "u2", " "}, "admin")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1", "u2"}, added)
	assert.Empty(t, removed)

	added, removed, err = repo.SetAssignments("runner-a", []string{"u2", "u3"}, "admin")
	require.NoError(t, err)
	assert.Equal(t, []string{"u3"}, added)
	assert.Equal(t, []string{"u1"}, removed)

	byNode, err := repo.AssignmentsByNode()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u2", "u3"}, byNode["runner-a"])

	ok, err := repo.IsAssigned("runner-a", "u1")
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = repo.IsAssigned("runner-a", "u3")
	require.NoError(t, err)
	assert.True(t, ok)

	got, err := repo.RunnersForUser("u2")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"runner-a": true}, got)
}

func TestRunnerAccess_AssignUnassignIdempotent(t *testing.T) {
	t.Parallel()
	repo := newAccessRepo(t)

	require.NoError(t, repo.Assign("runner-a", "u1", "admin"))
	require.NoError(t, repo.Assign("runner-a", "u1", "admin"))
	require.ErrorContains(t, repo.Assign("", "u1", "admin"), "required")

	require.NoError(t, repo.Unassign("runner-a", "u1"))
	require.NoError(t, repo.Unassign("runner-a", "u1"))
	ok, err := repo.IsAssigned("runner-a", "u1")
	require.NoError(t, err)
	assert.False(t, ok)
}
