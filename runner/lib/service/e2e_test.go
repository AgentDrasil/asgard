package service

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/api"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

// newAPIServer builds the real HTTP API whose per-chat file access goes
// through resolve (here: a gRPC client talking to a real Service).
func newAPIServer(t *testing.T, resolve func(string) (runnerfs.FS, error)) (*api.Server, *dbmodels.SessionRepository) {
	t.Helper()
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	srv, err := api.New(&config.Config{Host: "http://localhost:8080"}, testDB, api.WithRunnerFS(resolve))
	require.NoError(t, err)
	return srv, dbmodels.NewSessionRepository(testDB)
}

func TestAPIOverRunnerService(t *testing.T) {
	cl, ws := startService(t, Options{})
	srv, repo := newAPIServer(t, func(id string) (runnerfs.FS, error) { return runnerfs.NewRemote(cl, id), nil })

	require.NoError(t, os.WriteFile(filepath.Join(ws, "notes.md"), []byte("# hi"), 0o644))
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chat, Title: "t", RunDir: ws, Artifacts: []string{"notes.md", "gone.md"}}))

	// Workspace file: authorized artifact is read through the runner.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chat+"&path=notes.md", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var wf api.WorkspaceFileResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &wf))
	assert.Equal(t, "# hi", wf.Content)
	assert.Equal(t, "md", wf.Ext)

	// Not an artifact: denied; outside the workspace: denied.
	require.NoError(t, os.WriteFile(filepath.Join(ws, "other.md"), []byte("x"), 0o644))
	for path, want := range map[string]int{"other.md": http.StatusForbidden, "../escape": http.StatusForbidden, "gone.md": http.StatusNotFound, "unlisted-missing.md": http.StatusForbidden} {
		rr = httptest.NewRecorder()
		srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chat+"&path="+path, nil))
		assert.Equal(t, want, rr.Code, path)
	}

	// Attachment upload then ranged download; the backend host has no copy.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("files", "data.txt")
	require.NoError(t, err)
	_, _ = part.Write([]byte("0123456789"))
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+chat+"/attachments", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var atts []dbmodels.Attachment
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &atts))
	require.Len(t, atts, 1)
	assert.Equal(t, "data.txt", atts[0].Name)
	assert.Equal(t, "text/plain; charset=utf-8", atts[0].MimeType)
	assert.FileExists(t, filepath.Join(paths.AttachmentsDir(chat), "data.txt"))

	req = httptest.NewRequest(http.MethodGet, "/api/sessions/"+chat+"/attachments/data.txt", nil)
	req.Header.Set("Range", "bytes=3-5")
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusPartialContent, rr.Code)
	assert.Equal(t, "345", rr.Body.String())
	assert.Equal(t, "bytes 3-5/10", rr.Header().Get("Content-Range"))
}

func TestAPIRunnerOfflineIs503(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, repo := newAPIServer(t, func(string) (runnerfs.FS, error) {
		return nil, runnerfs.Errf(runnerfs.CodeUnavailable, "runner offline")
	})
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chat, Title: "t"}))

	for _, tc := range []struct{ method, url string }{
		{http.MethodGet, "/api/v1/workspace/file?session_id=" + chat + "&path=a.md"},
		{http.MethodGet, "/api/sessions/" + chat + "/attachments/a.txt"},
		{http.MethodPost, "/api/sessions/" + chat + "/attachments"},
	} {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, _ := mw.CreateFormFile("files", "a.txt")
		_, _ = part.Write([]byte("x"))
		_ = mw.Close()
		req := httptest.NewRequest(tc.method, tc.url, &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusServiceUnavailable, rr.Code, tc.url)
	}
}
