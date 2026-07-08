package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AgentDrasil/asgard/lib/agentwrapper"
	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/db"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

func TestTeamHandler(t *testing.T) {
	// Setup mock clients to satisfy Validate
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"Gemini 3.5 Flash (Low)"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tmpDir := t.TempDir()

	err := os.MkdirAll(filepath.Join(tmpDir, "agents"), 0755)
	assert.NoError(t, err)

	teamsYaml := `
teams:
  - team-red
  - team-blue
`
	err = os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte(teamsYaml), 0644)
	assert.NoError(t, err)

	// Create agent_father (required by Server.reload)
	fatherDir := filepath.Join(tmpDir, "agents", "agent_father")
	err = os.MkdirAll(fatherDir, 0755)
	assert.NoError(t, err)
	fatherYaml := `
id: "agent_father"
name: "Agent Father"
description: "The agent creates other agents."
team: "team-red"
cli:
  - cli: "agy"
    model: "Gemini 3.5 Flash (Low)"
`
	err = os.WriteFile(filepath.Join(fatherDir, "config.yaml"), []byte(fatherYaml), 0644)
	assert.NoError(t, err)

	// Create agent-alpha (team-red)
	alphaDir := filepath.Join(tmpDir, "agents", "agent_alpha")
	err = os.MkdirAll(alphaDir, 0755)
	assert.NoError(t, err)
	alphaYaml := `
id: "agent_alpha"
name: "Agent Alpha"
description: "Alpha red agent"
team: "team-red"
cli:
  - cli: "agy"
    model: "Gemini 3.5 Flash (Low)"
`
	err = os.WriteFile(filepath.Join(alphaDir, "config.yaml"), []byte(alphaYaml), 0644)
	assert.NoError(t, err)

	// Create agent-beta (team-red)
	betaDir := filepath.Join(tmpDir, "agents", "agent_beta")
	err = os.MkdirAll(betaDir, 0755)
	assert.NoError(t, err)
	betaYaml := `
id: "agent_beta"
name: "Agent Beta"
description: "Beta red agent"
team: "team-red"
cli:
  - cli: "agy"
    model: "Gemini 3.5 Flash (Low)"
`
	err = os.WriteFile(filepath.Join(betaDir, "config.yaml"), []byte(betaYaml), 0644)
	assert.NoError(t, err)

	// Create agent-gamma (team-blue)
	gammaDir := filepath.Join(tmpDir, "agents", "agent_gamma")
	err = os.MkdirAll(gammaDir, 0755)
	assert.NoError(t, err)
	gammaYaml := `
id: "agent_gamma"
name: "Agent Gamma"
description: "Gamma blue agent"
team: "team-blue"
cli:
  - cli: "agy"
    model: "Gemini 3.5 Flash (Low)"
`
	err = os.WriteFile(filepath.Join(gammaDir, "config.yaml"), []byte(gammaYaml), 0644)
	assert.NoError(t, err)

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	err = dbmodels.AutoMigrate(testDB)
	assert.NoError(t, err)
	repo := dbmodels.NewSessionRepository(testDB)

	srv, err := New(conf, testDB)
	assert.NoError(t, err)

	// 1. Missing chat_id parameter
	req := httptest.NewRequest(http.MethodGet, "/team", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `"error":"chat_id is required"`)

	// 2. Session not found
	req = httptest.NewRequest(http.MethodGet, "/team?chat_id=nonexistent", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), `"error":"session or current agent not found"`)

	// 3. Normal flow: set session to Agent Alpha (team-red), expect Agent Father and Agent Beta
	session := &dbmodels.Session{
		ChatID:       "chat-123",
		CurrentAgent: "Agent Alpha",
	}
	err = repo.SaveSession(session)
	assert.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/team?chat_id=chat-123", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var returnedAgents []string
	err = json.Unmarshal(w.Body.Bytes(), &returnedAgents)
	assert.NoError(t, err)

	// Should contain agent_father and agent_beta, but not agent_alpha (current) or agent_gamma (team-blue)
	assert.Len(t, returnedAgents, 2)

	ids := make(map[string]bool)
	for _, id := range returnedAgents {
		ids[id] = true
	}
	assert.True(t, ids["agent_father"])
	assert.True(t, ids["agent_beta"])
	assert.False(t, ids["agent_alpha"])
	assert.False(t, ids["agent_gamma"])
}
