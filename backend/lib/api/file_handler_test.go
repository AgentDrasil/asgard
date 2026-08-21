package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
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
	chatID := uuid.Must(uuid.NewV7()).String()
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
}
