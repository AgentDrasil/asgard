package dbmodels

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/db"
)

func TestCleanExpiredSessions(t *testing.T) {
	dbConn := db.NewDBForTest(t)

	err := AutoMigrate(dbConn)
	require.NoError(t, err)

	repo := NewSessionRepository(dbConn)

	// Use t.TempDir() as isolated tmp base
	tmpBase := t.TempDir()

	expiredID := "test-expired-session-id"
	recentID := "test-recent-session-id"
	runningID := "test-running-session-id"
	orphanID := "test-orphan-session-id"

	expiredTmpDir := filepath.Join(tmpBase, expiredID)
	recentTmpDir := filepath.Join(tmpBase, recentID)
	runningTmpDir := filepath.Join(tmpBase, runningID)
	orphanTmpDir := filepath.Join(tmpBase, orphanID)

	require.NoError(t, os.MkdirAll(expiredTmpDir, 0755))
	require.NoError(t, os.MkdirAll(recentTmpDir, 0755))
	require.NoError(t, os.MkdirAll(runningTmpDir, 0755))
	require.NoError(t, os.MkdirAll(orphanTmpDir, 0755))

	now := time.Now()
	expiredTime := now.AddDate(0, -1, -1) // > 1 month ago

	expiredSess := &Session{
		ChatID:    expiredID,
		Title:     "Expired Session",
		UpdatedAt: expiredTime,
	}
	recentSess := &Session{
		ChatID:    recentID,
		Title:     "Recent Session",
		UpdatedAt: now,
	}
	runningSess := &Session{
		ChatID:    runningID,
		Title:     "Running Session",
		Agents:    Agents{Agent{Name: "agent-1", Status: AgentStatusRunning}},
		UpdatedAt: expiredTime,
	}

	require.NoError(t, repo.SaveSession(expiredSess))
	require.NoError(t, repo.SaveSession(recentSess))
	require.NoError(t, repo.SaveSession(runningSess))

	// Explicitly set updated_at in DB
	require.NoError(t, dbConn.Exec("UPDATE sessions SET updated_at = ? WHERE chat_id = ?", expiredTime, expiredID).Error)
	require.NoError(t, dbConn.Exec("UPDATE sessions SET updated_at = ? WHERE chat_id = ?", expiredTime, runningID).Error)

	// Set orphan directory & content modification times to expiredTime
	require.NoError(t, os.Chtimes(orphanTmpDir, expiredTime, expiredTime))

	cutoff := now.AddDate(0, -1, 0)
	err = repo.CleanExpiredSessions(CleanExpiredSessionsOptions{
		Cutoff:  cutoff,
		TmpBase: tmpBase,
	})
	require.NoError(t, err)

	// Verify DB state
	sExpired, err := repo.GetSession(expiredID)
	require.NoError(t, err)
	assert.Nil(t, sExpired)

	sRecent, err := repo.GetSession(recentID)
	require.NoError(t, err)
	assert.NotNil(t, sRecent)

	sRunning, err := repo.GetSession(runningID)
	require.NoError(t, err)
	assert.NotNil(t, sRunning, "Running session should not be deleted")

	// Verify tmp directory state
	_, errExpiredDir := os.Stat(expiredTmpDir)
	assert.True(t, os.IsNotExist(errExpiredDir))

	_, errOrphanDir := os.Stat(orphanTmpDir)
	assert.True(t, os.IsNotExist(errOrphanDir))

	_, errRecentDir := os.Stat(recentTmpDir)
	assert.False(t, os.IsNotExist(errRecentDir))

	_, errRunningDir := os.Stat(runningTmpDir)
	assert.False(t, os.IsNotExist(errRunningDir))
}
