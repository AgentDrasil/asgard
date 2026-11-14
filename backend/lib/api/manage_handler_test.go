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
	"github.com/AgentDrasil/asgard/pkg/version"
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

	// Reload agents dynamically
	err = srv.ReloadAgents()
	require.NoError(t, err)

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
		UILang:   "zh-CN",
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
	assert.Contains(t, w.Body.String(), `"default_ui_lang":"zh-CN"`)
}

func TestSystemStatusHandler_OkAndDegraded(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"gemini-3.7-flash-high"}},
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
    model: "gemini-3.7-flash-high"
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
	assert.Equal(t, version.Get(), snap.Version)

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

	// Write corrupted agent config so subsequent reloads will fail
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte("invalid: yaml: : ["), 0644))

	// First reload failure
	err = srv.ReloadAgents()
	require.Error(t, err)

	snap1 := srv.Diagnostics().Snapshot()
	assert.Equal(t, "degraded", snap1.Status)
	assert.Len(t, snap1.Errors, 1)

	// Second reload failure
	err = srv.ReloadAgents()
	require.Error(t, err)

	snap2 := srv.Diagnostics().Snapshot()
	assert.Equal(t, "degraded", snap2.Status)
	// Must not accumulate duplicate errors
	assert.Len(t, snap2.Errors, 1)
	assert.Equal(t, snap1.Errors, snap2.Errors)
}

func TestWriteConfigDirect(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.yaml")
	err := writeConfigDirect(path, "key: value\n", 0644)
	require.NoError(t, err)

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "key: value\n", string(content))

	// Directory path causes OpenFile error
	err = writeConfigDirect(tmpDir, "key: value\n", 0644)
	assert.Error(t, err)
}

func TestServerReload_KnownModels(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy":      &mockClient{models: []string{"gemini-3.7-flash-high"}},
		"opencode": &mockClient{models: []string{"claude-sonnet-4-6"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "agents", "agent_father"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "agents", "agent_worker"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	fatherYaml := `
id: "agent_father"
name: "Agent Father"
description: "Root agent"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "gemini-3.7-flash-high"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))

	workerYaml := `
id: "agent_worker"
name: "Agent Worker"
description: "Worker agent"
run_dirs: ["/tmp"]
cli:
  - cli: "opencode"
    model: "claude-sonnet-4-6"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_worker", "config.yaml"), []byte(workerYaml), 0644))

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB)
	require.NoError(t, err)

	snap := srv.Diagnostics().Snapshot()
	assert.Equal(t, "ok", snap.Status)
	assert.Empty(t, snap.Errors)
	assert.Empty(t, snap.Warnings)
}

func TestSystemLogsHandler(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"gemini-3.7-flash-high"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "agents", "agent_father"), 0755))

	fatherYaml := `
id: "agent_father"
name: "Agent Father"
description: "Root agent"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "gemini-3.7-flash-high"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB)
	require.NoError(t, err)

	// Add test entries
	srv.Diagnostics().AddError("config", "corrupted syntax")
	srv.Diagnostics().AddWarning("ssh", "key missing")

	// 1. GET /api/system/logs (all)
	reqAll := httptest.NewRequest(http.MethodGet, "/api/system/logs", nil)
	wAll := httptest.NewRecorder()
	srv.ServeHTTP(wAll, reqAll)
	assert.Equal(t, http.StatusOK, wAll.Code)

	var respAll SystemLogsResponse
	require.NoError(t, json.Unmarshal(wAll.Body.Bytes(), &respAll))
	require.Len(t, respAll.Logs, 2)
	assert.Equal(t, "error", respAll.Logs[0].Level)
	assert.Equal(t, "config", respAll.Logs[0].Source)
	assert.Equal(t, "corrupted syntax", respAll.Logs[0].Message)
	assert.Equal(t, "warn", respAll.Logs[1].Level)
	assert.Equal(t, "ssh", respAll.Logs[1].Source)
	assert.Equal(t, "key missing", respAll.Logs[1].Message)

	// 2. GET /api/system/logs?level=warn
	reqWarn := httptest.NewRequest(http.MethodGet, "/api/system/logs?level=warn", nil)
	wWarn := httptest.NewRecorder()
	srv.ServeHTTP(wWarn, reqWarn)
	assert.Equal(t, http.StatusOK, wWarn.Code)

	var respWarn SystemLogsResponse
	require.NoError(t, json.Unmarshal(wWarn.Body.Bytes(), &respWarn))
	require.Len(t, respWarn.Logs, 1)
	assert.Equal(t, "warn", respWarn.Logs[0].Level)
	assert.Equal(t, "ssh", respWarn.Logs[0].Source)

	// 3. GET /api/system/logs?level=error
	reqErr := httptest.NewRequest(http.MethodGet, "/api/system/logs?level=error", nil)
	wErr := httptest.NewRecorder()
	srv.ServeHTTP(wErr, reqErr)
	assert.Equal(t, http.StatusOK, wErr.Code)

	var respErr SystemLogsResponse
	require.NoError(t, json.Unmarshal(wErr.Body.Bytes(), &respErr))
	require.Len(t, respErr.Logs, 1)
	assert.Equal(t, "error", respErr.Logs[0].Level)
	assert.Equal(t, "config", respErr.Logs[0].Source)
}
