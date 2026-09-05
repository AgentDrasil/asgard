package dbmodels

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
)

func TestCleanExpiredSessions(t *testing.T) {
	dbConn := db.NewDBForTest(t)

	err := AutoMigrate(dbConn)
	require.NoError(t, err)

	repo := NewSessionRepository(dbConn)

	// Use t.TempDir() as isolated tmp base
	tmpBase := t.TempDir()

	// Session base is derived as a sibling of the tmp base (~/data when tmp defaults to ~/tmp)
	sessionBase := filepath.Join(filepath.Dir(tmpBase), "data")
	t.Cleanup(func() { _ = os.RemoveAll(sessionBase) })
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(sessionBase, chatID)
	})
	repo.SetCABundleDirFunc(func(chatID string) string {
		return filepath.Join(tmpBase, ".asgard-ca", chatID)
	})

	expiredID := "test-expired-session-id"
	recentID := "test-recent-session-id"
	runningID := "test-running-session-id"
	orphanID := "test-orphan-session-id"

	expiredTmpDir := filepath.Join(tmpBase, expiredID)
	recentTmpDir := filepath.Join(tmpBase, recentID)
	runningTmpDir := filepath.Join(tmpBase, runningID)
	orphanTmpDir := filepath.Join(tmpBase, orphanID)
	expiredSessionDir := filepath.Join(sessionBase, expiredID)
	orphanSessionDir := filepath.Join(sessionBase, orphanID)

	caBase := filepath.Join(tmpBase, ".asgard-ca")
	expiredCABundleDir := filepath.Join(caBase, expiredID)
	orphanCABundleDir := filepath.Join(caBase, orphanID)
	recentCABundleDir := filepath.Join(caBase, recentID)

	require.NoError(t, os.MkdirAll(expiredTmpDir, 0755))
	require.NoError(t, os.MkdirAll(recentTmpDir, 0755))
	require.NoError(t, os.MkdirAll(runningTmpDir, 0755))
	require.NoError(t, os.MkdirAll(orphanTmpDir, 0755))
	require.NoError(t, os.MkdirAll(expiredSessionDir, 0755))
	require.NoError(t, os.MkdirAll(orphanSessionDir, 0755))
	for _, caDir := range []string{expiredCABundleDir, orphanCABundleDir, recentCABundleDir} {
		require.NoError(t, os.MkdirAll(caDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(caDir, "merged-ca-certificates.crt"), []byte("cert"), 0644))
	}

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
	require.NoError(t, os.Chtimes(orphanSessionDir, expiredTime, expiredTime))
	// Stale CA bundles for orphan and recent chats; container mtime also stale to
	// verify the whole .asgard-ca tree is never removed wholesale.
	require.NoError(t, os.Chtimes(caBase, expiredTime, expiredTime))
	require.NoError(t, os.Chtimes(orphanCABundleDir, expiredTime, expiredTime))
	require.NoError(t, os.Chtimes(filepath.Join(orphanCABundleDir, "merged-ca-certificates.crt"), expiredTime, expiredTime))
	require.NoError(t, os.Chtimes(recentCABundleDir, expiredTime, expiredTime))
	require.NoError(t, os.Chtimes(filepath.Join(recentCABundleDir, "merged-ca-certificates.crt"), expiredTime, expiredTime))

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

	// Verify session directory state
	_, errExpiredSessionDir := os.Stat(expiredSessionDir)
	assert.True(t, os.IsNotExist(errExpiredSessionDir))

	_, errOrphanSessionDir := os.Stat(orphanSessionDir)
	assert.True(t, os.IsNotExist(errOrphanSessionDir))

	_, errRecentDir := os.Stat(recentTmpDir)
	assert.False(t, os.IsNotExist(errRecentDir))

	_, errRunningDir := os.Stat(runningTmpDir)
	assert.False(t, os.IsNotExist(errRunningDir))

	// Verify CA bundle cleanup: expired + orphan bundles removed, recent chat's
	// bundle kept (its session still exists in DB), container dir preserved.
	_, errExpiredCA := os.Stat(expiredCABundleDir)
	assert.True(t, os.IsNotExist(errExpiredCA))

	_, errOrphanCA := os.Stat(orphanCABundleDir)
	assert.True(t, os.IsNotExist(errOrphanCA))

	_, errRecentCA := os.Stat(recentCABundleDir)
	assert.False(t, os.IsNotExist(errRecentCA), "CA bundle of an existing session must not be removed")

	_, errCABase := os.Stat(caBase)
	assert.False(t, os.IsNotExist(errCABase), "CA bundle container dir must not be removed wholesale")
}

