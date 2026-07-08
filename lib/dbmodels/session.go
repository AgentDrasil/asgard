package dbmodels

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

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

type Session struct {
	ChatID string `gorm:"primaryKey"`
	// name of current agent.
	CurrentAgent string
	// map of agents in json format.
	Agents Agents `gorm:"type:text"`
	// Dir agent running on
	RunDir string
}

type Agent struct {
	Name      string `json:"name"`
	SessionID string `json:"session_id"`
}

type SessionRepository struct {
	db *gorm.DB
}

func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
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

	session.CurrentAgent = agentName

	return r.SaveSession(session)
}
