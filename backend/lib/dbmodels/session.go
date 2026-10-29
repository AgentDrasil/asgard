package dbmodels

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/AgentDrasil/asgard/pkg/paths"
)

var sqlLikeReplacer = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")

type Agents []Agent

// Value implements driver.Valuer
func (a Agents) Value() (driver.Value, error) {
	if len(a) == 0 {
		return nil, nil
	}
	return json.Marshal(a)
}

// Scan implements sql.Scanner
func (a *Agents) Scan(value interface{}) error {
	if value == nil {
		*a = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("failed to scan Agents: unsupported type %T", value)
	}
	return json.Unmarshal(bytes, a)
}

type Attachment struct {
	Name     string `json:"name"`
	Path     string `json:"path"` // Path inside sandbox, e.g. "/tmp/attachments/filename.ext"
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType,omitempty"`
}

type ChatMessage struct {
	ID            string       `json:"id"`
	Role          string       `json:"role"`
	Content       string       `json:"content"`
	AgentName     string       `json:"agentName,omitempty"`
	Timestamp     int64        `json:"timestamp,omitempty"`
	ActivityType  string       `json:"activityType,omitempty"`
	StepIndex     int          `json:"stepIndex,omitempty"`
	InputTokens   int          `json:"inputTokens,omitempty"`
	MaxTokens     int          `json:"maxTokens,omitempty"`
	Model         string       `json:"model,omitempty"`
	Replied       bool         `json:"replied,omitempty"`
	ReplyText     string       `json:"replyText,omitempty"`
	TargetFiles   []string     `json:"targetFiles,omitempty"`
	ArtifactFiles []string     `json:"artifactFiles,omitempty"`
	Attachments   []Attachment `json:"attachments,omitempty"`
}

type Messages []ChatMessage

type Artifacts []string

// Value implements driver.Valuer
func (a Artifacts) Value() (driver.Value, error) {
	if len(a) == 0 {
		return nil, nil
	}
	return json.Marshal(a)
}

// Scan implements sql.Scanner
func (a *Artifacts) Scan(value interface{}) error {
	if value == nil {
		*a = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("failed to scan Artifacts: unsupported type %T", value)
	}
	return json.Unmarshal(bytes, a)
}

