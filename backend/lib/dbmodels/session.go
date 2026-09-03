package dbmodels

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/moznion/go-optional"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
)

type Agent struct {
	Name string `json:"name"`
	// Sessions maps "<cli>/<model>" → session ID returned by that CLI.
	// For sequential agents this map has at most one entry.
	// For fresh-mode agents this map is not written.
	Sessions map[string]string `json:"sessions,omitempty"`
	Status   AgentStatus       `json:"status,omitempty"`
}

func defaultSessionDir(chatID string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "data", chatID)
	}
	return filepath.Join(os.TempDir(), chatID)
}

type SessionRepository struct {
	db             *gorm.DB
	sessionDirFunc func(chatID string) string
	sessionLocks   sync.Map // chatID string -> *sync.Mutex
}

func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{
		db:             db,
		sessionDirFunc: defaultSessionDir,
	}
}

// SetSessionDirFunc overrides the directory resolver function (used for testing isolation).
func (r *SessionRepository) SetSessionDirFunc(fn func(chatID string) string) {
	r.sessionDirFunc = fn
}

func (r *SessionRepository) sessionDir(chatID string) string {
	if r.sessionDirFunc != nil {
		return r.sessionDirFunc(chatID)
	}
	return defaultSessionDir(chatID)
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

	// Clean physical session directory (messages.jsonl, workflows, etc.)
	dir := r.sessionDir(chatID)
	if dir != "" {
		_ = os.RemoveAll(dir)
	}

	// Remove lock entry from map while holding lock to avoid leaks
	r.sessionLocks.Delete(chatID)
	return nil
}

// GetSession retrieves the session for a given chat ID and loads messages from messages.jsonl.
func (r *SessionRepository) GetSession(chatID string) (*Session, error) {
	var session Session
	err := r.db.First(&session, "chat_id = ?", chatID).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}

	// Read messages from transcript file
	msgs, err := ReadMessages(r.sessionDir(chatID))
	if err != nil {
		return nil, fmt.Errorf("read session transcript: %w", err)
	}
	session.Messages = msgs
	return &session, nil
}

// SaveSession saves or updates the session metadata and writes messages to messages.jsonl if len(Messages) > 0.
func (r *SessionRepository) SaveSession(session *Session) error {
	if len(session.Messages) > 0 {
		lock := r.getSessionLock(session.ChatID)
		lock.Lock()
		defer lock.Unlock()

		dir := r.sessionDir(session.ChatID)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create session dir: %w", err)
		}
		path := TranscriptFilePath(dir)
		if err := writeAllMessagesAtomic(dir, path, session.Messages); err != nil {
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
		return tx.Model(&existing).Updates(map[string]any{
			"title":         session.Title,
			"current_agent": session.CurrentAgent,
			"run_dir":       session.RunDir,
		}).Error
	})
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
// optionally updates the run directory. cliKey has the format "<cli>/<model>".
// Pass an empty sessionID to skip updating the session map entry.
func (r *SessionRepository) UpdateAgentSession(chatID string, agentID string, cliKey string, sessionID string, runDirOpt optional.Option[string]) error {
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

// GetAgentSessions returns the sessions map for a specific agent in a chat.
func (r *SessionRepository) GetAgentSessions(chatID string, agentID string) (map[string]string, error) {
	session, err := r.GetSession(chatID)
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

	dir := r.sessionDir(chatID)
	appended, err := AppendMessage(dir, msg)
	if err != nil {
		return fmt.Errorf("append transcript message: %w", err)
	}

	summary := TruncateSummary(msg.Content, 200)

	// Re-check unreplied ask_user state in transcript
	msgs, readErr := ReadMessages(dir)
	hasUnreplied := false
	if readErr == nil {
		for _, m := range msgs {
			if m.Role == "ask_user" && !m.Replied {
				hasUnreplied = true
				break
			}
		}
	} else {
		// Fallback to local logic if read fails
		hasUnreplied = session.HasAskUserUnreplied || (msg.Role == "ask_user" && !msg.Replied)
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

	dir := r.sessionDir(chatID)
	updatedMsg, hasUnreplied, err := MarkAskUserReplied(dir, messageID, replyText)
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
