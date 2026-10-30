package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/api"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
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
	assert.FileExists(t, filepath.Join(runnerpaths.AttachmentsDir(chat), "data.txt"))

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

func getJSON(t *testing.T, srv http.Handler, url string, out any) int {
	t.Helper()
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, url, nil))
	if rr.Code == http.StatusOK && out != nil {
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), out), rr.Body.String())
	}
	return rr.Code
}

func TestAPIDirsAndGitOverRunnerService(t *testing.T) {
	cl, ws := startService(t, Options{})
	srv, repo := newAPIServer(t, func(id string) (runnerfs.FS, error) { return runnerfs.NewRemote(cl, id), nil })
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chat, Title: "t", RunDir: ws}))

	git(t, ws, "init")
	put(t, filepath.Join(ws, "src", "main.go"), "package main\n")
	put(t, filepath.Join(ws, "README.md"), "# r\n")
	git(t, ws, "add", ".")
	git(t, ws, "commit", "-m", "first")
	put(t, filepath.Join(ws, "src", "main.go"), "package main\n// edit\n")
	put(t, filepath.Join(ws, "new.txt"), "n\n")
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "escape")))

	// Tree: all pages are folded into one response.
	var tree api.FileTreeResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/files/tree?session_id="+chat, &tree))
	var names []string
	for _, e := range tree.Entries {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{"src", "README.md", "escape", "new.txt"}, names)
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/files/tree?session_id="+chat+"&path=src", &tree))
	require.Len(t, tree.Entries, 1)
	assert.Equal(t, "src/main.go", tree.Entries[0].Path)
	assert.Equal(t, http.StatusForbidden, getJSON(t, srv, "/api/files/tree?session_id="+chat+"&path=escape", nil))
	assert.Equal(t, http.StatusForbidden, getJSON(t, srv, "/api/files/tree?session_id="+chat+"&path=../x", nil))
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/files/tree?session_id="+chat+"&path=missing", nil))

	var content api.FileContentResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/files/content?session_id="+chat+"&path=src/main.go", &content))
	assert.Equal(t, "package main\n// edit\n", content.Content)
	assert.Equal(t, http.StatusForbidden, getJSON(t, srv, "/api/files/content?session_id="+chat+"&path=escape/x", nil))

	var search api.FileSearchResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/files/search?session_id="+chat+"&query=main", &search))
	require.Len(t, search.Files, 1)
	assert.Equal(t, "src/main.go", search.Files[0].Path)

	var diff api.GitDiffResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/git/diff?session_id="+chat, &diff))
	byPath := map[string]api.GitDiffFile{}
	for _, f := range diff.Files {
		byPath[f.NewPath] = f
	}
	require.Len(t, byPath, 2)
	assert.Equal(t, "M", byPath["src/main.go"].Status)
	assert.Equal(t, "package main\n", byPath["src/main.go"].OldContent)
	assert.Contains(t, byPath["src/main.go"].Hunks[0], "+// edit")
	assert.Equal(t, "A", byPath["new.txt"].Status)
	assert.Equal(t, http.StatusInternalServerError, getJSON(t, srv, "/api/git/diff?session_id="+chat+"&commit=nonexistent", nil))

	var lg api.GitLogResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/git/log?session_id="+chat, &lg))
	require.Len(t, lg.Commits, 1)
	assert.Equal(t, "first", lg.Commits[0].Message)

	var sub api.SubdirsResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/subdirs?dir="+ws, &sub))
	assert.Equal(t, []string{"src"}, sub.Subdirs)
	assert.Equal(t, ws, sub.GitRoot)
	assert.Equal(t, http.StatusForbidden, getJSON(t, srv, "/api/subdirs?dir=/etc", nil))
	assert.Equal(t, http.StatusForbidden, getJSON(t, srv, "/api/subdirs?dir="+outside, nil))

	// A hostile repo config never runs anything on the runner.
	marker := filepath.Join(t.TempDir(), "pwned")
	git(t, ws, "config", "--local", "core.fsmonitor", "touch "+marker)
	assert.Equal(t, http.StatusForbidden, getJSON(t, srv, "/api/git/diff?session_id="+chat, nil))
	assert.NoFileExists(t, marker)
}

func TestAPIDirsRunnerOfflineIs503(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, repo := newAPIServer(t, func(string) (runnerfs.FS, error) {
		return nil, runnerfs.Errf(runnerfs.CodeUnavailable, "runner offline")
	})
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chat, Title: "t"}))
	for _, url := range []string{
		"/api/files/tree?session_id=" + chat,
		"/api/files/content?session_id=" + chat + "&path=a",
		"/api/files/search?session_id=" + chat,
		"/api/git/diff?session_id=" + chat,
		"/api/git/log?session_id=" + chat,
		"/api/subdirs?dir=/x",
	} {
		assert.Equal(t, http.StatusServiceUnavailable, getJSON(t, srv, url, nil), url)
	}
	for _, url := range []string{"/api/git/push", "/api/git/pull"} {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, url, bytes.NewBufferString(`{"session_id":"`+chat+`"}`)))
		assert.Equal(t, http.StatusServiceUnavailable, rr.Code, url)
	}
}

// wsRead returns the output payloads received so far until substr shows up.
func wsWaitFor(t *testing.T, c *websocket.Conn, substr string) {
	t.Helper()
	var got strings.Builder
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	for !strings.Contains(got.String(), substr) {
		_, msg, err := c.ReadMessage()
		require.NoError(t, err, "output so far: %q", got.String())
		require.NotEmpty(t, msg)
		assert.Equal(t, byte('0'), msg[0])
		got.Write(msg[1:])
	}
}

