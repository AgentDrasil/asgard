package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/AgentDrasil/asgard/lib/config"
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
