package api

import (
	"encoding/json"
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

	handler, card := NewAgentHandler(agent, "http://localhost:8080", repo, nil, "")
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

func TestHandleAgents(t *testing.T) {
	srv := &Server{
		agents: []*agents.Agent{
			{
				Config: agents.AgentConfig{
					Name: "Agent Alpha",
				},
			},
			{
				Config: agents.AgentConfig{
					Name: "Agent Beta",
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	w := httptest.NewRecorder()
	srv.handleAgents(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var returnedAgents []AgentInfo
	err := json.Unmarshal(w.Body.Bytes(), &returnedAgents)
	assert.NoError(t, err)

	assert.Equal(t, 2, len(returnedAgents))
	assert.Equal(t, "Agent Alpha", returnedAgents[0].Name)
	assert.Equal(t, "Agent Beta", returnedAgents[1].Name)
}
