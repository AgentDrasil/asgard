package db

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/backend/lib/config"
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
		assert.Equal(t, 1, sqlDB.Stats().MaxOpenConnections)

		var busyTimeout int
		err = db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error
		require.NoError(t, err)
		assert.Equal(t, 10000, busyTimeout)

		var journalMode string
		err = db.Raw("PRAGMA journal_mode").Scan(&journalMode).Error
		require.NoError(t, err)
		assert.Equal(t, "wal", journalMode)
		assert.False(t, db.Config.PrepareStmt) //nolint:staticcheck // gorm.DB.Config is not embedded; selector is intentional
	})

	t.Run("concurrent writes", func(t *testing.T) {
		t.Parallel()
		dbPath := filepath.Join(t.TempDir(), "concurrent.db")
		db, err := NewDB(&config.Config{
			DB:  "sqlite",
			DSN: dbPath,
		})
		require.NoError(t, err)

		type TestModel struct {
			ID   uint `gorm:"primaryKey"`
			Name string
		}
		require.NoError(t, db.AutoMigrate(&TestModel{}))

		const concurrency = 10
		errCh := make(chan error, concurrency)
		for i := 0; i < concurrency; i++ {
			go func(idx int) {
				errCh <- db.Transaction(func(tx *gorm.DB) error {
					return tx.Create(&TestModel{Name: "test"}).Error
				})
			}(i)
		}

		for i := 0; i < concurrency; i++ {
			require.NoError(t, <-errCh)
		}
	})

	t.Run("concurrent transactions and reads", func(t *testing.T) {
		t.Parallel()
		dbPath := filepath.Join(t.TempDir(), "concurrent_reads.db")
		db, err := NewDB(&config.Config{
			DB:  "sqlite",
			DSN: dbPath,
		})
		require.NoError(t, err)

		type Item struct {
			ID   uint `gorm:"primaryKey"`
			Name string
		}
		require.NoError(t, db.AutoMigrate(&Item{}))

		const workers = 10
		errCh := make(chan error, workers*2)

		// Concurrent transactions performing writes
		for i := 0; i < workers; i++ {
			go func(idx int) {
				errCh <- db.Transaction(func(tx *gorm.DB) error {
					return tx.Create(&Item{Name: "item"}).Error
				})
			}(i)
		}

		// Concurrent reads querying items
		for i := 0; i < workers; i++ {
			go func(idx int) {
				var items []Item
				errCh <- db.Where("name = ?", "item").Find(&items).Error
			}(i)
		}

		for i := 0; i < workers*2; i++ {
			require.NoError(t, <-errCh)
		}
	})
}
