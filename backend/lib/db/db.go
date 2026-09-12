package db

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/libtnb/sqlite"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

func gormConfig(prepareStmt bool) *gorm.Config {
	return &gorm.Config{
		Logger: NewLogger(
			WithDefaultLogLevel(zerolog.InfoLevel),
			WithSlowThreshold(200*time.Millisecond),
			WithLogParams(),
			WithIgnoreNotFoundError(),
		),
		PrepareStmt: prepareStmt,
	}
}

// newSQLiteDB opens a SQLite database at dsn with Asgard's standard pragmas.
func newSQLiteDB(dsn string) (*gorm.DB, error) {
	if !strings.Contains(dsn, "busy_timeout") && !strings.Contains(dsn, "_timeout") {
		separator := "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
		dsn = fmt.Sprintf("%s%s_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=_txlock(immediate)&_busy_timeout=10000&_journal_mode=WAL&_sync=NORMAL", dsn, separator)
	}
	db, err := gorm.Open(sqlite.Open(dsn), gormConfig(false))
	if err != nil {
		return nil, err
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	return db, nil
}

// NewDB opens the configured database. SQLite always lives at the fixed
// ~/asgard/data/data.db path; only the Postgres connection string is
// configurable.
func NewDB(conf *config.Config) (*gorm.DB, error) {
	if conf.DB == "sqlite" {
		return newSQLiteDB(paths.DBFile())
	}
	return gorm.Open(postgres.Open(conf.DSN), gormConfig(true))
}

func NewDBForTest(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := newSQLiteDB(":memory:")
	require.NoError(t, err)
	return db
}
