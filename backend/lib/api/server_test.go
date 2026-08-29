package api

import (
	"context"
	"encoding/json"
	"io"
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
)

func TestServer_WebUIHostingAndFallback(t *testing.T) {
	tempDir := t.TempDir()
	indexPath := filepath.Join(tempDir, "index.html")
	if err := os.WriteFile(indexPath, []byte("<html><body>Frontend Root</body></html>"), 0644); err != nil {
		t.Fatalf("failed to create index.html: %v", err)
	}

	assetsDir := filepath.Join(tempDir, "assets")
	if err := os.MkdirAll(assetsDir, 0755); err != nil {
		t.Fatalf("failed to create assets dir: %v", err)
	}
	jsPath := filepath.Join(assetsDir, "app.js")
	if err := os.WriteFile(jsPath, []byte("console.log('hello');"), 0644); err != nil {
		t.Fatalf("failed to create app.js: %v", err)
	}

	cfg := &config.Config{
		Host:      "localhost",
		WebUIPath: tempDir,
	}

	srv := &Server{
		conf: cfg,
	}

	mux := srv.buildMuxLocked()

	// 1. Existing static file request
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected status OK, got %d", res.StatusCode)
	}
	if string(body) != "console.log('hello');" {
		t.Errorf("expected JS content, got %s", string(body))
	}

	// 2. Client-side route fallback request
	req = httptest.NewRequest(http.MethodGet, "/dashboard/settings", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	res = rec.Result()
	body, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected status OK for fallback route, got %d", res.StatusCode)
	}
	if string(body) != "<html><body>Frontend Root</body></html>" {
		t.Errorf("expected index.html fallback content, got %s", string(body))
	}
}

func TestServer_WorkflowCronIntegration(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tempDir := t.TempDir()
	agentsDir := filepath.Join(tempDir, "agents")
	require.NoError(t, os.MkdirAll(filepath.Join(agentsDir, "agent_father"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "agent_father", "config.yaml"), []byte(`
id: "agent_father"
name: "Agent Father"
description: "Father"
team: "my-team"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "test-model"
`), 0644))

	cfg := &config.Config{
		AgentDir: tempDir,
	}
	srv, err := New(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, srv.cronManager)

	// Verify runWorkflowCronTrigger activeExecutions mutex guard
	chatID := "test-cron-guard"
	srv.activeExecutions.Store(chatID, struct{}{})
	err = srv.runWorkflowCronTrigger(context.Background(), nil, chatID, "", true)
	require.NoError(t, err) // Should skip silently
	srv.activeExecutions.Delete(chatID)

	// Verify Shutdown cleans up cronManager
	require.NoError(t, srv.Shutdown(context.Background()))
}

func TestServer_ReloadFailure_MuxInitialized_StatusAccessible(t *testing.T) {
	tempDir := t.TempDir()
	// Empty agents dir - reload will fail because agent_father is missing
	cfg := &config.Config{
		AgentDir: tempDir,
	}

	srv, err := New(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, srv)

	// Verify accessing GET /api/system/status works without panic
	req := httptest.NewRequest(http.MethodGet, "/api/system/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var snap DiagnosticsSnapshot
	err = json.Unmarshal(w.Body.Bytes(), &snap)
	require.NoError(t, err)
	assert.Equal(t, "degraded", snap.Status)
	assert.NotEmpty(t, snap.Errors)

	require.NoError(t, srv.Shutdown(context.Background()))
}

func TestServer_NilRepo_Returns503InDegradedMode(t *testing.T) {
	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tempDir := t.TempDir()
	agentsDir := filepath.Join(tempDir, "agents")
	require.NoError(t, os.MkdirAll(filepath.Join(agentsDir, "agent_father"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "agent_father", "config.yaml"), []byte(`
id: "agent_father"
name: "Agent Father"
description: "Father"
team: "my-team"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "test-model"
`), 0644))

	cfg := &config.Config{
		AgentDir: tempDir,
	}

	// Start server with nil DB / repo
	srv, err := New(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, srv)

	// 1. GET /api/sessions should return 503 Service Unavailable
	reqSessions := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	wSessions := httptest.NewRecorder()
	srv.ServeHTTP(wSessions, reqSessions)
	assert.Equal(t, http.StatusServiceUnavailable, wSessions.Code)
	assert.Contains(t, wSessions.Body.String(), "database unavailable in degraded mode")

	// 2. GET /api/agents should return 200 OK
	reqAgents := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	wAgents := httptest.NewRecorder()
	srv.ServeHTTP(wAgents, reqAgents)
	assert.Equal(t, http.StatusOK, wAgents.Code)

	var agents []AgentInfo
	err = json.Unmarshal(wAgents.Body.Bytes(), &agents)
	require.NoError(t, err)
	assert.Len(t, agents, 1)
	assert.Equal(t, "agent_father", agents[0].ID)

	require.NoError(t, srv.Shutdown(context.Background()))
}
