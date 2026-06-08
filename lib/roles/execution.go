package roles

// ExecutionResult represents the output of an agent execution.
type ExecutionResult struct {
	SessionID string     `json:"session_id"`
	Response  string     `json:"response"`
	Stats     Stats      `json:"stats"`
	RawOutput []byte     `json:"-"`
}

// Stats contains various performance metrics.
type Stats struct {
	Tokens TokenStats `json:"tokens"`
}

// TokenStats tracks token usage.
type TokenStats struct {
	Input      int `json:"input"`
	Prompt     int `json:"prompt"`
	Candidates int `json:"candidates"`
	Total      int `json:"total"`
	Cached     int `json:"cached"`
	Thoughts   int `json:"thoughts"`
}