type Session struct {
	ChatID string `gorm:"primaryKey"`
	// name of current agent.
	CurrentAgent string
	// map of agents in json format.
	Agents Agents `gorm:"type:text"`
	// Dir agent running on
	RunDir string
	// RunnerNodeID is the runner every run of this chat executes on. It is
	// set once, when the chat starts, and never changes afterwards.
	RunnerNodeID string `gorm:"column:runner_node_id;index" json:"runnerId,omitempty"`
	// Title of the session
	Title string
	// Messages of the session (not stored in DB, persisted in messages.jsonl)
	Messages Messages `gorm:"-" json:"messages,omitempty"`
	// MessageCount is the number of messages in the session
	MessageCount int `gorm:"column:message_count;default:0" json:"messageCount"`
	// HasAskUserUnreplied tracks whether there are any unreplied ask_user messages
	HasAskUserUnreplied bool `gorm:"column:has_ask_user_unreplied;default:false" json:"hasAskUserUnreplied"`
	// LastMessageSummary is a compact summary of the last message
	LastMessageSummary string `gorm:"column:last_message_summary;type:text" json:"lastMessageSummary,omitempty"`
	// Artifacts generated in session
	Artifacts Artifacts `gorm:"type:text"`

	// AllowCrossSession allows accessing host ~/tmp, ~/data, and database files inside sandbox
	AllowCrossSession bool `gorm:"column:allow_cross_session;default:false" json:"allowCrossSession"`

	// QuotaResetIDs is the journal of an in-progress orphaned-quota reset: a
	// JSON list of the quota ask messages being retired. It is non-empty only
	// between the steps of the reset (see resetQuotaSuspension).
	QuotaResetIDs *string `gorm:"column:quota_reset_ids;type:text" json:"-"`

	IsArchived bool `gorm:"default:false"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// HasUnrepliedAskUser returns true if there is an unreplied ask_user message in the session.
// In memory or after GetSession/DB query, this provides O(1) query time.
func (s *Session) HasUnrepliedAskUser() bool {
	if s.HasAskUserUnreplied {
		return true
	}
	for _, m := range s.Messages {
		if m.Role == "ask_user" && !m.Replied {
			return true
		}
	}
	return false
}

// IsRunning returns true if any agent in the session has status AgentStatusRunning.
func (s *Session) IsRunning() bool {
	for _, a := range s.Agents {
		if a.Status == AgentStatusRunning {
			return true
		}
	}
	return false
}

type AgentStatus uint

const (
	AgentStatusUnknown AgentStatus = iota
	AgentStatusRunning
	AgentStatusCompleted
	// AgentStatusWaitingHuman marks an agent parked on a human node of a
	// suspended workflow run. It must stay distinguishable from Completed so
	// hosts and UIs can tell "waiting for the user" from "done".
	AgentStatusWaitingHuman
)

type Agent struct {
	Name string `json:"name"`
	// Sessions maps "<cli>/<model>" → session ID returned by that CLI.
	// For sequential agents this map has at most one entry.
	// For fresh-mode agents this map is not written.
	Sessions map[string]string `json:"sessions,omitempty"`
	Status   AgentStatus       `json:"status,omitempty"`
	Model    string            `json:"model,omitempty"`
}

func defaultSessionDir(chatID string) string {
	return paths.SessionDir(chatID)
}

func defaultCABundleDir(chatID string) string {
	return paths.CABundleDir(chatID)
}

type SessionRepository struct {
	db              *gorm.DB
	sessionDirFunc  func(chatID string) string
	caBundleDirFunc func(chatID string) string
	fsResolver      FSResolver
	// pendingQuotaReset maps chats whose startup quota reset was deferred to
	// a pendingQuotaReset entry.
	pendingQuotaReset sync.Map
	// quotaResetHook is called between the reset steps (tests inject failures).
	quotaResetHook func(stage string) error
	sessionLocks   sync.Map // chatID string -> *sync.Mutex
}

func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{
		db:              db,
		sessionDirFunc:  defaultSessionDir,
		caBundleDirFunc: defaultCABundleDir,
	}
}

// SetSessionDirFunc overrides the directory resolver function (used for testing isolation).
func (r *SessionRepository) SetSessionDirFunc(fn func(chatID string) string) {
	r.sessionDirFunc = fn
}

// SetCABundleDirFunc overrides the per-chat merged CA bundle directory resolver
// (used for testing isolation).
func (r *SessionRepository) SetCABundleDirFunc(fn func(chatID string) string) {
	r.caBundleDirFunc = fn
}

// SetFSResolver routes every chat's transcript through its runner's
// RunnerService instead of a local session directory: the runner's copy of
// messages.jsonl is authoritative (the sandbox reads it as /session), and the
// backend never creates per-chat directories of its own. Without a resolver
// (tests, runner-less development) the transcript is a local file.
func (r *SessionRepository) SetFSResolver(fn FSResolver) { r.fsResolver = fn }

// store returns the transcript store of a chat.
func (r *SessionRepository) store(chatID string) (sessionStore, error) {
	if r.fsResolver != nil {
		fs, err := r.fsResolver(chatID)
		if err != nil {
			return nil, err
		}
		return fsStore{fs}, nil
	}
	return dirStore{r.sessionDir(chatID)}, nil
}

func (r *SessionRepository) sessionDir(chatID string) string {
	if r.sessionDirFunc != nil {
		return r.sessionDirFunc(chatID)
	}
	return defaultSessionDir(chatID)
}

func (r *SessionRepository) caBundleDir(chatID string) string {
	if r.caBundleDirFunc != nil {
		return r.caBundleDirFunc(chatID)
	}
	return defaultCABundleDir(chatID)
}

func (r *SessionRepository) getSessionLock(chatID string) *sync.Mutex {
	actual, _ := r.sessionLocks.LoadOrStore(chatID, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

const (
	// DefaultSessionLimit is the default limit for session listing queries.
	DefaultSessionLimit = 500
	// MaxSessionLimit is the maximum upper bound for session listing queries.
	MaxSessionLimit = 1000
)

// NormalizeSessionLimit ensures the given limit falls within [1, MaxSessionLimit],
// falling back to DefaultSessionLimit if limit is <= 0.
func NormalizeSessionLimit(limit int) int {
	if limit <= 0 {
		return DefaultSessionLimit
	}
	if limit > MaxSessionLimit {
		return MaxSessionLimit
	}
	return limit
}

// GetSessions retrieves sessions, filtering by archived status and applying an optional limit.
// By default (or when includeArchived is false), only active (unarchived) sessions are returned.
// When includeArchived is true, only archived sessions are returned.
// limit specifies the maximum number of sessions to return (default 500, max 1000).
// Only loads metadata from DB; Messages remains empty.
func (r *SessionRepository) GetSessions(includeArchived bool, limit ...int) ([]Session, error) {
	queryLimit := DefaultSessionLimit
	if len(limit) > 0 {
		queryLimit = NormalizeSessionLimit(limit[0])
	}

	var sessions []Session
	err := r.db.Where("COALESCE(is_archived, false) = ?", includeArchived).
		Order("updated_at desc").
		Limit(queryLimit).
		Find(&sessions).Error
	return sessions, err
}

// ArchiveSession archives a session by chat ID.
func (r *SessionRepository) ArchiveSession(chatID string) error {
	return r.db.Model(&Session{}).Where("chat_id = ?", chatID).Update("is_archived", true).Error
}

// SearchSessions searches sessions by matching title (case-insensitive substring match).
// Returns an empty slice (not nil) if query is empty or no matches are found.
// By default, it only searches unarchived sessions.
func (r *SessionRepository) SearchSessions(query string, limit int) ([]Session, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return make([]Session, 0), nil
	}

	if limit <= 0 {
		limit = 20
	} else if limit > 50 {
		limit = 50
	}

	escaped := sqlLikeReplacer.Replace(trimmed)

	var sessions []Session
	err := r.db.Where("COALESCE(is_archived, false) = false AND LOWER(title) LIKE LOWER(?) ESCAPE '\\'", "%"+escaped+"%").
		Order("updated_at desc").
		Limit(limit).
		Find(&sessions).Error
	if err != nil {
		return nil, err
	}
	if sessions == nil {
		sessions = make([]Session, 0)
	}
	return sessions, nil
}

// DeleteSession deletes a session by chat ID and removes its session directory.
func (r *SessionRepository) DeleteSession(chatID string) error {
	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	if err := r.db.Delete(&Session{}, "chat_id = ?", chatID).Error; err != nil {
		return err
	}

	if r.db.Migrator().HasTable(&QueuedMessage{}) {
		if err := r.db.Delete(&QueuedMessage{}, "chat_id = ?", chatID).Error; err != nil {
			return err
		}
	}

	// Clean the physical session directory (messages.jsonl, workflows, etc.)
	// and the per-chat merged CA bundle directory (~/tmp/.asgard-ca/<chatID>).
	// With a runner resolver those live on the runner and the backend touches
	// no local directory: the runner's cleanup sweep removes them.
	if r.fsResolver == nil {
		if dir := r.sessionDir(chatID); dir != "" {
			_ = os.RemoveAll(dir)
		}
		if caDir := r.caBundleDir(chatID); caDir != "" {
			_ = os.RemoveAll(caDir)
		}
	}

	// Remove lock entry from map while holding lock to avoid leaks
	r.sessionLocks.Delete(chatID)
	return nil
}

// GetSessionMeta retrieves the session row for a chat ID without its
// transcript (Messages stays empty). Most callers only need the metadata, and
// the transcript lives on the chat's runner, so reading it costs an RPC.
func (r *SessionRepository) GetSessionMeta(chatID string) (*Session, error) {
	var session Session
	err := r.db.First(&session, "chat_id = ?", chatID).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

// GetSession retrieves the session for a given chat ID and loads messages from
// messages.jsonl (on the chat's runner when a resolver is set).
func (r *SessionRepository) GetSession(chatID string) (*Session, error) {
	session, err := r.GetSessionMeta(chatID)
	if err != nil || session == nil {
		return nil, err
	}

	// Read messages from transcript file
	store, err := r.store(chatID)
	if err != nil {
		return nil, fmt.Errorf("read session transcript: %w", err)
	}
	msgs, err := readMessages(store)
	if err != nil {
		return nil, fmt.Errorf("read session transcript: %w", err)
	}
	session.Messages = msgs
	return session, nil
}

// SaveSession saves or updates the session metadata and writes messages to messages.jsonl if len(Messages) > 0.
func (r *SessionRepository) SaveSession(session *Session) error {
	if len(session.Messages) > 0 {
		lock := r.getSessionLock(session.ChatID)
		lock.Lock()
		defer lock.Unlock()

		store, err := r.store(session.ChatID)
		if err != nil {
			return fmt.Errorf("save transcript: %w", err)
		}
		if err := writeAllMessages(store, session.Messages); err != nil {
			return fmt.Errorf("save transcript: %w", err)
		}

		session.MessageCount = len(session.Messages)
		hasUnreplied := false
		for _, m := range session.Messages {
			if m.Role == "ask_user" && !m.Replied {
				hasUnreplied = true
				break
			}
		}
		session.HasAskUserUnreplied = hasUnreplied
		if len(session.Messages) > 0 {
			session.LastMessageSummary = TruncateSummary(session.Messages[len(session.Messages)-1].Content, 200)
		}
	}

	return r.db.Save(session).Error
}

// UpsertSession creates the session if it does not exist; if it already exists,
// only metadata columns (Title, CurrentAgent, RunDir, UpdatedAt) are updated so
// that existing Agents, Artifacts and transcript are preserved.
func (r *SessionRepository) UpsertSession(session *Session) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var existing Session
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existing, "chat_id = ?", session.ChatID).Error
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return tx.Create(session).Error
			}
			return err
		}
		updates := map[string]any{
			"title":         session.Title,
			"current_agent": session.CurrentAgent,
			"run_dir":       session.RunDir,
		}
		// An existing binding is never replaced.
		if existing.RunnerNodeID == "" && session.RunnerNodeID != "" {
			updates["runner_node_id"] = session.RunnerNodeID
		}
		return tx.Model(&existing).Updates(updates).Error
	})
}

// BindRunner binds the chat to runnerID unless it is already bound, creating
// the session row if needed. It returns the runner the chat is bound to
// afterwards: runnerID for a new binding, the stored one otherwise, so callers
// detect a conflicting concurrent binding by comparing the result.
func (r *SessionRepository) BindRunner(chatID, runnerID string) (string, error) {
	var bound string
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var session Session
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, "chat_id = ?", chatID).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			bound = runnerID
			return tx.Create(&Session{ChatID: chatID, RunnerNodeID: runnerID}).Error
		}
		if session.RunnerNodeID != "" {
			bound = session.RunnerNodeID
			return nil
		}
		bound = runnerID
		return tx.Model(&session).Update("runner_node_id", runnerID).Error
	})
	return bound, err
}

// ChatIDsForRunnerWithQueue returns the chats bound to the given runner that
// have queued messages.
func (r *SessionRepository) ChatIDsForRunnerWithQueue(runnerID string) ([]string, error) {
	var ids []string
	err := r.db.Model(&Session{}).
		Where("runner_node_id = ? AND chat_id IN (?)", runnerID,
			r.db.Model(&QueuedMessage{}).Select("DISTINCT chat_id")).
		Pluck("chat_id", &ids).Error
	return ids, err
}

// UpdateAgentStatus updates the status for a specific agent in a session.
func (r *SessionRepository) UpdateAgentStatus(chatID string, agentID string, status AgentStatus) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var session Session
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, "chat_id = ?", chatID).Error
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}

		found := false
		for i, a := range session.Agents {
			if a.Name == agentID {
				session.Agents[i].Status = status
				found = true
				break
			}
		}
		if !found {
			session.Agents = append(session.Agents, Agent{
				Name:   agentID,
				Status: status,
			})
		}

		return tx.Save(&session).Error
	})
}

// UpdateAgentSession updates the session ID for a specific agent+CLI in a chat and
// optionally updates the run directory and allow_cross_session. cliKey has the format "<cli>/<model>".
// Pass an empty sessionID to skip updating the session map entry.
func (r *SessionRepository) UpdateAgentSession(chatID string, agentID string, cliKey string, sessionID string, runDirOpt optional.Option[string], allowCrossSessionOpt ...optional.Option[bool]) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var session Session
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, "chat_id = ?", chatID).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}

		var sessPtr *Session
		if err == nil {
			sessPtr = &session
		} else {
			sessPtr = &Session{
				ChatID:       chatID,
				CurrentAgent: agentID,
			}
		}

		if len(allowCrossSessionOpt) > 0 && allowCrossSessionOpt[0].IsSome() {
			sessPtr.AllowCrossSession = allowCrossSessionOpt[0].Unwrap()
		}

		if runDirOpt.IsSome() && runDirOpt.Unwrap() != "" {
			sessPtr.RunDir = runDirOpt.Unwrap()
		}

		found := false
		for i, a := range sessPtr.Agents {
			if a.Name == agentID {
				if sessionID != "" && cliKey != "" {
					if sessPtr.Agents[i].Sessions == nil {
						sessPtr.Agents[i].Sessions = make(map[string]string)
					}
					sessPtr.Agents[i].Sessions[cliKey] = sessionID
				}
				found = true
				break
			}
		}

		if !found {
			newAgent := Agent{
				Name:   agentID,
				Status: AgentStatusCompleted,
			}
			if sessionID != "" && cliKey != "" {
				newAgent.Sessions = map[string]string{cliKey: sessionID}
			}
			sessPtr.Agents = append(sessPtr.Agents, newAgent)
		}

		return tx.Save(sessPtr).Error
	})
}

// UpdateAgentModel updates the active model for a specific agent in a chat.
func (r *SessionRepository) UpdateAgentModel(chatID string, agentID string, model string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var session Session
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, "chat_id = ?", chatID).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}

		var sessPtr *Session
		if err == nil {
			sessPtr = &session
		} else {
			sessPtr = &Session{
				ChatID:       chatID,
				CurrentAgent: agentID,
			}
		}

		found := false
		for i, a := range sessPtr.Agents {
			if a.Name == agentID {
				sessPtr.Agents[i].Model = model
				found = true
				break
			}
		}

		if !found {
			newAgent := Agent{
				Name:   agentID,
				Status: AgentStatusCompleted,
				Model:  model,
			}
			sessPtr.Agents = append(sessPtr.Agents, newAgent)
		}

		return tx.Save(sessPtr).Error
	})
}

// ResetAllRunningAgents sets all agents with status AgentStatusRunning to AgentStatusCompleted across all sessions.
func (r *SessionRepository) ResetAllRunningAgents() error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var sessions []Session
		if err := tx.Find(&sessions).Error; err != nil {
			return err
		}
		for _, sess := range sessions {
			modified := false
			for i, a := range sess.Agents {
				if a.Status == AgentStatusRunning {
					sess.Agents[i].Status = AgentStatusCompleted
					modified = true
				}
			}
			if modified {
				if err := tx.Save(&sess).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// QuotaAskMessagePrefix marks ask_user messages raised by the single-agent
// quota-decision loop. Unlike workflow suspensions (which are backed by a
// persisted run snapshot and re-driven), these are served by an in-memory
// waiter, so a crash can orphan them; startup recovery keys off this prefix.
const QuotaAskMessagePrefix = "ask-quota-"

// ResetOrphanedQuotaSuspensions clears single-agent quota suspensions left
// behind by a crash. Unreplied quota ask_user messages created before this
// call (the backend start) are marked replied with the given note (so the UI
// stops showing a pending decision whose waiter no longer exists), the owning
// agents' WaitingHuman status is reset to Completed and the unreplied flag is
// updated. Asks created after the cutoff belong to live waiters and are never
// touched.
//
// A chat whose transcript cannot be reached now (its runner has not
// reconnected yet) is skipped with a warning and remembered with the same
// cutoff; it is redone by ResetPendingQuotaSuspensions when its runner comes
// online.
func (r *SessionRepository) ResetOrphanedQuotaSuspensions(note string) error {
	cutoff := time.Now()
	var sessions []Session
	if err := r.db.Find(&sessions).Error; err != nil {
		return err
	}
	for i := range sessions {
		sess := sessions[i]
		done, err := r.resetQuotaSuspension(sess.ChatID, note, cutoff)
		if err != nil {
			// Keep sweeping the other chats; the runner-online path retries.
			log.Warn().Err(err).Str("chat_id", sess.ChatID).Msg("failed to reset orphaned quota suspension; will retry")
			r.pendingQuotaReset.Store(sess.ChatID, pendingQuotaReset{runner: sess.RunnerNodeID, note: note, cutoff: cutoff})
			continue
		}
		if !done {
			log.Warn().Str("chat_id", sess.ChatID).Str("runner", sess.RunnerNodeID).
				Msg("quota suspension reset deferred until the chat's runner is reachable")
			r.pendingQuotaReset.Store(sess.ChatID, pendingQuotaReset{runner: sess.RunnerNodeID, note: note, cutoff: cutoff})
		}
	}
	return nil
}

// pendingQuotaReset is a quota reset deferred until runner is reachable.
type pendingQuotaReset struct {
	runner string
	note   string
	cutoff time.Time
}

// ResetPendingQuotaSuspensions redoes the startup reset for chats of runnerID
// that were skipped while it was offline, with the original cutoff. It is
// idempotent: a chat without a pre-cutoff unreplied quota ask is left untouched.
func (r *SessionRepository) ResetPendingQuotaSuspensions(runnerID string) {
	r.pendingQuotaReset.Range(func(k, v any) bool {
		if v.(pendingQuotaReset).runner == runnerID {
			r.retryPendingQuota(k.(string))
		}
		return true
	})
}

// RetryPendingQuotaReset retries the deferred reset of one chat, if any (a
// new run or message starting for the chat).
func (r *SessionRepository) RetryPendingQuotaReset(chatID string) {
	r.retryPendingQuota(chatID)
}

// PendingQuotaRunners lists the runners with deferred quota resets.
func (r *SessionRepository) PendingQuotaRunners() []string {
	seen := map[string]bool{}
	var out []string
	r.pendingQuotaReset.Range(func(_, v any) bool {
		if id := v.(pendingQuotaReset).runner; !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
		return true
	})
	return out
}

func (r *SessionRepository) retryPendingQuota(chatID string) {
	v, ok := r.pendingQuotaReset.Load(chatID)
	if !ok {
		return
	}
	p := v.(pendingQuotaReset)
	if done, err := r.resetQuotaSuspension(chatID, p.note, p.cutoff); err != nil {
		log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to reset orphaned quota suspension")
	} else if done {
		r.pendingQuotaReset.Delete(chatID)
	}
}

// quotaResetItem is one retired quota ask and the agent that owned it.
type quotaResetItem struct {
	ID    string `json:"id"`
	Agent string `json:"agent,omitempty"`
	// Timestamp identifies the retired ask: quota ask ids are deterministic,
	// so a later suspension can replace the line under the same id.
	Timestamp int64 `json:"ts"`
}

func (r *SessionRepository) hook(stage string) error {
	if r.quotaResetHook == nil {
		return nil
	}
	return r.quotaResetHook(stage)
}

// resetQuotaSuspension resets one chat in three journaled steps so a crash or
// error between them converges on the next run:
//
//	(a) DB: record the ids about to be retired (sessions.quota_reset_ids);
//	(b) transcript: mark exactly those ids replied (idempotent by id);
//	(c) DB: reconcile the unreplied flag and the owning agents' status, and
//	    clear the journal.
//
// A chat with a journal resumes from (b) for exactly its ids. done is false
// when the transcript could not be reached.
func (r *SessionRepository) resetQuotaSuspension(chatID, note string, cutoff time.Time) (done bool, err error) {
	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	meta, err := r.GetSessionMeta(chatID)
	if err != nil {
		return false, err
	}
	if meta == nil {
		return true, nil
	}
	store, serr := r.store(chatID)
	if serr != nil {
		return false, nil
	}
	msgs, rerr := readMessages(store)
	if rerr != nil {
		return false, nil
	}

	var items []quotaResetItem
	if meta.QuotaResetIDs != nil && *meta.QuotaResetIDs != "" {
		if err := json.Unmarshal([]byte(*meta.QuotaResetIDs), &items); err != nil {
			return true, fmt.Errorf("decode quota reset journal: %w", err)
		}
	} else {
		for _, m := range msgs {
			if m.Role == "ask_user" && !m.Replied && strings.HasPrefix(m.ID, QuotaAskMessagePrefix) && m.Timestamp < cutoff.UnixMilli() {
				items = append(items, quotaResetItem{ID: m.ID, Agent: m.AgentName, Timestamp: m.Timestamp})
			}
		}
		if len(items) == 0 {
			return true, nil
		}
		// (a)
		raw, _ := json.Marshal(items)
		if err := r.db.Model(&Session{}).Where("chat_id = ?", chatID).Update("quota_reset_ids", string(raw)).Error; err != nil {
			return true, err
		}
		if err := r.hook("journaled"); err != nil {
			return true, err
		}
	}

	// (b) Exact-id marking only: an item whose message is gone, or was
	// replaced by a newer ask under the same id, is dropped unmarked. The
	// positional fallback of MarkAskUserReplied is never used here.
	var retained []quotaResetItem
	dirty := false
	for _, it := range items {
		for i := range msgs {
			m := &msgs[i]
			if m.ID != it.ID || m.Role != "ask_user" || m.Timestamp != it.Timestamp || m.Timestamp >= cutoff.UnixMilli() {
				continue
			}
			retained = append(retained, it)
			if !m.Replied {
				m.Replied = true
				m.ReplyText = note
				dirty = true
			}
			break
		}
	}
	if dirty {
		if err := writeAllMessages(store, msgs); err != nil {
			return false, nil
		}
	}
	items = retained
	if err := r.hook("marked"); err != nil {
		return true, err
	}
	after, rerr := readMessages(store)
	if rerr != nil {
		return false, nil
	}
	hasUnreplied := hasUnrepliedAskUser(after)
	agentBusy := map[string]bool{}
	for _, m := range after {
		if m.Role == "ask_user" && !m.Replied {
			agentBusy[m.AgentName] = true
		}
	}

	// (c) Targeted updates under a row lock: never rewrite the other columns
	// from a stale snapshot.
	return true, r.db.Transaction(func(tx *gorm.DB) error {
		var sess Session
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&sess, "chat_id = ?", chatID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		// A workflow suspended on a human node owns WaitingHuman agents too.
		wfWaiting := false
		if tx.Migrator().HasTable(&WorkflowRun{}) {
			var n int64
			if err := tx.Model(&WorkflowRun{}).Where("session_id = ? AND status = ?", chatID, WorkflowStatusWaitingHuman).Count(&n).Error; err != nil {
				return err
			}
			wfWaiting = n > 0
		}
		updates := map[string]any{"has_ask_user_unreplied": hasUnreplied, "quota_reset_ids": nil}
		if !wfWaiting {
			owners := map[string]bool{}
			for _, it := range items {
				if it.Agent != "" && !agentBusy[it.Agent] {
					owners[it.Agent] = true
				}
			}
			changed := false
			for i, a := range sess.Agents {
				if owners[a.Name] && a.Status == AgentStatusWaitingHuman {
					sess.Agents[i].Status = AgentStatusCompleted
					changed = true
				}
			}
			if changed {
				updates["agents"] = sess.Agents
			}
		}
		return tx.Model(&Session{}).Where("chat_id = ?", chatID).Updates(updates).Error
	})
}

// GetAgentSessions returns the sessions map for a specific agent in a chat.
func (r *SessionRepository) GetAgentSessions(chatID string, agentID string) (map[string]string, error) {
	session, err := r.GetSessionMeta(chatID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, nil
	}
	for _, a := range session.Agents {
		if a.Name == agentID {
			return a.Sessions, nil
		}
	}
	return nil, nil
}

// UpdateSessionTitle updates the title of a session by chat ID.
func (r *SessionRepository) UpdateSessionTitle(chatID string, title string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var session Session
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, "chat_id = ?", chatID).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}

		var sessPtr *Session
		if err == nil {
			sessPtr = &session
		} else {
			sessPtr = &Session{
				ChatID: chatID,
			}
		}
		sessPtr.Title = title
		return tx.Save(sessPtr).Error
	})
}

// AppendMessage appends a ChatMessage to a session by chat ID.
// If a message with the same non-empty ID already exists, it updates the existing message in-place in messages.jsonl.
// Checks session existence in DB first; if not found, returns gorm.ErrRecordNotFound without writing file.
func (r *SessionRepository) AppendMessage(chatID string, msg ChatMessage) error {
	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	var session Session
	if err := r.db.First(&session, "chat_id = ?", chatID).Error; err != nil {
		return err
	}

	store, err := r.store(chatID)
	if err != nil {
		return fmt.Errorf("append transcript message: %w", err)
	}
	appended, state, err := appendMessage(store, msg)
	if err != nil {
		return fmt.Errorf("append transcript message: %w", err)
	}

	summary := TruncateSummary(msg.Content, 200)

	// The unreplied ask_user state: exact when the append had to read the
	// transcript anyway (id-keyed update). A plain append cannot resolve an
	// ask_user, so the stored flag only turns on; re-reading the whole
	// transcript over RPC after every message would be wasteful.
	hasUnreplied := session.HasAskUserUnreplied || (msg.Role == "ask_user" && !msg.Replied)
	if state.known {
		hasUnreplied = state.unreplied
	}

	updates := map[string]any{
		"last_message_summary":   summary,
		"has_ask_user_unreplied": hasUnreplied,
		"updated_at":             time.Now(),
	}
	if appended {
		updates["message_count"] = gorm.Expr("message_count + 1")
	}

	return r.db.Model(&Session{}).Where("chat_id = ?", chatID).Updates(updates).Error
}

// MarkAskUserReplied marks an ask_user ChatMessage as replied and sets its reply text in messages.jsonl.
// Returns the updated ChatMessage on success. Returns nil, nil if session does not exist in DB.
func (r *SessionRepository) MarkAskUserReplied(chatID string, messageID string, replyText string) (*ChatMessage, error) {
	var session Session
	if err := r.db.First(&session, "chat_id = ?", chatID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}

	lock := r.getSessionLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	store, err := r.store(chatID)
	if err != nil {
		return nil, fmt.Errorf("mark ask user replied in transcript: %w", err)
	}
	updatedMsg, hasUnreplied, err := markAskUserReplied(store, messageID, replyText)
	if err != nil {
		return nil, fmt.Errorf("mark ask user replied in transcript: %w", err)
	}

	updates := map[string]any{
		"has_ask_user_unreplied": hasUnreplied,
		"updated_at":             time.Now(),
	}
	if err := r.db.Model(&Session{}).Where("chat_id = ?", chatID).Updates(updates).Error; err != nil {
		return nil, err
	}

	return updatedMsg, nil
}

// AppendArtifact appends an artifact path to a session's Artifacts list safely and deduplicated.
func (r *SessionRepository) AppendArtifact(chatID string, artifactPath string) error {
	if artifactPath == "" {
		return nil
	}
	return r.AppendArtifacts(chatID, []string{artifactPath})
}

// AppendArtifacts appends multiple artifact paths to a session's Artifacts list safely and deduplicated in a single transaction.
func (r *SessionRepository) AppendArtifacts(chatID string, artifactPaths []string) error {
	if chatID == "" || len(artifactPaths) == 0 {
		return nil
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		var session Session
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, "chat_id = ?", chatID).Error
		if err != nil {
			return err
		}

		existing := make(map[string]bool, len(session.Artifacts))
		for _, item := range session.Artifacts {
			existing[item] = true
		}

		modified := false
		for _, item := range artifactPaths {
			if item != "" && !existing[item] {
				session.Artifacts = append(session.Artifacts, item)
				existing[item] = true
				modified = true
			}
		}

		if !modified {
			return nil
		}
		return tx.Save(&session).Error
	})
}
