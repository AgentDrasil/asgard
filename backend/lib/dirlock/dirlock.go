package dirlock

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

type Mode string

const (
	ModeExclusive Mode = "exclusive"
	ModeReadOnly  Mode = "readonly"
)

type Reason string

const (
	ReasonRunning     Reason = "running"
	ReasonUncommitted Reason = "uncommitted"
)

var ErrLockActiveRun = errors.New("cannot unlock directory while session is actively running")

type Status struct {
	IsLocked   bool   `json:"isLocked,omitempty"`
	LockMode   Mode   `json:"lockMode,omitempty"`
	LockReason Reason `json:"lockReason,omitempty"`
	LockedDir  string `json:"lockedDir,omitempty"`
	CanUnlock  bool   `json:"canUnlock,omitempty"`

	IsWaitingForLock bool   `json:"isWaitingForLock,omitempty"`
	LockedByChatID   string `json:"lockedByChatId,omitempty"`
	LockedByTitle    string `json:"lockedByTitle,omitempty"`
	BlockingReason   Reason `json:"blockingReason,omitempty"`
}

type AcquireRequest struct {
	ChatID   string
	Title    string
	RunnerID string
	Dirs     []string
	Mode     Mode
}

type lockHolder struct {
	chatID     string
	title      string
	runnerID   string
	dirs       []string
	mode       Mode
	reason     Reason
	activeRuns int
}

type waiter struct {
	req             AcquireRequest
	ready           chan struct{}
	blockedByChatID string
	blockedByTitle  string
	blockedByReason Reason
	blockedDir      string
}

type Manager struct {
	mu       sync.Mutex
	onChange func(chatID string)
	holders  map[string]*lockHolder // chatID -> holder
	waiters  []*waiter
}

func NewManager(onChange func(chatID string)) *Manager {
	return &Manager{
		onChange: onChange,
		holders:  make(map[string]*lockHolder),
	}
}

// IsExemptDir returns true if dir == "" or cleaned path is /tmp, /session,
// under /tmp/, /session/, /session-data/tmp/, /session-data/sessions/,
// /asgard/data/tmp/, /asgard/data/sessions/, or under runnerpaths.SessionTmpDir(chatID) / runnerpaths.SessionDir(chatID).
func IsExemptDir(chatID, dir string) bool {
	if dir == "" {
		return true
	}
	clean := filepath.Clean(dir)
	if clean == "." || clean == "/tmp" || clean == "/session" {
		return true
	}
	if strings.HasPrefix(clean, "/tmp/") || strings.HasPrefix(clean, "/session/") {
		return true
	}
	if clean == "/session-data/tmp" || strings.HasPrefix(clean, "/session-data/tmp/") {
		return true
	}
	if clean == "/session-data/sessions" || strings.HasPrefix(clean, "/session-data/sessions/") {
		return true
	}
	if clean == "/asgard/data/tmp" || strings.HasPrefix(clean, "/asgard/data/tmp/") {
		return true
	}
	if clean == "/asgard/data/sessions" || strings.HasPrefix(clean, "/asgard/data/sessions/") {
		return true
	}

	tmpDir := filepath.Clean(runnerpaths.TmpDir())
	if clean == tmpDir || strings.HasPrefix(clean, tmpDir+"/") {
		return true
	}
	sessionsDir := filepath.Clean(runnerpaths.SessionsDir())
	if clean == sessionsDir || strings.HasPrefix(clean, sessionsDir+"/") {
		return true
	}

	if chatID != "" {
		sessTmp := filepath.Clean(runnerpaths.SessionTmpDir(chatID))
		if clean == sessTmp || strings.HasPrefix(clean, sessTmp+"/") {
			return true
		}
		sessDir := filepath.Clean(runnerpaths.SessionDir(chatID))
		if clean == sessDir || strings.HasPrefix(clean, sessDir+"/") {
			return true
		}
	}
	return false
}

