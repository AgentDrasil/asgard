package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

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

func TestManageConfig_GetContent_SameOrigin(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
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
    model: "test-model"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	cfgFilePath := filepath.Join(tmpDir, "custom-config.yaml")
	sampleContent := "debug: true\nhost: 127.0.0.1\n"
	require.NoError(t, os.WriteFile(cfgFilePath, []byte(sampleContent), 0644))

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB, WithConfigPath(cfgFilePath))
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/manage/config", nil)
	req.Host = "localhost:8080"
	req.Header.Set("Origin", "http://localhost:8080")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp ConfigRawResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, cfgFilePath, resp.Path)
	assert.Equal(t, sampleContent, resp.Content)
	assert.True(t, resp.Exists)
}

func TestManageConfig_GetContent_CrossOrigin_Rejected(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
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
    model: "test-model"
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

	req := httptest.NewRequest(http.MethodGet, "/api/manage/config", nil)
	req.Host = "192.168.1.100:8080"
	req.RemoteAddr = "192.168.1.50:12345"
	req.Header.Set("Origin", "http://attacker.com")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "cross-origin manage request rejected")
}

func TestManageConfig_Put_LoopbackDevAllowed(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
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
    model: "test-model"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	cfgFilePath := filepath.Join(tmpDir, "dev-config.yaml")
	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB, WithConfigPath(cfgFilePath))
	require.NoError(t, err)

	validContent := `
debug: true
db: sqlite
dsn: dev.db
agent_dir: "` + tmpDir + `"
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-2.5-flash
`
	body, _ := json.Marshal(SaveConfigRawRequest{Content: validContent})
	req := httptest.NewRequest(http.MethodPut, "/api/manage/config", strings.NewReader(string(body)))
	req.Host = "localhost:8080"
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Origin", "http://localhost:8082") // Vite dev server port
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	saved, err := os.ReadFile(cfgFilePath)
	require.NoError(t, err)
	assert.Equal(t, validContent, string(saved))
}

func TestManageConfig_PutValidContent_AtomicAndFallback(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
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
    model: "test-model"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	cfgFilePath := filepath.Join(tmpDir, "config.yaml")
	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB, WithConfigPath(cfgFilePath))
	require.NoError(t, err)

	validContent := `
debug: true
db: sqlite
dsn: live.db
agent_dir: "` + tmpDir + `"
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-2.5-flash
`
	body, _ := json.Marshal(SaveConfigRawRequest{Content: validContent})
	req := httptest.NewRequest(http.MethodPut, "/api/manage/config", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	saved, err := os.ReadFile(cfgFilePath)
	require.NoError(t, err)
	assert.Equal(t, validContent, string(saved))
}

func TestManageConfig_PutInvalidContent_Rejection(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
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
    model: "test-model"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	cfgFilePath := filepath.Join(tmpDir, "config.yaml")
	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB, WithConfigPath(cfgFilePath))
	require.NoError(t, err)

	// Missing required fields
	invalidContent := `
debug: true
db: mysql
`
	body, _ := json.Marshal(SaveConfigRawRequest{Content: invalidContent})
	req := httptest.NewRequest(http.MethodPut, "/api/manage/config", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid configuration")
}

func TestManageHandler_SaveConfig_Providers_Valid(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
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
    model: "test-model"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	cfgFilePath := filepath.Join(tmpDir, "config.yaml")
	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB, WithConfigPath(cfgFilePath))
	require.NoError(t, err)

	validContent := `
debug: true
db: sqlite
dsn: live.db
agent_dir: "` + tmpDir + `"
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-2.5-flash
providers:
  - simplest
`
	body, _ := json.Marshal(SaveConfigRawRequest{Content: validContent})
	req := httptest.NewRequest(http.MethodPut, "/api/manage/config", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	saved, err := os.ReadFile(cfgFilePath)
	require.NoError(t, err)
	assert.Equal(t, validContent, string(saved))
}

func TestManageHandler_SaveConfig_Providers_Invalid(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
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
    model: "test-model"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	cfgFilePath := filepath.Join(tmpDir, "config.yaml")
	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB, WithConfigPath(cfgFilePath))
	require.NoError(t, err)

	invalidContent := `
debug: true
db: sqlite
dsn: live.db
agent_dir: "` + tmpDir + `"
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-2.5-flash
providers:
  - unknown-provider
`
	body, _ := json.Marshal(SaveConfigRawRequest{Content: invalidContent})
	req := httptest.NewRequest(http.MethodPut, "/api/manage/config", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `unsupported provider \"unknown-provider\"`)
}

