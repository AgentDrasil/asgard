package dbmodels

type Session struct {
	// group_id/topic_id
	TopicID string `gorm:"primaryKey"`
	// name of current agent.
	CurrentAgent string
	// map of agents in json format.
	// TODO: make this a struct.
	Agents string
}

type Agent struct {
	Name      string `json:"name"`
	SessionID string `json:"session_id"`
}
