package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// hasUncommittedChangesInDirs checks whether any of the directories in dirs has unstashed git changes.
func (s *Server) hasUncommittedChangesInDirs(ctx context.Context, chatID string, dirs []string) bool {
	if s == nil || len(dirs) == 0 {
		return false
	}
	fs, err := s.chatFS(chatID)
	if err != nil {
		return false
	}

	checkCtx := ctx
	if checkCtx == nil || checkCtx.Err() != nil {
		var cancel context.CancelFunc
		checkCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	} else {
		var cancel context.CancelFunc
		checkCtx, cancel = context.WithTimeout(checkCtx, 10*time.Second)
		defer cancel()
	}

	for _, d := range dirs {
		if d == "" || dirlock.IsExemptDir(chatID, d) {
			continue
		}
		res, err := fs.GitLog(checkCtx, d, 1)
		if err == nil && res.UnstashedCount > 0 {
			return true
		}
	}
	return false
}

// hasUncommittedChanges checks whether runDir or any directory currently locked for chatID has unstashed git changes.
func (s *Server) hasUncommittedChanges(chatID, runDir string) bool {
	if s == nil || chatID == "" {
		return false
	}
	dirs := make([]string, 0, 4)
	if runDir != "" && !dirlock.IsExemptDir(chatID, runDir) {
		dirs = append(dirs, runDir)
	}
	if s.dirLocks != nil {
		for _, d := range s.dirLocks.LockedDirsForChat(chatID) {
			if d != "" && !dirlock.IsExemptDir(chatID, d) && !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}
	return s.hasUncommittedChangesInDirs(context.Background(), chatID, dirs)
}

// acquireChatDirLock acquires directory locks for chatID and the given agent/runDir.
func (s *Server) acquireChatDirLock(ctx context.Context, chatID string, agent *agentspec.Agent, runDir string) (func(keepUncommitted bool), error) {
	if s == nil || s.dirLocks == nil {
		return func(bool) {}, nil
	}

	runnerID := ""
	title := ""
	if s.repo != nil && chatID != "" {
		meta, err := s.repo.GetSessionMeta(chatID)
		if err != nil {
			return nil, fmt.Errorf("failed to get session metadata for chat %s: %w", chatID, err)
		}
		if meta != nil {
			runnerID = meta.RunnerNodeID
			title = meta.Title
		}
	}

	var cfg *agentspec.AgentConfig
	if agent != nil {
		cfg = &agent.Config
	}
	dirs, mode := dirlock.ComputeLockedDirs(chatID, runDir, cfg)

	return s.dirLocks.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   chatID,
		Title:    title,
		RunnerID: runnerID,
		Dirs:     dirs,
		Mode:     mode,
	})
}

// dirLockStatus returns the dirlock.Status snapshot for chatID or nil.
func (s *Server) dirLockStatus(chatID string) *dirlock.Status {
	if s == nil || s.dirLocks == nil || chatID == "" {
		return nil
	}
	return s.dirLocks.StatusForChat(chatID)
}

// handleUnlockSession handles POST /api/sessions/{id}/unlock to release an idle uncommitted lock.
func (s *Server) handleUnlockSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "session id is required")
		return
	}
	if !IsValidChatID(id) {
		writeJSONError(w, http.StatusBadRequest, "invalid session id format")
		return
	}

	if s.repo != nil {
		sess, err := s.repo.GetSessionMeta(id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to query session: "+err.Error())
			return
		}
		if sess == nil {
			writeJSONError(w, http.StatusNotFound, "session not found")
			return
		}
		if sess.RunnerNodeID != "" {
			if aerr := s.authorizeRunner(r, sess.RunnerNodeID); aerr != nil {
				writeJSONError(w, aerr.status, aerr.msg)
				return
			}
		}
	}

	if s.dirLocks == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "unlocked",
			"chatId":   id,
			"unlocked": false,
		})
		return
	}

	unlocked, err := s.dirLocks.UnlockIdle(id)
	if errors.Is(err, dirlock.ErrLockActiveRun) {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "unlocked",
		"chatId":   id,
		"unlocked": unlocked,
	})
}
