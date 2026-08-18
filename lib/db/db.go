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

	"github.com/AgentDrasil/asgard/lib/config"
)

func NewDB(conf *config.Config) (*gorm.DB, error) {
	newLogger := NewLogger(
		WithDefaultLogLevel(zerolog.InfoLevel),
		WithSlowThreshold(200*time.Millisecond),
		WithLogParams(),
		WithIgnoreNotFoundError(),
	)

	config := &gorm.Config{
		Logger:      newLogger,
		PrepareStmt: true,
	}

	if conf.DB == "sqlite" {
		dsn := conf.DSN
		if !strings.Contains(dsn, "_busy_timeout") && !strings.Contains(dsn, "_timeout") {
			separator := "?"
			if strings.Contains(dsn, "?") {
				separator = "&"
			}
			dsn = fmt.Sprintf("%s%s_busy_timeout=10000&_journal_mode=WAL&_sync=NORMAL", dsn, separator)
		}
		db, err := gorm.Open(sqlite.Open(dsn), config)
		if err != nil {
			return nil, err
		}
		if sqlDB, err := db.DB(); err == nil {
			if strings.Contains(conf.DSN, ":memory:") {
				sqlDB.SetMaxOpenConns(1)
			} else {
				sqlDB.SetMaxOpenConns(4)
			}
		}
		return db, nil
	} else { // pg
		return gorm.Open(postgres.Open(conf.DSN), config)
	}
}

func NewDBForTest(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := NewDB(&config.Config{
		DB:  "sqlite",
		DSN: ":memory:",
	})
	require.NoError(t, err)
	return db
}