// ComputeLockedDirs extracts normalized directories to lock and the lock mode.
func ComputeLockedDirs(chatID string, runDir string, cfg *agentspec.AgentConfig) ([]string, Mode) {
	mode := ModeExclusive
	if cfg != nil && cfg.IsReadOnly() {
		mode = ModeReadOnly
	}

	var dirs []string
	if runDir != "" && !IsExemptDir(chatID, runDir) {
		dirs = append(dirs, filepath.Clean(runDir))
	}

	if cfg != nil && !cfg.IsReadOnly() {
		for _, entry := range cfg.MountDirs.EffectiveReadWriteEntries() {
			if entry.MaskRules != nil {
				// DO NOT lock this path! A mount with mask_rules is a shared parent container.
				continue
			}
			if entry.Path != "" && !IsExemptDir(chatID, entry.Path) {
				dirs = append(dirs, filepath.Clean(entry.Path))
			}
		}
	}

	if len(dirs) == 0 {
		return []string{}, mode
	}

	slices.Sort(dirs)
	dirs = slices.Compact(dirs)
	return dirs, mode
}

// PathsOverlap returns true if cleanA == cleanB, or cleanA is an ancestor of cleanB, or cleanB is an ancestor of cleanA.
func PathsOverlap(a, b string) bool {
	cleanA := filepath.Clean(a)
	cleanB := filepath.Clean(b)
	if cleanA == cleanB {
		return true
	}
	if cleanA == "/" || strings.HasPrefix(cleanB, cleanA+"/") {
		return true
	}
	if cleanB == "/" || strings.HasPrefix(cleanA, cleanB+"/") {
		return true
	}
	return false
}

// Acquire requests lock acquisition according to req.
func (m *Manager) Acquire(ctx context.Context, req AcquireRequest) (func(keepUncommitted bool), error) {
	cleanedDirs := make([]string, 0, len(req.Dirs))
	for _, d := range req.Dirs {
		if d != "" {
			cleanedDirs = append(cleanedDirs, filepath.Clean(d))
		}
	}
	slices.Sort(cleanedDirs)
	cleanedDirs = slices.Compact(cleanedDirs)
	req.Dirs = cleanedDirs

	if req.Mode == "" {
		req.Mode = ModeExclusive
	}

	if len(req.Dirs) == 0 {
		return func(bool) {}, nil
	}

	m.mu.Lock()

	// 1. Same ChatID is re-entrant and never conflicts with itself on already-held dirs.
	// Check if this request expands held directories or upgrades lock mode.
	if h, ok := m.holders[req.ChatID]; ok {
		needsExpansionOrUpgrade := false
		targetMode := h.mode
		if h.mode == ModeReadOnly && req.Mode == ModeExclusive {
			targetMode = ModeExclusive
			needsExpansionOrUpgrade = true
		}

		mergedDirs := append([]string{}, h.dirs...)
		for _, d := range req.Dirs {
			if !slices.Contains(mergedDirs, d) {
				mergedDirs = append(mergedDirs, d)
			}
		}
		slices.Sort(mergedDirs)
		mergedDirs = slices.Compact(mergedDirs)

		if !needsExpansionOrUpgrade {
			for _, rd := range req.Dirs {
				covered := false
				cleanRD := filepath.Clean(rd)
				for _, hd := range h.dirs {
					cleanHD := filepath.Clean(hd)
					if cleanHD == cleanRD || cleanHD == "/" || strings.HasPrefix(cleanRD, cleanHD+"/") {
						covered = true
						break
					}
				}
				if !covered {
					needsExpansionOrUpgrade = true
					break
				}
			}
		}

		if needsExpansionOrUpgrade {
			conflicts, conflictHolder, conflictDir := m.findConflictLocked(AcquireRequest{
				ChatID:   req.ChatID,
				RunnerID: req.RunnerID,
				Dirs:     mergedDirs,
				Mode:     targetMode,
			})
			if conflicts {
				m.mu.Unlock()
				blockerID := ""
				if conflictHolder != nil {
					blockerID = conflictHolder.chatID
				}
				return nil, fmt.Errorf("cannot expand or upgrade held directory lock for chat %s: directory %s is locked by chat %s", req.ChatID, conflictDir, blockerID)
			}
			h.dirs = mergedDirs
			h.mode = targetMode
		}

		h.activeRuns++
		h.reason = ReasonRunning
		if req.Title != "" {
			h.title = req.Title
		}

		m.mu.Unlock()
		m.notify([]string{req.ChatID})
		return m.makeReleaseFunc(req.ChatID), nil
	}

	// 2. Different ChatID: check for conflicts with holders and earlier FIFO waiters.
	conflicts, blockingHolder, blockingDir := m.findConflictLocked(req)
	if !conflicts {
		m.holders[req.ChatID] = &lockHolder{
			chatID:     req.ChatID,
			title:      req.Title,
			runnerID:   req.RunnerID,
			dirs:       req.Dirs,
			mode:       req.Mode,
			reason:     ReasonRunning,
			activeRuns: 1,
		}
		m.mu.Unlock()
		m.notify([]string{req.ChatID})
		return m.makeReleaseFunc(req.ChatID), nil
	}

	// Conflicted: add FIFO waiter entry and notify UI.
	blockedByReason := ReasonRunning
	blockedByChatID := ""
	blockedByTitle := ""
	if blockingHolder != nil {
		blockedByChatID = blockingHolder.chatID
		blockedByTitle = blockingHolder.title
		if blockingHolder.reason != "" {
			blockedByReason = blockingHolder.reason
		}
	}
	if blockingDir == "" && len(req.Dirs) > 0 {
		blockingDir = req.Dirs[0]
	}

	w := &waiter{
		req:             req,
		ready:           make(chan struct{}),
		blockedByChatID: blockedByChatID,
		blockedByTitle:  blockedByTitle,
		blockedByReason: blockedByReason,
		blockedDir:      blockingDir,
	}
	m.waiters = append(m.waiters, w)
	m.mu.Unlock()
	m.notify([]string{req.ChatID})

	select {
	case <-w.ready:
		m.mu.Lock()
		h := m.holders[req.ChatID]
		m.mu.Unlock()
		if h == nil {
			return nil, errors.New("lock acquisition cancelled")
		}
		return m.makeReleaseFunc(req.ChatID), nil

	case <-ctx.Done():
		m.mu.Lock()
		select {
		case <-w.ready:
			// Lock was granted right before or concurrently with cancellation
			m.mu.Unlock()
			rel := m.makeReleaseFunc(req.ChatID)
			rel(false)
			return nil, ctx.Err()
		default:
			for idx, item := range m.waiters {
				if item == w {
					m.waiters = append(m.waiters[:idx], m.waiters[idx+1:]...)
					break
				}
			}
			woken := m.processWaitersLocked()
			m.mu.Unlock()
			m.notify(append(woken, req.ChatID))
			return nil, ctx.Err()
		}
	}
}

