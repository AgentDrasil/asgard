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
// session temporary directories (e.g. ~/tmp/<chatID>) older than cutoff.
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

	var errs []error

	for _, sess := range expiredSessions {
		// 1. Skip if session is currently running
		if sess.IsRunning() {
			log.Info().Str("chatID", sess.ChatID).Msg("Skipping cleanup for active running session")
			continue
		}

		// 2. Delete session DB record
		if err := r.DeleteSession(sess.ChatID); err != nil {
			log.Error().Err(err).Str("chatID", sess.ChatID).Msg("Failed to delete expired session from db")
			errs = append(errs, fmt.Errorf("delete session %s: %w", sess.ChatID, err))
			continue
		}

		// 3. Remove session folder under tmpDir/<chatID>
		if tmpDir != "" && sess.ChatID != "" {
			sessionTmpPath := filepath.Join(tmpDir, sess.ChatID)
			if err := os.RemoveAll(sessionTmpPath); err != nil {
				log.Warn().Err(err).Str("path", sessionTmpPath).Msg("Failed to remove session tmp dir")
				errs = append(errs, fmt.Errorf("remove tmp dir %s: %w", sessionTmpPath, err))
			}
		}
	}

	// 4. Clean orphan directories in tmpDir whose chat_id does not exist in DB and latest mtime < cutoff
	if tmpDir != "" {
		entries, err := os.ReadDir(tmpDir)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}

				dirPath := filepath.Join(tmpDir, entry.Name())
				latestMtime := getLatestModTime(dirPath, entry)
				if !latestMtime.Before(opts.Cutoff) {
					continue
				}

				name := entry.Name()
				var count int64
				if err := r.db.Model(&Session{}).Where("chat_id = ?", name).Count(&count).Error; err == nil && count == 0 {
					if err := os.RemoveAll(dirPath); err != nil {
						log.Warn().Err(err).Str("path", dirPath).Msg("Failed to remove orphan tmp dir")
						errs = append(errs, fmt.Errorf("remove orphan tmp dir %s: %w", dirPath, err))
					} else {
						log.Info().Str("path", dirPath).Msg("Removed orphan session tmp dir")
					}
				}
			}
		}
	}

	return errors.Join(errs...)
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
