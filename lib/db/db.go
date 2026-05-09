package db

import (
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
		return gorm.Open(sqlite.Open(conf.DSN), config)
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