func TestAPITtydOverRunnerService(t *testing.T) {
	cl, _ := startService(t, Options{Shell: fakeShell(t)})
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	online := true
	srv, err := api.New(&config.Config{Host: "http://localhost:8080"}, testDB, api.WithRunnerClient(func(node string) (runnerpb.RunnerServiceClient, error) {
		if node != "r1" || !online {
			return nil, errors.New("offline")
		}
		return cl, nil
	}))
	require.NoError(t, err)
	repo := dbmodels.NewSessionRepository(testDB)
	work := filepath.Join(runnerpaths.SessionTmpDir(chat), "work")
	require.NoError(t, os.MkdirAll(work, 0o755))
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chat, Title: "t", RunDir: work, RunnerNodeID: "r1"}))
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	dial := func(path string) (*websocket.Conn, *http.Response, error) {
		d := websocket.Dialer{Subprotocols: []string{"tty"}}
		return d.Dial(wsURL+path, nil)
	}

	c, _, err := dial("/api/ttyd/agent-" + chat + "/ws")
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	assert.Equal(t, "tty", c.Subprotocol())
	require.NoError(t, c.WriteMessage(websocket.TextMessage, []byte(`{"AuthToken":"","columns":90,"rows":25}`)))
	realWork, err := filepath.EvalSymlinks(work)
	require.NoError(t, err)
	wsWaitFor(t, c, "cwd="+realWork)
	wsWaitFor(t, c, "25 90")
	require.NoError(t, c.WriteMessage(websocket.BinaryMessage, append([]byte{'1'}, []byte(`{"columns":100,"rows":30}`)...)))
	require.NoError(t, c.WriteMessage(websocket.BinaryMessage, append([]byte{'0'}, []byte("hello\n")...)))
	wsWaitFor(t, c, "got:hello")
	// The shell exits: the socket is closed normally.
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	// (remaining output frames may still arrive before the close)
	for err == nil {
		_, _, err = c.ReadMessage()
	}
	assert.True(t, websocket.IsCloseError(err, websocket.CloseNormalClosure), "%v", err)

	// A plain shell needs a runner: none is named and none is the only one.
	_, resp, err := dial("/api/ttyd/sidebar/ws")
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	c2, _, err := dial("/api/ttyd/sidebar/ws?runner=r1")
	require.NoError(t, err)
	_ = c2.Close()

	// Unknown chat, non-websocket request and offline runner.
	_, resp, err = dial("/api/ttyd/agent-" + "11111111-1111-7111-8111-111111111111" + "/ws")
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	plain, err := http.Get(ts.URL + "/api/ttyd/sidebar/ws?runner=r1")
	require.NoError(t, err)
	_ = plain.Body.Close()
	assert.Equal(t, http.StatusBadRequest, plain.StatusCode)
	online = false
	_, resp, err = dial("/api/ttyd/agent-" + chat + "/ws")
	require.Error(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// TestTranscriptAndWorkflowStoreOverRunnerService drives the backend's
// transcript and offloaded workflow files through a real gRPC Service: the
// session API reads the runner's messages.jsonl, which is the file the
// sandbox sees as /session/messages.jsonl.
func TestTranscriptAndWorkflowStoreOverRunnerService(t *testing.T) {
	cl, ws := startService(t, Options{})
	resolve := func(id string) (runnerfs.FS, error) { return runnerfs.NewRemote(cl, id), nil }
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	srv, err := api.New(&config.Config{Host: "http://localhost:8080"}, testDB, api.WithRunnerFS(resolve))
	require.NoError(t, err)
	repo := dbmodels.NewSessionRepository(testDB)
	repo.SetFSResolver(resolve)
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chat, Title: "t", RunDir: ws}))

	require.NoError(t, repo.AppendMessage(chat, dbmodels.ChatMessage{Role: "user", Content: "hello"}))
	require.NoError(t, repo.AppendMessage(chat, dbmodels.ChatMessage{ID: "ask-1", Role: "ask_user", Content: "ok?"}))
	_, err = repo.MarkAskUserReplied(chat, "ask-1", "yes")
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sessions/"+chat, nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var got struct {
		Messages []dbmodels.ChatMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Len(t, got.Messages, 2)
	assert.Equal(t, "hello", got.Messages[0].Content)
	assert.True(t, got.Messages[1].Replied)
	assert.Equal(t, "yes", got.Messages[1].ReplyText)

	raw, err := os.ReadFile(filepath.Join(runnerpaths.SessionDir(chat), "messages.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"content":"hello"`)

	// Offloaded workflow files land in the same session dir on the runner.
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)
	wfRepo.SetFSResolver(resolve)
	require.NoError(t, wfRepo.SaveRun(&dbmodels.WorkflowRun{RunID: "run-e2e", SessionID: chat, Status: dbmodels.WorkflowStatusRunning, DAGSpec: "name: x\n", Input: "in"}))
	assert.FileExists(t, filepath.Join(runnerpaths.SessionDir(chat), "workflows", "run-e2e", "dag_spec.yaml"))
	run, err := wfRepo.GetRun("run-e2e")
	require.NoError(t, err)
	assert.Equal(t, "name: x\n", run.DAGSpec)
}
