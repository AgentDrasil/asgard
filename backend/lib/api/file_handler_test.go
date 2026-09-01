package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

func TestWorkspaceFileHandler(t *testing.T) {
	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	conf := &config.Config{Host: "http://localhost:8080"}

	server := &Server{
		conf: conf,
		repo: repo,
	}
	server.mux = server.buildMuxLocked()

	// Create temp directory for workspace
	tempWorkspaceDir := t.TempDir()
	testFilePath := filepath.Join(tempWorkspaceDir, "hello.txt")
	err = os.WriteFile(testFilePath, []byte("Hello Workspace World"), 0644)
	require.NoError(t, err)

	// Create session with runDir
	chatID := uuid.NewV7().String()
	err = repo.UpdateAgentSession(chatID, "test-agent", "", "", nil)
	require.NoError(t, err)

	// Manually update RunDir
	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	sess.RunDir = tempWorkspaceDir
	err = repo.SaveSession(sess)
	require.NoError(t, err)

	t.Run("Missing Parameters", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("Valid Workspace File Read", func(t *testing.T) {
		sess, err := repo.GetSession(chatID)
		require.NoError(t, err)
		sess.Artifacts = append(sess.Artifacts, "hello.txt")
		err = repo.SaveSession(sess)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path=hello.txt", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)

		var resp WorkspaceFileResponse
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, "hello.txt", resp.Name)
		assert.Equal(t, "txt", resp.Ext)
		assert.Equal(t, "Hello Workspace World", resp.Content)
	})

	t.Run("Unauthorized Workspace File Read", func(t *testing.T) {
		unauthPath := filepath.Join(tempWorkspaceDir, "secret.go")
		err = os.WriteFile(unauthPath, []byte("package main"), 0644)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path=secret.go", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), "access denied: file not authorized in session")
	})

	t.Run("Path Traversal Guard", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path=../../etc/passwd", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), "access denied")
	})

	t.Run("File Not Found", func(t *testing.T) {
		sess, err := repo.GetSession(chatID)
		require.NoError(t, err)
		sess.Artifacts = append(sess.Artifacts, "non_existent.txt")
		err = repo.SaveSession(sess)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path=non_existent.txt", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("Read Tmp File", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		sessionTmpDir := filepath.Join(home, "tmp", chatID)
		err = os.MkdirAll(sessionTmpDir, 0755)
		require.NoError(t, err)

		tmpFile, err := os.CreateTemp(sessionTmpDir, "asgard_test_*.md")
		require.NoError(t, err)

		_, err = tmpFile.WriteString("# Temp Markdown Document")
		require.NoError(t, err)
		_ = tmpFile.Close()

		relTmpPath := "/tmp/" + filepath.Base(tmpFile.Name())

		// Add to session artifacts to authorize it
		sess, err := repo.GetSession(chatID)
		require.NoError(t, err)
		sess.Artifacts = append(sess.Artifacts, ".tmp/"+filepath.Base(tmpFile.Name()))
		err = repo.SaveSession(sess)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path="+relTmpPath, nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)

		var resp WorkspaceFileResponse
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, "# Temp Markdown Document", resp.Content)
		assert.Equal(t, "md", resp.Ext)

		// Also test /tmp/session-id/<filename> path
		reqSessionIdPath := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path=/tmp/session-id/"+filepath.Base(tmpFile.Name()), nil)
		rrSessionId := httptest.NewRecorder()
		server.ServeHTTP(rrSessionId, reqSessionIdPath)
		assert.Equal(t, http.StatusOK, rrSessionId.Code)
		var respSessionId WorkspaceFileResponse
		err = json.Unmarshal(rrSessionId.Body.Bytes(), &respSessionId)
		require.NoError(t, err)
		assert.Equal(t, "# Temp Markdown Document", respSessionId.Content)
	})

	t.Run("Read Tmp File - Unauthorized Denied", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		sessionTmpDir := filepath.Join(home, "tmp", chatID)
		err = os.MkdirAll(sessionTmpDir, 0755)
		require.NoError(t, err)

		tmpFile, err := os.CreateTemp(sessionTmpDir, "asgard_test_unauth_*.md")
		require.NoError(t, err)
		_ = tmpFile.Close()

		relTmpPath := "/tmp/" + filepath.Base(tmpFile.Name())

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path="+relTmpPath, nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), "access denied")
	})

	t.Run("Valid Workspace Raw Media Stream", func(t *testing.T) {
		pngPath := filepath.Join(tempWorkspaceDir, "sample.png")
		fakePngBytes := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}
		err = os.WriteFile(pngPath, fakePngBytes, 0644)
		require.NoError(t, err)

		sess, err := repo.GetSession(chatID)
		require.NoError(t, err)
		sess.Artifacts = append(sess.Artifacts, "sample.png")
		err = repo.SaveSession(sess)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path=sample.png&raw=1", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "image/png", rr.Header().Get("Content-Type"))
		assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "default-src 'none'; sandbox", rr.Header().Get("Content-Security-Policy"))
		assert.Equal(t, fakePngBytes, rr.Body.Bytes())
	})

	t.Run("Security Headers on Raw SVG Response", func(t *testing.T) {
		svgPath := filepath.Join(tempWorkspaceDir, "vector.svg")
		svgBytes := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="10"/></svg>`)
		err = os.WriteFile(svgPath, svgBytes, 0644)
		require.NoError(t, err)

		sess, err := repo.GetSession(chatID)
		require.NoError(t, err)
		sess.Artifacts = append(sess.Artifacts, "vector.svg")
		err = repo.SaveSession(sess)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path=vector.svg&raw=true", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "image/svg+xml", rr.Header().Get("Content-Type"))
		assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "default-src 'none'; sandbox", rr.Header().Get("Content-Security-Policy"))
	})

	t.Run("Non-Media Extension Raw Request Rejected", func(t *testing.T) {
		htmlPath := filepath.Join(tempWorkspaceDir, "index.html")
		err = os.WriteFile(htmlPath, []byte("<html><body>XSS</body></html>"), 0644)
		require.NoError(t, err)

		sess, err := repo.GetSession(chatID)
		require.NoError(t, err)
		sess.Artifacts = append(sess.Artifacts, "index.html")
		err = repo.SaveSession(sess)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path=index.html&raw=1", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), "access denied: streaming is only permitted for media files")
	})

	t.Run("HTTP Range Request for Video", func(t *testing.T) {
		mp4Path := filepath.Join(tempWorkspaceDir, "sample.mp4")
		fakeMp4Bytes := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
		err = os.WriteFile(mp4Path, fakeMp4Bytes, 0644)
		require.NoError(t, err)

		sess, err := repo.GetSession(chatID)
		require.NoError(t, err)
		sess.Artifacts = append(sess.Artifacts, "sample.mp4")
		err = repo.SaveSession(sess)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path=sample.mp4&raw=1", nil)
		req.Header.Set("Range", "bytes=0-9")
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusPartialContent, rr.Code)
		assert.Equal(t, "bytes 0-9/36", rr.Header().Get("Content-Range"))
		assert.Equal(t, []byte("0123456789"), rr.Body.Bytes())
		assert.Equal(t, "video/mp4", rr.Header().Get("Content-Type"))
	})

	t.Run("Zero-Buffer Metadata for Media", func(t *testing.T) {
		largePngPath := filepath.Join(tempWorkspaceDir, "large.png")
		f, err := os.Create(largePngPath)
		require.NoError(t, err)
		require.NoError(t, f.Truncate(maxReadFileSize+1024*1024)) // 6MB
		require.NoError(t, f.Close())

		sess, err := repo.GetSession(chatID)
		require.NoError(t, err)
		sess.Artifacts = append(sess.Artifacts, "large.png")
		err = repo.SaveSession(sess)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path=large.png", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		var resp WorkspaceFileResponse
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.True(t, resp.IsBinary)
		assert.Empty(t, resp.Content)
		assert.Equal(t, "png", resp.Ext)
		assert.Equal(t, int64(maxReadFileSize+1024*1024), resp.Size)
	})

	t.Run("Oversized Text File Rejected", func(t *testing.T) {
		largeTxtPath := filepath.Join(tempWorkspaceDir, "huge.txt")
		f, err := os.Create(largeTxtPath)
		require.NoError(t, err)
		_, err = f.Write(bytes.Repeat([]byte("a"), 1024))
		require.NoError(t, err)
		require.NoError(t, f.Truncate(maxReadFileSize+1024))
		require.NoError(t, f.Close())

		sess, err := repo.GetSession(chatID)
		require.NoError(t, err)
		sess.Artifacts = append(sess.Artifacts, "huge.txt")
		err = repo.SaveSession(sess)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspace/file?session_id="+chatID+"&path=huge.txt", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "file size exceeds maximum allowed limit")
	})
}
