package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
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

	// Write teams.yaml
	teamsYaml := `
teams:
  - my-team
`
	err = os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte(teamsYaml), 0644)
	assert.NoError(t, err)

	// Create agentfather config explicitly since auto-initialization was removed
	fatherDir := filepath.Join(tmpDir, "agents", "agent_father")
	err = os.MkdirAll(fatherDir, 0755)
	assert.NoError(t, err)

	fatherYaml := `
id: "agent_father"
name: "Agent Father"
description: "The agent creates other agents."
team: "my-team"
run_dirs: ["/tmp"]
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
	// Server starts with 1 agent: agent_father
	assert.Len(t, srv.agents, 1)
	assert.Equal(t, "agent_father", srv.agents[0].Config.ID)

	// Create a new agent configuration file dynamically
	agentDir := filepath.Join(tmpDir, "agents", "my-agent")
	err = os.MkdirAll(agentDir, 0755)
	assert.NoError(t, err)

	configYaml := `
id: "my-agent"
name: "My Agent"
description: "Dynamically added agent"
team: "my-team"
run_dirs: ["/tmp"]
cli:
  - cli: "opencode"
    model: "gemini-2.5-flash"
`
	err = os.WriteFile(filepath.Join(agentDir, "config.yaml"), []byte(configYaml), 0644)
	assert.NoError(t, err)

	// Call POST /api/manage/reload via ServeHTTP
	req := httptest.NewRequest(http.MethodPost, "/api/manage/reload", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"success"`)

	// Verify that the new agent is loaded (total of 2 agents: agent_father + my-agent)
	srv.mu.RLock()
	t.Cleanup(srv.mu.RUnlock)
	assert.Len(t, srv.agents, 2)
	assert.Equal(t, "agent_father", srv.agents[0].Config.ID)
	assert.Equal(t, "My Agent", srv.agents[1].Config.Name)
}

func TestServerConfig(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tmpDir := t.TempDir()
	err := os.MkdirAll(filepath.Join(tmpDir, "agents", "agent_father"), 0755)
	assert.NoError(t, err)

	fatherYaml := `
id: "agent_father"
name: "Agent Father"
description: "Root agent"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "test-model"
`
	err = os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644)
	assert.NoError(t, err)

	teamsYaml := `
teams:
  - my-team
`
	err = os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte(teamsYaml), 0644)
	assert.NoError(t, err)

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
		FirebaseWebpushWeb: &config.FirebaseWebpushWebConfig{
			APIKey:   "test-key",
			VapidKey: "test-vapid",
		},
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB)
	assert.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"apiKey":"test-key"`)
	assert.Contains(t, w.Body.String(), `"vapidKey":"test-vapid"`)
}

func TestSystemStatusHandler_OkAndDegraded(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tmpDir := t.TempDir()
	err := os.MkdirAll(filepath.Join(tmpDir, "agents", "agent_father"), 0755)
	require.NoError(t, err)

	fatherYaml := `
id: "agent_father"
name: "Agent Father"
description: "Root agent"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "test-model"
`
	err = os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644)
	require.NoError(t, err)

	teamsYaml := `
teams:
  - my-team
`
	err = os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte(teamsYaml), 0644)
	require.NoError(t, err)

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB)
	require.NoError(t, err)

	// 1. Initial status -> "ok"
	req := httptest.NewRequest(http.MethodGet, "/api/system/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var snap DiagnosticsSnapshot
	err = json.Unmarshal(w.Body.Bytes(), &snap)
	require.NoError(t, err)
	assert.Equal(t, "ok", snap.Status)
	assert.Empty(t, snap.Errors)

	// 2. Add error -> "degraded"
	srv.Diagnostics().AddError("config", "corrupted syntax")
	srv.Diagnostics().AddWarning("ssh", "key missing")

	req2 := httptest.NewRequest(http.MethodGet, "/api/system/status", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)

	var snap2 DiagnosticsSnapshot
	err = json.Unmarshal(w2.Body.Bytes(), &snap2)
	require.NoError(t, err)
	assert.Equal(t, "degraded", snap2.Status)
	assert.Equal(t, []string{"corrupted syntax"}, snap2.Errors)
	assert.Equal(t, []string{"key missing"}, snap2.Warnings)
}

func TestHandleReload_DeduplicatesDiagnostics(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tmpDir := t.TempDir()
	err := os.MkdirAll(filepath.Join(tmpDir, "agents", "agent_father"), 0755)
	require.NoError(t, err)

	fatherYaml := `
id: "agent_father"
name: "Agent Father"
description: "Root agent"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "test-model"
`
	err = os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644)
	require.NoError(t, err)

	teamsYaml := `
teams:
  - my-team
`
	err = os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte(teamsYaml), 0644)
	require.NoError(t, err)

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB)
	require.NoError(t, err)

	// Remove agent_father directory so subsequent reloads will fail
	require.NoError(t, os.RemoveAll(filepath.Join(tmpDir, "agents", "agent_father")))

	// First reload failure
	req1 := httptest.NewRequest(http.MethodPost, "/api/manage/reload", nil)
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusInternalServerError, w1.Code)

	snap1 := srv.Diagnostics().Snapshot()
	assert.Equal(t, "degraded", snap1.Status)
	assert.Len(t, snap1.Errors, 1)

	// Second reload failure
	req2 := httptest.NewRequest(http.MethodPost, "/api/manage/reload", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusInternalServerError, w2.Code)

	snap2 := srv.Diagnostics().Snapshot()
	assert.Equal(t, "degraded", snap2.Status)
	// Must not accumulate duplicate errors
	assert.Len(t, snap2.Errors, 1)
	assert.Equal(t, snap1.Errors, snap2.Errors)
}
