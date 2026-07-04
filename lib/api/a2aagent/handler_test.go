package a2aagent

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/db"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

func TestNewAgentHandler(t *testing.T) {
	testDB := db.NewDBForTest(t)
	repo := dbmodels.NewSessionRepository(testDB)

	agent := &agents.Agent{
		Config: agents.AgentConfig{
			ID:          "test-agent",
			Name:        "Test Agent",
			Description: "A test agent for testing A2A integration",
		},
		Path: "/dummy/path",
	}

	handler, card := NewAgentHandler(agent, repo)
	assert.NotNil(t, handler)
	assert.NotNil(t, card)
	assert.Equal(t, "Test Agent", card.Name)
	assert.Equal(t, "A test agent for testing A2A integration", card.Description)
	assert.Equal(t, "1.0.0", card.Version)

	// Test request to A2A REST handler
	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// A2A REST handler should return JSON content type
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}
