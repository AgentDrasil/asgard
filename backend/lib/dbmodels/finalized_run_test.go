package dbmodels

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
)

func TestFinalizedRunRepository_MarkFinalized(t *testing.T) {
	t.Parallel()

	testDB := db.NewDBForTest(t)
	require.NoError(t, AutoMigrate(testDB))
	repo := NewFinalizedRunRepository(testDB)

	first, err := repo.MarkFinalized(context.Background(), "run-1")
	require.NoError(t, err)
	assert.True(t, first)

	again, err := repo.MarkFinalized(context.Background(), "run-1")
	require.NoError(t, err)
	assert.False(t, again)

	other, err := repo.MarkFinalized(context.Background(), "run-2")
	require.NoError(t, err)
	assert.True(t, other)
}
