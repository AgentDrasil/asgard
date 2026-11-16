package dbmodels

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatRouteSetReplaceDeleteAndCap(t *testing.T) {
	repo, _ := setupTestQueueRepo(t)
	replaced, err := repo.SetRoute("c", "design", "x", "n1", 2)
	require.NoError(t, err)
	assert.False(t, replaced)
	replaced, err = repo.SetRoute("c", "design", "y", "n2", 2)
	require.NoError(t, err)
	assert.True(t, replaced)
	_, err = repo.SetRoute("c", "build", "x", "", 2)
	require.NoError(t, err)
	_, err = repo.SetRoute("c", "scope", "x", "", 2)
	assert.ErrorIs(t, err, ErrRouteLimit)
	_, err = repo.SetRoute("c", "build", "z", "", 2)
	require.NoError(t, err, "replacing at the cap")

	require.NoError(t, repo.DeleteRoute("c", "build"))
	assert.ErrorIs(t, repo.DeleteRoute("c", "build"), ErrRouteNotFound)
}

func TestEffectiveRoutesNearestWins(t *testing.T) {
	repo, _ := setupTestQueueRepo(t)
	saveSpawnChat(t, repo, "root", "")
	saveSpawnChat(t, repo, "mid", "root")
	saveSpawnChat(t, repo, "leaf", "mid")
	saveSpawnChat(t, repo, "sib", "root")
	for _, r := range [][3]string{{"root", "design", "r"}, {"mid", "design", "m"}, {"root", "scope", "r2"}, {"sib", "build", "s"}} {
		_, err := repo.SetRoute(r[0], r[1], r[2], "", 32)
		require.NoError(t, err)
	}
	routes, root, err := repo.EffectiveRoutes("leaf")
	require.NoError(t, err)
	assert.Equal(t, "root", root)
	var got []string
	for _, r := range routes {
		got = append(got, r.Topic+"="+r.TargetChatID+"@"+r.DefinedAt)
	}
	assert.Equal(t, []string{"design=m@mid", "scope=r2@root"}, got)

	routes, root, err = repo.EffectiveRoutes("missing")
	require.NoError(t, err)
	assert.Empty(t, root)
	assert.Empty(t, routes)
}

func TestEffectiveRoutesSurvivesCycle(t *testing.T) {
	repo, _ := setupTestQueueRepo(t)
	saveSpawnChat(t, repo, "a", "b")
	saveSpawnChat(t, repo, "b", "a")
	_, err := repo.SetRoute("b", "t", "a", "", 32)
	require.NoError(t, err)
	routes, root, err := repo.EffectiveRoutes("a")
	require.NoError(t, err)
	assert.NotEmpty(t, root)
	assert.Len(t, routes, 1)
}

func TestDeleteSessionDropsRoutes(t *testing.T) {
	repo, _ := setupTestQueueRepo(t)
	saveSpawnChat(t, repo, "root", "")
	saveSpawnChat(t, repo, "kid", "root")
	_, err := repo.SetRoute("root", "design", "kid", "", 32)
	require.NoError(t, err)
	_, err = repo.SetRoute("kid", "user", "kid", "", 32)
	require.NoError(t, err)
	require.NoError(t, repo.DeleteSession("kid"))
	routes, _, err := repo.EffectiveRoutes("root")
	require.NoError(t, err)
	assert.Empty(t, routes)
	var n int64
	require.NoError(t, repo.db.Model(&ChatRoute{}).Count(&n).Error)
	assert.Zero(t, n)
}
