package cleanup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

func TestSchedulerCleanExpiredSessions(t *testing.T) {
	dbConn := db.NewDBForTest(t)

	err := dbmodels.AutoMigrate(dbConn)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(dbConn)

	tmpBase := t.TempDir()
	sessionBase := filepath.Join(filepath.Dir(tmpBase), "session")
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(sessionBase, chatID)
	})

	scheduler, err := NewScheduler(repo, WithTmpBase(tmpBase))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = scheduler.Shutdown()
	})

	expiredID := "sched-expired-session"
	recentID := "sched-recent-session"
	runningID := "sched-running-session"
	orphanID := "sched-orphan-session"

	expiredTmpDir := filepath.Join(tmpBase, expiredID)
	recentTmpDir := filepath.Join(tmpBase, recentID)
	runningTmpDir := filepath.Join(tmpBase, runningID)
	orphanTmpDir := filepath.Join(tmpBase, orphanID)

	require.NoError(t, os.MkdirAll(expiredTmpDir, 0755))
	require.NoError(t, os.MkdirAll(recentTmpDir, 0755))
	require.NoError(t, os.MkdirAll(runningTmpDir, 0755))
	require.NoError(t, os.MkdirAll(orphanTmpDir, 0755))

	now := time.Now()
	expiredTime := now.AddDate(0, -1, -2)
	expiredSess := &dbmodels.Session{
		ChatID:    expiredID,
		Title:     "Expired Session",
		CreatedAt: expiredTime,
		UpdatedAt: expiredTime,
	}
	recentSess := &dbmodels.Session{
		ChatID:    recentID,
		Title:     "Recent Session",
		CreatedAt: now,
		UpdatedAt: now,
	}
	runningSess := &dbmodels.Session{
		ChatID:    runningID,
		Title:     "Running Session",
		Agents:    dbmodels.Agents{dbmodels.Agent{Name: "agent-1", Status: dbmodels.AgentStatusRunning}},
		CreatedAt: expiredTime,
		UpdatedAt: expiredTime,
	}

	require.NoError(t, repo.SaveSession(expiredSess))
	require.NoError(t, repo.SaveSession(recentSess))
	require.NoError(t, repo.SaveSession(runningSess))

	require.NoError(t, dbConn.Exec("UPDATE sessions SET updated_at = ? WHERE chat_id = ?", expiredTime, expiredID).Error)
	require.NoError(t, dbConn.Exec("UPDATE sessions SET updated_at = ? WHERE chat_id = ?", expiredTime, runningID).Error)

	require.NoError(t, os.Chtimes(orphanTmpDir, expiredTime, expiredTime))

	// Manually trigger CleanExpiredSessions
	scheduler.CleanExpiredSessions()

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

func TestNewScheduler_NilRepo(t *testing.T) {
	_, err := NewScheduler(nil)
	assert.Error(t, err)
}