func (m *Manager) findConflictLocked(req AcquireRequest) (bool, *lockHolder, string) {
	// Check against current holders
	for _, h := range m.holders {
		if h.chatID == req.ChatID || h.runnerID != req.RunnerID {
			continue
		}
		for _, d1 := range req.Dirs {
			for _, d2 := range h.dirs {
				if PathsOverlap(d1, d2) {
					if req.Mode == ModeExclusive || h.mode == ModeExclusive {
						return true, h, d1
					}
				}
			}
		}
	}

	// Check against earlier FIFO waiters to maintain ordering and prevent writer starvation
	for _, w := range m.waiters {
		if w.req.ChatID == req.ChatID || w.req.RunnerID != req.RunnerID {
			continue
		}
		for _, d1 := range req.Dirs {
			for _, d2 := range w.req.Dirs {
				if PathsOverlap(d1, d2) {
					if req.Mode == ModeExclusive || w.req.Mode == ModeExclusive {
						blockingH := m.holders[w.blockedByChatID]
						if blockingH == nil || blockingH.chatID == req.ChatID {
							for _, h := range m.holders {
								if h.chatID != req.ChatID && h.runnerID == req.RunnerID {
									for _, hd := range h.dirs {
										if PathsOverlap(d1, hd) {
											blockingH = h
											break
										}
									}
								}
								if blockingH != nil {
									break
								}
							}
						}
						if blockingH == nil || blockingH.chatID == req.ChatID {
							blockingH = &lockHolder{
								chatID: w.req.ChatID,
								title:  w.req.Title,
								reason: ReasonRunning,
							}
						}
						return true, blockingH, d1
					}
				}
			}
		}
	}

	return false, nil, ""
}

