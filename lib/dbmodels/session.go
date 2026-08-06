package dbmodels

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/moznion/go-optional"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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

type ChatMessage struct {
	ID           string `json:"id"`
	Role         string `json:"role"`
	Content      string `json:"content"`
	AgentName    string `json:"agentName,omitempty"`
	Timestamp    int64  `json:"timestamp,omitempty"`
	ActivityType string `json:"activityType,omitempty"`
	StepIndex    int    `json:"stepIndex,omitempty"`
	InputTokens  int    `json:"inputTokens,omitempty"`
	MaxTokens    int    `json:"maxTokens,omitempty"`
	Replied      bool   `json:"replied,omitempty"`
	ReplyText    string `json:"replyText,omitempty"`
}

type Messages []ChatMessage

// Value implements driver.Valuer
func (m Messages) Value() (driver.Value, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return json.Marshal(m)
}

// Scan implements sql.Scanner
func (m *Messages) Scan(value interface{}) error {
	if value == nil {
		*m = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("failed to scan Messages: unsupported type %T", value)
	}
	return json.Unmarshal(bytes, m)
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
	// Messages of the session
	Messages Messages `gorm:"type:text"`

	CreatedAt time.Time
	UpdatedAt time.Time
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

type SessionRepository struct {
	db *gorm.DB
}

func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

// GetSessions retrieves all sessions.
func (r *SessionRepository) GetSessions() ([]Session, error) {
	var sessions []Session
	err := r.db.Order("updated_at desc").Limit(20).Find(&sessions).Error
	return sessions, err
}

// DeleteSession deletes a session by chat ID.
func (r *SessionRepository) DeleteSession(chatID string) error {
	return r.db.Delete(&Session{}, "chat_id = ?", chatID).Error
}

// GetSession retrieves the session for a given chat ID.
func (r *SessionRepository) GetSession(chatID string) (*Session, error) {
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

// SaveSession saves or updates the session.
func (r *SessionRepository) SaveSession(session *Session) error {
	return r.db.Save(session).Error
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
				Status: AgentStatusRunning,
			}
			if sessionID != "" && cliKey != "" {
				newAgent.Sessions = map[string]string{cliKey: sessionID}
			}
			sessPtr.Agents = append(sessPtr.Agents, newAgent)
		}

		return tx.Save(sessPtr).Error
	})
}

// GetAgentSessions returns the sessions map for a specific agent in a chat.
// Returns nil if the session or agent is not found.
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
func (r *SessionRepository) AppendMessage(chatID string, msg ChatMessage) error {
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
		sessPtr.Messages = append(sessPtr.Messages, msg)
		return tx.Save(sessPtr).Error
	})
}

// MarkAskUserReplied marks an ask_user ChatMessage as replied and sets its reply text.
func (r *SessionRepository) MarkAskUserReplied(chatID string, messageID string, replyText string) error {
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
		for i, m := range session.Messages {
			if m.ID == messageID || (m.Role == "ask_user" && !m.Replied && messageID == "") {
				session.Messages[i].Replied = true
				session.Messages[i].ReplyText = replyText
				found = true
				break
			}
		}

		if !found {
			for i := len(session.Messages) - 1; i >= 0; i-- {
				if session.Messages[i].Role == "ask_user" && !session.Messages[i].Replied {
					session.Messages[i].Replied = true
					session.Messages[i].ReplyText = replyText
					break
				}
			}
		}

		return tx.Save(&session).Error
	})
}
