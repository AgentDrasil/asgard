package a2aagent

import (
	"context"
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
)

type mockClient struct {
	models []string
}

func (m *mockClient) Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	var usages []types.ModelUsage
	for _, model := range m.models {
		usages = append(usages, types.ModelUsage{Model: model, Remaining: 1.0})
	}
	return usages, nil
}

func (m *mockClient) Models(ctx context.Context, opts types.UsageOptions) ([]string, error) {
	return m.models, nil
}

func (m *mockClient) Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	return &types.PromptResult{}, nil
}

func TestServerReload(t *testing.T) {
	// Setup mock clients to make tests independent of installed CLIs
	mockClients := map[string]types.CLIClient{
		"agy":      &mockClient{models: []string{"Gemini 3.5 Flash (Low)"}},
		"opencode": &mockClient{models: []string{"gemini-2.5-flash"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	// Create a temporary agents directory
	tmpDir := t.TempDir()

	// Create subdirectories for loader verification
	err := os.MkdirAll(filepath.Join(tmpDir, "agents"), 0755)
	assert.NoError(t, err)

	// Create agentfather config explicitly since auto-initialization was removed
	fatherDir := filepath.Join(tmpDir, "agents", "agentfather")
	err = os.MkdirAll(fatherDir, 0755)
	assert.NoError(t, err)

	fatherYaml := `
id: "agentfather"
name: "Agent Father"
description: "The agent creates other agents."
cli:
  - cli: "agy"
    model: "Gemini 3.5 Flash (Low)"
`
	err = os.WriteFile(filepath.Join(fatherDir, "config.yaml"), []byte(fatherYaml), 0644)
	assert.NoError(t, err)

	// Set up config
	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)

	// Create Server
	srv, err := New(conf, testDB)
	assert.NoError(t, err)
	// Server starts with 1 agent: agentfather
	assert.Len(t, srv.agents, 1)
	assert.Equal(t, "agentfather", srv.agents[0].Config.ID)

	// Create a new agent configuration file dynamically
	agentDir := filepath.Join(tmpDir, "agents", "my-agent")
	err = os.MkdirAll(agentDir, 0755)
	assert.NoError(t, err)

	configYaml := `
id: "my-agent"
name: "My Agent"
description: "Dynamically added agent"
cli:
  - cli: "opencode"
    model: "gemini-2.5-flash"
`
	err = os.WriteFile(filepath.Join(agentDir, "config.yaml"), []byte(configYaml), 0644)
	assert.NoError(t, err)

	// Call POST /manage/reload via ServeHTTP
	req := httptest.NewRequest(http.MethodPost, "/manage/reload", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"success"`)

	// Verify that the new agent is loaded (total of 2 agents: agentfather + my-agent)
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	assert.Len(t, srv.agents, 2)
	assert.Equal(t, "agentfather", srv.agents[0].Config.ID)
	assert.Equal(t, "My Agent", srv.agents[1].Config.Name)
}