func TestManageConfig_Put_RenameFallbackOnMountErrors(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tests := []struct {
		name           string
		renameErr      error
		expectedStatus int
		expectSaved    bool
	}{
		{
			name:           "fallback on EXDEV (cross-device/bind-mount)",
			renameErr:      syscall.EXDEV,
			expectedStatus: http.StatusOK,
			expectSaved:    true,
		},
		{
			name:           "fallback on EBUSY (mountpoint busy)",
			renameErr:      syscall.EBUSY,
			expectedStatus: http.StatusOK,
			expectSaved:    true,
		},
		{
			name:           "failure on unexpected rename error",
			renameErr:      syscall.EPERM,
			expectedStatus: http.StatusInternalServerError,
			expectSaved:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "agents", "agent_father"), 0755))

			fatherYaml := `
id: "agent_father"
name: "Agent Father"
description: "Root agent"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "test-model"
`
			require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))
			require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

			cfgFilePath := filepath.Join(tmpDir, "config.yaml")
			conf := &config.Config{
				AgentDir: tmpDir,
				Port:     8080,
			}

			testDB := db.NewDBForTest(t)
			srv, err := New(conf, testDB, WithConfigPath(cfgFilePath))
			require.NoError(t, err)

			t.Cleanup(func() { osRename = os.Rename })
			osRename = func(_, _ string) error {
				return tc.renameErr
			}

			validContent := `
debug: true
db: sqlite
dsn: live.db
agent_dir: "` + tmpDir + `"
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-2.5-flash
`
			body, _ := json.Marshal(SaveConfigRawRequest{Content: validContent})
			req := httptest.NewRequest(http.MethodPut, "/api/manage/config", strings.NewReader(string(body)))
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			assert.Equal(t, tc.expectedStatus, w.Code)
			if tc.expectSaved {
				saved, err := os.ReadFile(cfgFilePath)
				require.NoError(t, err)
				assert.Equal(t, validContent, string(saved))
			}

			// Ensure no config-*.tmp leftover files remain in dir
			tmpMatches, err := filepath.Glob(filepath.Join(tmpDir, "config-*.tmp"))
			require.NoError(t, err)
			assert.Empty(t, tmpMatches, "temporary files should be cleaned up")
		})
	}
}

func TestManageRestart_TriggerCalled(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
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
    model: "test-model"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	var count atomic.Int32
	mockTrigger := func() {
		count.Add(1)
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB, WithRestartTrigger(mockTrigger))
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/manage/restart", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "server restart initiated")

	require.Eventually(t, func() bool {
		return count.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "restart trigger should fire exactly once")
}

func TestWriteConfigDirect(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.yaml")
	err := writeConfigDirect(path, "key: value\n")
	require.NoError(t, err)

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "key: value\n", string(content))

	// Directory path causes OpenFile error
	err = writeConfigDirect(tmpDir, "key: value\n")
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

func TestServerReload_UnknownModel_SoftPass(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"unknown-provider/secret-model-v1"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "agents", "agent_father"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	fatherYaml := `
id: "agent_father"
name: "Agent Father"
description: "Root agent"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "unknown-provider/secret-model-v1"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB)
	require.NoError(t, err)

	// Soft pass allows normal startup and reload
	req := httptest.NewRequest(http.MethodPost, "/api/manage/reload", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"success"`)

	snap := srv.Diagnostics().Snapshot()
	assert.Equal(t, "ok", snap.Status) // Warnings do not degrade status to error
	assert.Empty(t, snap.Errors)
	require.Len(t, snap.Warnings, 1)
	assert.Contains(t, snap.Warnings[0], `Agent "agent_father" uses uncataloged model "unknown-provider/secret-model-v1"; falling back to 1M default context window`)
}

func TestServerReload_DiagnosticsReset(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"unknown-model-xyz", "gemini-3.7-flash-high"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "agents", "agent_father"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	unknownYaml := `
id: "agent_father"
name: "Agent Father"
description: "Root agent"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "unknown-model-xyz"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(unknownYaml), 0644))

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB)
	require.NoError(t, err)

	snap1 := srv.Diagnostics().Snapshot()
	require.Len(t, snap1.Warnings, 1)
	assert.Contains(t, snap1.Warnings[0], "unknown-model-xyz")

	// Update to known model and reload
	knownYaml := `
id: "agent_father"
name: "Agent Father"
description: "Root agent"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "gemini-3.7-flash-high"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(knownYaml), 0644))

	req := httptest.NewRequest(http.MethodPost, "/api/manage/reload", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	snap2 := srv.Diagnostics().Snapshot()
	assert.Empty(t, snap2.Warnings)
}

func TestServerReload_DisplayNameModelWarning(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"Claude 3.7 Sonnet (Thinking)"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "agents", "agent_father"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	fatherYaml := `
id: "agent_father"
name: "Agent Father"
description: "Root agent"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "Claude 3.7 Sonnet (Thinking)"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "agents", "agent_father", "config.yaml"), []byte(fatherYaml), 0644))

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	srv, err := New(conf, testDB)
	require.NoError(t, err)

	// Display name models soft-pass startup/reload with a warning in diagnostics
	req := httptest.NewRequest(http.MethodPost, "/api/manage/reload", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"success"`)

	snap := srv.Diagnostics().Snapshot()
	assert.Equal(t, "ok", snap.Status)
	assert.Empty(t, snap.Errors)
	require.Len(t, snap.Warnings, 1)
	assert.Contains(t, snap.Warnings[0], `Agent "agent_father" uses uncataloged model "Claude 3.7 Sonnet (Thinking)"; falling back to 1M default context window`)
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
