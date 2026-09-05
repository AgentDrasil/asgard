package dbmodels

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
)

// CleanExpiredSessionsOptions contains configuration for session cleanup.
type CleanExpiredSessionsOptions struct {
	Cutoff  time.Time
	TmpBase string // Base directory for session temporary files, defaults to ~/tmp
}

// CleanExpiredSessions deletes inactive, non-running sessions and their corresponding
// session directories (e.g. ~/tmp/<chatID> and ~/data/<chatID>, plus the per-chat
// merged CA bundle dir ~/tmp/.asgard-ca/<chatID>) older than cutoff.
func (r *SessionRepository) CleanExpiredSessions(opts CleanExpiredSessionsOptions) error {
	var expiredSessions []Session
	if err := r.db.Where("updated_at < ?", opts.Cutoff).Find(&expiredSessions).Error; err != nil {
		return fmt.Errorf("query expired sessions: %w", err)
	}

	tmpDir := opts.TmpBase
	if tmpDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			log.Warn().Err(err).Msg("CleanExpiredSessions: could not determine user home directory")
		} else {
			tmpDir = filepath.Join(homeDir, "tmp")
		}
	}

	// Session dirs (sandbox /session) live as a sibling of the tmp base (~/data when tmp defaults to ~/tmp)
	sessionDir := ""
	if tmpDir != "" {
		sessionDir = filepath.Join(filepath.Dir(tmpDir), "data")
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
			if base == tmpDir && entry.Name() == caBundleDirName {
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
