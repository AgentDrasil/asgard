package dbmodels

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

type Session struct {
	// group_id/topic_id
	TopicID string `gorm:"primaryKey"`
	// name of current agent.
	CurrentAgent string
	// map of agents in json format.
	// TODO: make this a struct.
	Agents string
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

// GetSession retrieves the session for a given topic ID.
func (r *SessionRepository) GetSession(topicID string) (*Session, error) {
	var session Session
	err := r.db.First(&session, "topic_id = ?", topicID).Error
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

// UpdateAgentSession updates the session ID for a specific agent in a topic.
func (r *SessionRepository) UpdateAgentSession(topicID string, agentName string, sessionID string) error {
	session, err := r.GetSession(topicID)
	if err != nil {
		return err
	}

	if session == nil {
		// Create new session if it doesn't exist
		session = &Session{
			TopicID:      topicID,
			CurrentAgent: agentName,
		}
	}

	var agents []Agent
	if session.Agents != "" {
		if err := json.Unmarshal([]byte(session.Agents), &agents); err != nil {
			return fmt.Errorf("failed to unmarshal agents: %w", err)
		}
	}

	found := false
	for i, a := range agents {
		if a.Name == agentName {
			agents[i].SessionID = sessionID
			found = true
			break
		}
	}

	if !found {
		agents = append(agents, Agent{
			Name:      agentName,
			SessionID: sessionID,
		})
	}

	agentsJSON, err := json.Marshal(agents)
	if err != nil {
		return fmt.Errorf("failed to marshal agents: %w", err)
	}

	session.Agents = string(agentsJSON)
	session.CurrentAgent = agentName

	return r.SaveSession(session)
}
