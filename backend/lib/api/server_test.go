package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