func (m *Manager) makeReleaseFunc(chatID string) func(keepUncommitted bool) {
	var once sync.Once
	return func(keepUncommitted bool) {
		once.Do(func() {
			m.mu.Lock()
			h, ok := m.holders[chatID]
			if !ok {
				m.mu.Unlock()
				return
			}
			h.activeRuns--
			if h.activeRuns > 0 {
				m.mu.Unlock()
				return
			}

			// activeRuns == 0
			var notified []string
			if h.mode == ModeExclusive && keepUncommitted {
				h.reason = ReasonUncommitted
				notified = append(notified, chatID)
				for _, w := range m.waiters {
					if w.blockedByChatID == chatID {
						w.blockedByReason = ReasonUncommitted
						notified = append(notified, w.req.ChatID)
					}
				}
				m.mu.Unlock()
				m.notify(notified)
				return
			}

			// Release lock completely
			delete(m.holders, chatID)
			notified = append(notified, chatID)
			woken := m.processWaitersLocked()
			notified = append(notified, woken...)
			m.mu.Unlock()
			m.notify(notified)
		})
	}
}

// processWaitersLocked inspects waiters in FIFO order and grants locks to any
// that are now unblocked. Must be called with m.mu locked. Returns chats to notify.
func (m *Manager) processWaitersLocked() []string {
	var notified []string

	i := 0
	for i < len(m.waiters) {
		w := m.waiters[i]

		// Check against current holders
		conflictsWithHolder := false
		var conflictHolder *lockHolder
		var conflictDir string

		for _, h := range m.holders {
			if h.chatID == w.req.ChatID || h.runnerID != w.req.RunnerID {
				continue
			}
			for _, d1 := range w.req.Dirs {
				for _, d2 := range h.dirs {
					if PathsOverlap(d1, d2) {
						if w.req.Mode == ModeExclusive || h.mode == ModeExclusive {
							conflictsWithHolder = true
							conflictHolder = h
							conflictDir = d1
							break
						}
					}
				}
				if conflictsWithHolder {
					break
				}
			}
			if conflictsWithHolder {
				break
			}
		}

		// Check against earlier waiters still in m.waiters (indices 0..i-1)
		conflictsWithEarlierWaiter := false
		if !conflictsWithHolder {
			for j := 0; j < i; j++ {
				ew := m.waiters[j]
				if ew.req.RunnerID != w.req.RunnerID {
					continue
				}
				for _, d1 := range w.req.Dirs {
					for _, d2 := range ew.req.Dirs {
						if PathsOverlap(d1, d2) {
							if w.req.Mode == ModeExclusive || ew.req.Mode == ModeExclusive {
								conflictsWithEarlierWaiter = true
								break
							}
						}
					}
					if conflictsWithEarlierWaiter {
						break
					}
				}
				if conflictsWithEarlierWaiter {
					break
				}
			}
		}

		if !conflictsWithHolder && !conflictsWithEarlierWaiter {
			if existing, ok := m.holders[w.req.ChatID]; ok {
				for _, d := range w.req.Dirs {
					if !slices.Contains(existing.dirs, d) {
						existing.dirs = append(existing.dirs, d)
					}
				}
				slices.Sort(existing.dirs)
				if w.req.Mode == ModeExclusive {
					existing.mode = ModeExclusive
				}
				existing.activeRuns++
				existing.reason = ReasonRunning
				if w.req.Title != "" {
					existing.title = w.req.Title
				}
			} else {
				m.holders[w.req.ChatID] = &lockHolder{
					chatID:     w.req.ChatID,
					title:      w.req.Title,
					runnerID:   w.req.RunnerID,
					dirs:       w.req.Dirs,
					mode:       w.req.Mode,
					reason:     ReasonRunning,
					activeRuns: 1,
				}
			}
			m.waiters = append(m.waiters[:i], m.waiters[i+1:]...)
			close(w.ready)
			notified = append(notified, w.req.ChatID)
			continue
		}

		if conflictHolder != nil {
			w.blockedByChatID = conflictHolder.chatID
			w.blockedByTitle = conflictHolder.title
			w.blockedByReason = conflictHolder.reason
			w.blockedDir = conflictDir
		}
		i++
	}

	// Update blocking references for any remaining waiters
	for _, w := range m.waiters {
		for _, h := range m.holders {
			if h.chatID == w.req.ChatID || h.runnerID != w.req.RunnerID {
				continue
			}
			matched := false
			for _, d1 := range w.req.Dirs {
				for _, d2 := range h.dirs {
					if PathsOverlap(d1, d2) {
						w.blockedByChatID = h.chatID
						w.blockedByTitle = h.title
						w.blockedByReason = h.reason
						w.blockedDir = d1
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if matched {
				break
			}
		}
		notified = append(notified, w.req.ChatID)
	}

	return notified
}

// UnlockIdle removes an idle lock (activeRuns == 0) and unblocks queued waiters.
func (m *Manager) UnlockIdle(chatID string) (bool, error) {
	m.mu.Lock()
	h, ok := m.holders[chatID]
	if !ok {
		m.mu.Unlock()
		return false, nil
	}
	if h.activeRuns > 0 {
		m.mu.Unlock()
		return false, ErrLockActiveRun
	}

	delete(m.holders, chatID)
	var notified []string
	notified = append(notified, chatID)
	woken := m.processWaitersLocked()
	notified = append(notified, woken...)
	m.mu.Unlock()
	m.notify(notified)
	return true, nil
}

// ForceReleaseChat removes any held lock and waiter for chatID, waking eligible waiters.
func (m *Manager) ForceReleaseChat(chatID string) {
	m.mu.Lock()
	var notified []string
	if _, ok := m.holders[chatID]; ok {
		delete(m.holders, chatID)
		notified = append(notified, chatID)
	}

	for i := 0; i < len(m.waiters); {
		if m.waiters[i].req.ChatID == chatID {
			w := m.waiters[i]
			m.waiters = append(m.waiters[:i], m.waiters[i+1:]...)
			close(w.ready)
			notified = append(notified, chatID)
			continue
		}
		i++
	}

	woken := m.processWaitersLocked()
	notified = append(notified, woken...)
	m.mu.Unlock()
	m.notify(notified)
}

// UpdateChatTitle updates the title of a holder or waiter and notifies blocked chats.
func (m *Manager) UpdateChatTitle(chatID, title string) {
	if title == "" {
		return
	}
	m.mu.Lock()
	var notified []string
	if h, ok := m.holders[chatID]; ok {
		h.title = title
		notified = append(notified, chatID)
	}
	for _, w := range m.waiters {
		if w.req.ChatID == chatID {
			w.req.Title = title
			notified = append(notified, chatID)
		}
		if w.blockedByChatID == chatID {
			w.blockedByTitle = title
			notified = append(notified, w.req.ChatID)
		}
	}
	m.mu.Unlock()
	m.notify(notified)
}

// StatusForChat returns the status snapshot for chatID, or nil if chat has no lock and is not waiting.
func (m *Manager) StatusForChat(chatID string) *Status {
	if chatID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if h, ok := m.holders[chatID]; ok {
		dir := ""
		if len(h.dirs) > 0 {
			dir = h.dirs[0]
		}
		return &Status{
			IsLocked:   true,
			LockMode:   h.mode,
			LockReason: h.reason,
			LockedDir:  dir,
			CanUnlock:  h.activeRuns == 0,
		}
	}

	for _, w := range m.waiters {
		if w.req.ChatID == chatID {
			dir := w.blockedDir
			if dir == "" && len(w.req.Dirs) > 0 {
				dir = w.req.Dirs[0]
			}
			return &Status{
				IsWaitingForLock: true,
				LockedDir:        dir,
				LockedByChatID:   w.blockedByChatID,
				LockedByTitle:    w.blockedByTitle,
				BlockingReason:   w.blockedByReason,
			}
		}
	}

	return nil
}

// LockedDirsForChat returns a copy of all locked directories currently held by chatID, or nil.
func (m *Manager) LockedDirsForChat(chatID string) []string {
	if chatID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if h, ok := m.holders[chatID]; ok {
		res := make([]string, len(h.dirs))
		copy(res, h.dirs)
		return res
	}
	return nil
}

func (m *Manager) notify(chatIDs []string) {
	if m.onChange == nil || len(chatIDs) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(chatIDs))
	for _, id := range chatIDs {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		m.onChange(id)
	}
}
