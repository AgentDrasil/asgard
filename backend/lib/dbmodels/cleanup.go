package dbmodels

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"

	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

// CleanExpiredSessionsOptions contains configuration for session cleanup.
type CleanExpiredSessionsOptions struct {
	Cutoff time.Time
	// TmpBase overrides the session temporary root; defaults to runnerpaths.TmpDir().
	// It exists only to isolate tests.
	TmpBase string
	// SessionBase overrides the persistent session root; defaults to runnerpaths.SessionsDir().
	// It exists only to isolate tests.
	SessionBase string
}

// ExpiredIdleSessions returns the sessions last updated before cutoff that are
// not running (running ones are never cleaned up).
func (r *SessionRepository) ExpiredIdleSessions(cutoff time.Time) ([]Session, error) {
	var expired []Session
	if err := r.db.Where("updated_at < ?", cutoff).Find(&expired).Error; err != nil {
		return nil, fmt.Errorf("query expired sessions: %w", err)
	}
	idle := expired[:0]
	for _, sess := range expired {
		if sess.IsRunning() {
			log.Info().Str("chatID", sess.ChatID).Msg("Skipping cleanup for active running session")
			continue
		}
		idle = append(idle, sess)
	}
	return idle, nil
}

// AllChatIDs lists every chat id in the database: the keep set of the runners'
// orphan sweeps.
func (r *SessionRepository) AllChatIDs() ([]string, error) {
	var ids []string
	if err := r.db.Model(&Session{}).Pluck("chat_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("list chat ids: %w", err)
	}
	return ids, nil
}

// WorkflowChatsByRunner maps each runner node to the chats bound to it that
// have workflow runs (their offloaded files live in <session>/workflows on
// that runner).
func (r *SessionRepository) WorkflowChatsByRunner() (map[string][]string, error) {
	if !r.db.Migrator().HasTable(&WorkflowRun{}) {
		return nil, nil
	}
	var rows []struct {
		RunnerNodeID string
		ChatID       string
	}
	err := r.db.Table("sessions").
		Select("DISTINCT sessions.runner_node_id AS runner_node_id, sessions.chat_id AS chat_id").
		Joins("JOIN workflow_runs ON workflow_runs.session_id = sessions.chat_id").
		Where("sessions.runner_node_id <> ''").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list workflow chats: %w", err)
	}
	out := make(map[string][]string)
	for _, row := range rows {
		out[row.RunnerNodeID] = append(out[row.RunnerNodeID], row.ChatID)
	}
	return out, nil
}

// CleanExpiredSessions is the runner-less cleanup: it deletes inactive, non-running sessions and their corresponding
// session directories (/session-data/tmp/<chatID> and
// /session-data/sessions/<chatID>, plus the per-chat merged CA bundle dir
// /session-data/tmp/.asgard-ca/<chatID>) older than cutoff on THIS host. Only
// the runner-less (development) setup uses it: with runners configured the
// directories live on the runners and backend/lib/cleanup asks them to clean
// up (Cleanup RPC) instead of touching any local directory.
func (r *SessionRepository) CleanExpiredSessions(opts CleanExpiredSessionsOptions) error {
	var expiredSessions []Session
	if err := r.db.Where("updated_at < ?", opts.Cutoff).Find(&expiredSessions).Error; err != nil {
		return fmt.Errorf("query expired sessions: %w", err)
	}

	tmpDir := opts.TmpBase
	if tmpDir == "" {
		tmpDir = runnerpaths.TmpDir()
	}
	sessionDir := opts.SessionBase
	if sessionDir == "" {
		sessionDir = runnerpaths.SessionsDir()
	}

	cleanupBases := make([]string, 0, 2)
	for _, base := range []string{tmpDir, sessionDir} {
		if base != "" {
			cleanupBases = append(cleanupBases, base)
		}
	}

	var errs []error

	for _, sess := range expiredSessions {
		// 1. Skip if session is currently running
		if sess.IsRunning() {
			log.Info().Str("chatID", sess.ChatID).Msg("Skipping cleanup for active running session")
			continue
		}

		// 2. Delete session DB record and physical session directory
		if err := r.DeleteSession(sess.ChatID); err != nil {
			log.Error().Err(err).Str("chatID", sess.ChatID).Msg("Failed to delete expired session from db")
			errs = append(errs, fmt.Errorf("delete session %s: %w", sess.ChatID, err))
			continue
		}

		// 3. Remove session folders under cleanupBases (e.g. tmpDir/<chatID>)
		if sess.ChatID != "" {
			for _, base := range cleanupBases {
				sessionPath := filepath.Join(base, sess.ChatID)
				if err := os.RemoveAll(sessionPath); err != nil {
					log.Warn().Err(err).Str("path", sessionPath).Msg("Failed to remove session dir")
					errs = append(errs, fmt.Errorf("remove dir %s: %w", sessionPath, err))
				}
			}
		}
	}

	// 4. Clean orphan directories under tmpDir/sessionDir whose chat_id does not exist in DB and latest mtime < cutoff
	for _, base := range cleanupBases {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			dirPath := filepath.Join(base, entry.Name())

			// The CA bundle container (~/tmp/.asgard-ca) holds one subdirectory per
			// chat; sweep each chat bundle individually so bundles of live chats are
			// never removed wholesale.
			if base == tmpDir && entry.Name() == runnerpaths.CABundleDirName {
				caEntries, err := os.ReadDir(dirPath)
				if err != nil {
					continue
				}
				for _, caEntry := range caEntries {
					if !caEntry.IsDir() {
						continue
					}
					if err := r.removeOrphanDir(filepath.Join(dirPath, caEntry.Name()), caEntry, opts.Cutoff); err != nil {
						errs = append(errs, err)
					}
				}
				continue
			}

			if err := r.removeOrphanDir(dirPath, entry, opts.Cutoff); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

// removeOrphanDir removes dirPath when its latest modification time is older than
// cutoff and no session with a matching chat_id exists in the database.
func (r *SessionRepository) removeOrphanDir(dirPath string, entry os.DirEntry, cutoff time.Time) error {
	if !getLatestModTime(dirPath, entry).Before(cutoff) {
		return nil
	}

	var count int64
	if err := r.db.Model(&Session{}).Where("chat_id = ?", filepath.Base(dirPath)).Count(&count).Error; err != nil || count != 0 {
		return nil
	}

	if err := os.RemoveAll(dirPath); err != nil {
		log.Warn().Err(err).Str("path", dirPath).Msg("Failed to remove orphan session dir")
		return fmt.Errorf("remove orphan session dir %s: %w", dirPath, err)
	}
	log.Info().Str("path", dirPath).Msg("Removed orphan session dir")
	return nil
}

// getLatestModTime returns the latest modification time among the directory itself and any files inside it.
func getLatestModTime(dirPath string, entry os.DirEntry) time.Time {
	info, err := entry.Info()
	var latest time.Time
	if err == nil {
		latest = info.ModTime()
	}

	_ = filepath.WalkDir(dirPath, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			if fi.ModTime().After(latest) {
				latest = fi.ModTime()
			}
		}
		return nil
	})

	return latest
}

// PruneFinalizedRuns deletes finalized-run ids recorded before cutoff and
// returns how many were removed.
func (r *SessionRepository) PruneFinalizedRuns(cutoff time.Time) (int64, error) {
	res := r.db.Where("finalized_at < ?", cutoff).Delete(&FinalizedRun{})
	if res.Error != nil {
		return 0, fmt.Errorf("prune finalized runs: %w", res.Error)
	}
	return res.RowsAffected, nil
}
