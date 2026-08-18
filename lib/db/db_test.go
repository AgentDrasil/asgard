package db

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/config"
)

func TestNewDB_SQLiteConfig(t *testing.T) {
	t.Parallel()

	t.Run("memory sqlite", func(t *testing.T) {
		t.Parallel()
		db, err := NewDB(&config.Config{
			DB:  "sqlite",
			DSN: ":memory:",
		})
		require.NoError(t, err)
		assert.NotNil(t, db)

		sqlDB, err := db.DB()
		require.NoError(t, err)
		assert.Equal(t, 1, sqlDB.Stats().MaxOpenConnections)
	})

	t.Run("file sqlite", func(t *testing.T) {
		t.Parallel()
		dbPath := filepath.Join(t.TempDir(), "test.db")
		db, err := NewDB(&config.Config{
			DB:  "sqlite",
			DSN: dbPath,
		})
		require.NoError(t, err)
		assert.NotNil(t, db)

		sqlDB, err := db.DB()
		require.NoError(t, err)
		assert.Equal(t, 4, sqlDB.Stats().MaxOpenConnections)
	})
}
