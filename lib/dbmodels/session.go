package dbmodels

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/moznion/go-optional"
	"gorm.io/gorm"
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

type AgentStatus uint

const (
	AgentStatusUnknown AgentStatus = iota
	AgentStatusRunning
	AgentStatusCompleted
)

type Agent struct {
	Name      string      `json:"name"`
	SessionID string      `json:"session_id"`
	Status    AgentStatus `json:"status,omitempty"`
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
func (r *SessionRepository) UpdateAgentStatus(chatID string, agentName string, status AgentStatus) error {
	session, err := r.GetSession(chatID)
	if err != nil {
		return err
	}
	if session == nil {
		return nil
	}

	found := false
	for i, a := range session.Agents {
		if a.Name == agentName {
			session.Agents[i].Status = status
			found = true
			break
		}
	}
	if !found {
		session.Agents = append(session.Agents, Agent{
			Name:   agentName,
			Status: status,
		})
	}

	return r.SaveSession(session)
}

// UpdateAgentSession updates the session ID for a specific agent in a topic and optionally updates the run directory.
func (r *SessionRepository) UpdateAgentSession(chatID string, agentName string, sessionID string, runDirOpt optional.Option[string]) error {
	session, err := r.GetSession(chatID)
	if err != nil {
		return err
	}

	if session == nil {
		// Create new session if it doesn't exist
		session = &Session{
			ChatID:       chatID,
			CurrentAgent: agentName,
		}
	}

	if runDirOpt.IsSome() && runDirOpt.Unwrap() != "" {
		session.RunDir = runDirOpt.Unwrap()
	}

	found := false
	for i, a := range session.Agents {
		if a.Name == agentName {
			if sessionID != "" {
				session.Agents[i].SessionID = sessionID
			}
			found = true
			break
		}
	}

	if !found {
		session.Agents = append(session.Agents, Agent{
			Name:      agentName,
			SessionID: sessionID,
		})
	}

	return r.SaveSession(session)
}

// UpdateSessionTitle updates the title of a session by chat ID.
func (r *SessionRepository) UpdateSessionTitle(chatID string, title string) error {
	session, err := r.GetSession(chatID)
	if err != nil {
		return err
	}
	if session == nil {
		session = &Session{
			ChatID: chatID,
		}
	}
	session.Title = title
	return r.SaveSession(session)
}

// AppendMessage appends a ChatMessage to a session by chat ID.
func (r *SessionRepository) AppendMessage(chatID string, msg ChatMessage) error {
	session, err := r.GetSession(chatID)
	if err != nil {
		return err
	}
	if session == nil {
		session = &Session{
			ChatID: chatID,
		}
	}
	session.Messages = append(session.Messages, msg)
	return r.SaveSession(session)
}