func TestCleanExpiredSessions_CleansTranscriptAndWorkflowDirs(t *testing.T) {
	t.Parallel()

	dbConn := db.NewDBForTest(t)
	require.NoError(t, AutoMigrate(dbConn))

	repo := NewSessionRepository(dbConn)

	tmpBase := t.TempDir()
	sessionBase := filepath.Join(filepath.Dir(tmpBase), "data")
	t.Cleanup(func() { _ = os.RemoveAll(sessionBase) })
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(sessionBase, chatID)
	})

	expiredID := "expired-sess-with-files"
	orphanID := "orphan-sess-with-files"

	now := time.Now()
	expiredTime := now.AddDate(0, -1, -2)

	// Setup expired session
	require.NoError(t, repo.SaveSession(&Session{
		ChatID:    expiredID,
		Title:     "Expired Session with Transcript and Workflows",
		UpdatedAt: expiredTime,
		Messages: []ChatMessage{
			{ID: "m1", Role: "user", Content: "hello"},
		},
	}))
	require.NoError(t, dbConn.Exec("UPDATE sessions SET updated_at = ? WHERE chat_id = ?", expiredTime, expiredID).Error)

	// Create physical files for expired session
	expiredSessDir := filepath.Join(sessionBase, expiredID)
	expiredWfDir := filepath.Join(expiredSessDir, "workflows", "wf-run-1", "nodes")
	require.NoError(t, os.MkdirAll(expiredWfDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(expiredWfDir, "node.log"), []byte("log data"), 0644))

	// Setup orphan session directory in sessionBase
	orphanSessDir := filepath.Join(sessionBase, orphanID)
	orphanWfDir := filepath.Join(orphanSessDir, "workflows", "wf-run-orphan")
	require.NoError(t, os.MkdirAll(orphanWfDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(orphanSessDir, "messages.jsonl"), []byte(`{"role":"user"}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(orphanWfDir, "run.json"), []byte(`{}`), 0644))

	// Set mod times to expiredTime
	require.NoError(t, os.Chtimes(orphanSessDir, expiredTime, expiredTime))
	require.NoError(t, os.Chtimes(filepath.Join(orphanSessDir, "messages.jsonl"), expiredTime, expiredTime))
	require.NoError(t, os.Chtimes(filepath.Join(orphanSessDir, "workflows"), expiredTime, expiredTime))
	require.NoError(t, os.Chtimes(orphanWfDir, expiredTime, expiredTime))
	require.NoError(t, os.Chtimes(filepath.Join(orphanWfDir, "run.json"), expiredTime, expiredTime))

	cutoff := now.AddDate(0, -1, 0)
	err := repo.CleanExpiredSessions(CleanExpiredSessionsOptions{
		Cutoff:  cutoff,
		TmpBase: tmpBase,
	})
	require.NoError(t, err)

	// Expired session and orphan session should have their directories deleted
	_, errExpired := os.Stat(expiredSessDir)
	assert.True(t, os.IsNotExist(errExpired), "Expired session dir should be removed")

	_, errOrphan := os.Stat(orphanSessDir)
	assert.True(t, os.IsNotExist(errOrphan), "Orphan session dir should be removed")
}
