package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

func setupTestServer(t *testing.T) (*Server, *dbmodels.SessionRepository, string, string) {
	t.Helper()

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	tempSessionDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempSessionDir, chatID)
	})
	conf := &config.Config{Host: "http://localhost:8080"}

	server := &Server{
		conf: conf,
		repo: repo,
	}
	server.mux = server.buildMuxLocked()

	tempWorkspaceDir := t.TempDir()
	chatID := uuid.NewV7().String()

	err = repo.UpdateAgentSession(chatID, "test-agent", "", "", nil)
	require.NoError(t, err)

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	sess.RunDir = tempWorkspaceDir
	err = repo.SaveSession(sess)
	require.NoError(t, err)

	return server, repo, chatID, tempWorkspaceDir
}

func TestFilesTreeHandler_TableDriven(t *testing.T) {
	t.Parallel()

	server, _, chatID, workspaceDir := setupTestServer(t)

	// Create workspace fixture structure:
	// workspaceDir/
	//   .git/ (should be ignored)
	//     config
	//   node_modules/ (should be ignored)
	//     pkg.json
	//   src/
	//     main.go
	//     helper.go
	//   docs/
	//     readme.md
	//   root.txt
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceDir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, ".git", "config"), []byte("git config"), 0644))

	require.NoError(t, os.MkdirAll(filepath.Join(workspaceDir, "node_modules"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "node_modules", "pkg.json"), []byte("{}"), 0644))

	require.NoError(t, os.MkdirAll(filepath.Join(workspaceDir, "src"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "src", "main.go"), []byte("package main"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "src", "helper.go"), []byte("package main"), 0644))

	require.NoError(t, os.MkdirAll(filepath.Join(workspaceDir, "docs"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "docs", "readme.md"), []byte("# Docs"), 0644))

	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "root.txt"), []byte("root file"), 0644))

	nonExistentChatID := uuid.NewV7().String()

	tests := []struct {
		name           string
		sessionID      string
		subPath        string
		serverOverride *Server
		expectedCode   int
		checkResponse  func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:         "Missing session_id",
			sessionID:    "",
			subPath:      "",
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "session_id is required")
			},
		},
		{
			name:         "Invalid session_id format",
			sessionID:    "invalid/session/id",
			subPath:      "",
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "invalid session_id format")
			},
		},
		{
			name:         "Non-existent session_id",
			sessionID:    nonExistentChatID,
			subPath:      "",
			expectedCode: http.StatusNotFound,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "session not found")
			},
		},
		{
			name:      "Nil repo guard",
			sessionID: chatID,
			subPath:   "",
			serverOverride: func() *Server {
				s := &Server{conf: &config.Config{Host: "http://localhost:8080"}}
				s.mux = s.buildMuxLocked()
				return s
			}(),
			expectedCode: http.StatusInternalServerError,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "session repository unavailable")
			},
		},
		{
			name:         "Root listing - ignores .git and node_modules, folders first",
			sessionID:    chatID,
			subPath:      "",
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp FileTreeResponse
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Equal(t, "", resp.Path)
				require.Len(t, resp.Entries, 3) // docs, src, root.txt

				// Folders first alphabetically
				assert.Equal(t, "docs", resp.Entries[0].Name)
				assert.True(t, resp.Entries[0].IsDir)
				assert.Equal(t, "docs", resp.Entries[0].Path)

				assert.Equal(t, "src", resp.Entries[1].Name)
				assert.True(t, resp.Entries[1].IsDir)
				assert.Equal(t, "src", resp.Entries[1].Path)

				assert.Equal(t, "root.txt", resp.Entries[2].Name)
				assert.False(t, resp.Entries[2].IsDir)
				assert.Equal(t, "root.txt", resp.Entries[2].Path)
				assert.Equal(t, "txt", resp.Entries[2].Ext)
				assert.Equal(t, int64(9), resp.Entries[2].Size)
			},
		},
		{
			name:         "Subdirectory listing - src",
			sessionID:    chatID,
			subPath:      "src",
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp FileTreeResponse
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Equal(t, "src", resp.Path)
				require.Len(t, resp.Entries, 2)

				assert.Equal(t, "helper.go", resp.Entries[0].Name)
				assert.False(t, resp.Entries[0].IsDir)
				assert.Equal(t, "src/helper.go", resp.Entries[0].Path)
				assert.Equal(t, "go", resp.Entries[0].Ext)

				assert.Equal(t, "main.go", resp.Entries[1].Name)
				assert.False(t, resp.Entries[1].IsDir)
				assert.Equal(t, "src/main.go", resp.Entries[1].Path)
				assert.Equal(t, "go", resp.Entries[1].Ext)
			},
		},
		{
			name:         "Path traversal attack via ../",
			sessionID:    chatID,
			subPath:      "../../etc",
			expectedCode: http.StatusForbidden,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "access denied")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := server
			if tt.serverOverride != nil {
				srv = tt.serverOverride
			}

			reqURL := "/api/files/tree?session_id=" + tt.sessionID
			if tt.subPath != "" {
				reqURL += "&path=" + tt.subPath
			}

			req := httptest.NewRequest(http.MethodGet, reqURL, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedCode, rec.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestFilesContentHandler_TableDriven(t *testing.T) {
	t.Parallel()

	server, _, chatID, workspaceDir := setupTestServer(t)

	// Setup files
	normalFile := filepath.Join(workspaceDir, "main.go")
	require.NoError(t, os.WriteFile(normalFile, []byte("package main\n\nfunc main() {}\n"), 0644))

	binaryFile := filepath.Join(workspaceDir, "data.bin")
	require.NoError(t, os.WriteFile(binaryFile, []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}, 0644))

	bigFile := filepath.Join(workspaceDir, "large.txt")
	f, err := os.Create(bigFile)
	require.NoError(t, err)
	_, err = f.Write(bytes.Repeat([]byte("a"), 1024))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(maxReadFileSize+1024))
	require.NoError(t, f.Close())

	dirPath := filepath.Join(workspaceDir, "somedir")
	require.NoError(t, os.MkdirAll(dirPath, 0755))

	// Symlink escape test setup
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.key")
	require.NoError(t, os.WriteFile(outsideFile, []byte("secret_api_key_123"), 0644))

	symlinkPath := filepath.Join(workspaceDir, "leak_symlink.txt")
	require.NoError(t, os.Symlink(outsideFile, symlinkPath))

	// Media files for raw streaming tests
	pngFile := filepath.Join(workspaceDir, "image.png")
	pngBytes := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x01, 0x02, 0x03}
	require.NoError(t, os.WriteFile(pngFile, pngBytes, 0644))

	tests := []struct {
		name          string
		sessionID     string
		filePath      string
		raw           string
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:         "Valid source file read",
			sessionID:    chatID,
			filePath:     "main.go",
			raw:          "",
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp FileContentResponse
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Equal(t, "main.go", resp.Path)
				assert.Equal(t, "main.go", resp.Name)
				assert.Equal(t, "go", resp.Ext)
				assert.Equal(t, "package main\n\nfunc main() {}\n", resp.Content)
				assert.False(t, resp.IsBinary)
				assert.False(t, resp.UpdatedAt.IsZero())
			},
		},
		{
			name:         "Non-existent file",
			sessionID:    chatID,
			filePath:     "not_exist.go",
			raw:          "",
			expectedCode: http.StatusNotFound,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "file not found")
			},
		},
		{
			name:         "Directory requested instead of file",
			sessionID:    chatID,
			filePath:     "somedir",
			raw:          "",
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "requested path is a directory, not a file")
			},
		},
		{
			name:         "Oversized file (>5MB)",
			sessionID:    chatID,
			filePath:     "large.txt",
			raw:          "",
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "file size exceeds maximum allowed limit")
			},
		},
		{
			name:         "Binary file detection",
			sessionID:    chatID,
			filePath:     "data.bin",
			raw:          "",
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp FileContentResponse
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.True(t, resp.IsBinary)
				assert.Empty(t, resp.Content)
				assert.Equal(t, int64(5), resp.Size)
			},
		},
		{
			name:         "Path traversal attack via ../",
			sessionID:    chatID,
			filePath:     "../../etc/passwd",
			raw:          "",
			expectedCode: http.StatusForbidden,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "access denied")
			},
		},
		{
			name:         "Symlink escape attack",
			sessionID:    chatID,
			filePath:     "leak_symlink.txt",
			raw:          "",
			expectedCode: http.StatusForbidden,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "access denied: path escapes workspace boundary")
			},
		},
		{
			name:         "Workspace Files Raw Endpoint & Security Headers",
			sessionID:    chatID,
			filePath:     "image.png",
			raw:          "1",
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
				assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
				assert.Equal(t, "default-src 'none'; sandbox", rec.Header().Get("Content-Security-Policy"))
				assert.Equal(t, pngBytes, rec.Body.Bytes())
			},
		},
		{
			name:         "Workspace Files Raw Non-Media Denied",
			sessionID:    chatID,
			filePath:     "main.go",
			raw:          "1",
			expectedCode: http.StatusForbidden,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "access denied: streaming is only permitted for media files")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reqURL := "/api/files/content?session_id=" + tt.sessionID + "&path=" + tt.filePath
			if tt.raw != "" {
				reqURL += "&raw=" + tt.raw
			}
			req := httptest.NewRequest(http.MethodGet, reqURL, nil)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedCode, rec.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestFilesSearchHandler_TableDriven(t *testing.T) {
	t.Parallel()

	server, _, chatID, workspaceDir := setupTestServer(t)

	// Fixtures:
	// workspaceDir/
	//   .git/head
	//   node_modules/index.js
	//   app/
	//     models/user.go
	//     controllers/user_controller.go
	//     controllers/auth_controller.go
	//   config.json
	//   README.md
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceDir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, ".git", "head"), []byte("ref: refs/heads/main"), 0644))

	require.NoError(t, os.MkdirAll(filepath.Join(workspaceDir, "node_modules"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "node_modules", "index.js"), []byte("module.exports = {}"), 0644))

	require.NoError(t, os.MkdirAll(filepath.Join(workspaceDir, "app", "models"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "app", "models", "user.go"), []byte("package models"), 0644))

	require.NoError(t, os.MkdirAll(filepath.Join(workspaceDir, "app", "controllers"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "app", "controllers", "user_controller.go"), []byte("package controllers"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "app", "controllers", "auth_controller.go"), []byte("package controllers"), 0644))

	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "config.json"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "README.md"), []byte("# Title"), 0644))

	tests := []struct {
		name          string
		sessionID     string
		query         string
		limit         string
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:         "Empty query returns workspace files up to limit excluding .git and node_modules",
			sessionID:    chatID,
			query:        "",
			limit:        "10",
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp FileSearchResponse
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Len(t, resp.Files, 5) // README.md, config.json, app/controllers/auth_controller.go, app/controllers/user_controller.go, app/models/user.go
			},
		},
		{
			name:         "Substring search for 'user'",
			sessionID:    chatID,
			query:        "user",
			limit:        "",
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp FileSearchResponse
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				require.Len(t, resp.Files, 2)
				assert.Contains(t, []string{"app/controllers/user_controller.go", "app/models/user.go"}, resp.Files[0].Path)
				assert.Contains(t, []string{"app/controllers/user_controller.go", "app/models/user.go"}, resp.Files[1].Path)
			},
		},
		{
			name:         "Limit clamping test (limit=2)",
			sessionID:    chatID,
			query:        "",
			limit:        "2",
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp FileSearchResponse
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Len(t, resp.Files, 2)
			},
		},
		{
			name:         "Invalid session_id",
			sessionID:    "invalid/session/id",
			query:        "test",
			limit:        "",
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "invalid session_id format")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reqURL := "/api/files/search?session_id=" + tt.sessionID
			if tt.query != "" {
				reqURL += "&query=" + tt.query
			}
			if tt.limit != "" {
				reqURL += "&limit=" + tt.limit
			}

			req := httptest.NewRequest(http.MethodGet, reqURL, nil)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedCode, rec.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestFilesHandler_TmpResolution(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempHome, "data", chatID)
	})
	server := &Server{
		conf: &config.Config{Host: "http://localhost:8080"},
		repo: repo,
	}
	server.mux = server.buildMuxLocked()

	chatID := uuid.NewV7().String()
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	sess.RunDir = "/tmp/session-id"
	require.NoError(t, repo.SaveSession(sess))

	sessionTmp := filepath.Join(tempHome, "tmp", chatID)
	require.NoError(t, os.MkdirAll(sessionTmp, 0755))

	testFile := filepath.Join(sessionTmp, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("hello from session tmp"), 0644))

	t.Run("File Tree with /tmp/session-id RunDir", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/tree?session_id="+chatID, nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileTreeResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		found := false
		for _, entry := range resp.Entries {
			if entry.Name == "test.txt" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected test.txt in session tmp tree")
	})

	t.Run("File Tree explicitly requesting /tmp/session-id path", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/tree?session_id="+chatID+"&path=/tmp/session-id", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileTreeResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		found := false
		for _, entry := range resp.Entries {
			if entry.Name == "test.txt" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected test.txt in /tmp/session-id tree")
	})

	t.Run("File Content with /tmp/session-id/test.txt", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=/tmp/session-id/test.txt", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "hello from session tmp", resp.Content)
		assert.Equal(t, "test.txt", resp.Name)
	})

	t.Run("File Content with /tmp/test.txt", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=/tmp/test.txt", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "hello from session tmp", resp.Content)
	})

	t.Run("File Content with regular workspace and /tmp/session-id path", func(t *testing.T) {
		wsDir := t.TempDir()
		sess.RunDir = wsDir
		require.NoError(t, repo.SaveSession(sess))

		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=/tmp/session-id/test.txt", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "hello from session tmp", resp.Content)
	})

	t.Run("Workspace with tmp/ subfolder is NOT hijacked by session tmp", func(t *testing.T) {
		wsDir := t.TempDir()
		sess.RunDir = wsDir
		require.NoError(t, repo.SaveSession(sess))

		wsTmpDir := filepath.Join(wsDir, "tmp")
		require.NoError(t, os.MkdirAll(wsTmpDir, 0755))
		wsTmpFile := filepath.Join(wsTmpDir, "local-temp.txt")
		require.NoError(t, os.WriteFile(wsTmpFile, []byte("local workspace temp file"), 0644))

		// Requesting workspace relative path tmp/local-temp.txt
		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=tmp/local-temp.txt", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "local workspace temp file", resp.Content)
		assert.Equal(t, "local-temp.txt", resp.Name)
	})

	t.Run("File Content with tmp/... fallback to session tmp when absent from workspace", func(t *testing.T) {
		wsDir := t.TempDir()
		sess.RunDir = wsDir
		require.NoError(t, repo.SaveSession(sess))

		// Target exists only in session tmp (sessionTmpFile is test.txt with "hello from session tmp")
		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=tmp/test.txt", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "hello from session tmp", resp.Content)
		assert.Equal(t, "/tmp/test.txt", resp.Path)
	})

	t.Run("File Content with explicit scope=tmp", func(t *testing.T) {
		wsDir := t.TempDir()
		sess.RunDir = wsDir
		require.NoError(t, repo.SaveSession(sess))

		// Create same relative path in ws
		wsTmpDir := filepath.Join(wsDir, "tmp")
		require.NoError(t, os.MkdirAll(wsTmpDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(wsTmpDir, "test.txt"), []byte("ws content"), 0644))

		// Explicit scope=tmp should force reading from session tmp
		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=tmp/test.txt&scope=tmp", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "hello from session tmp", resp.Content)
	})

	t.Run("File Content with explicit scope=workspace when ws tmp exists", func(t *testing.T) {
		wsDir := t.TempDir()
		sess.RunDir = wsDir
		require.NoError(t, repo.SaveSession(sess))

		wsTmpDir := filepath.Join(wsDir, "tmp")
		require.NoError(t, os.MkdirAll(wsTmpDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(wsTmpDir, "test.txt"), []byte("ws content"), 0644))

		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=tmp/test.txt&scope=workspace", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "ws content", resp.Content)
	})

	t.Run("File Content with non-tmp path and scope=tmp ignores scope", func(t *testing.T) {
		wsDir := t.TempDir()
		sess.RunDir = wsDir
		require.NoError(t, repo.SaveSession(sess))

		readmePath := filepath.Join(wsDir, "README.md")
		require.NoError(t, os.WriteFile(readmePath, []byte("# Hello Workspace"), 0644))

		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=README.md&scope=tmp", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "# Hello Workspace", resp.Content)
	})

	t.Run("File Content with invalid scope fallback to auto", func(t *testing.T) {
		wsDir := t.TempDir()
		sess.RunDir = wsDir
		require.NoError(t, repo.SaveSession(sess))

		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=tmp/test.txt&scope=invalid", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "hello from session tmp", resp.Content)
	})

	t.Run("File Content with tmp/absent.txt absent from both ws and session tmp -> 404", func(t *testing.T) {
		wsDir := t.TempDir()
		sess.RunDir = wsDir
		require.NoError(t, repo.SaveSession(sess))

		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=tmp/absent.txt", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), "file not found")
	})

	t.Run("File Tree with path=tmp when ws tmp exists returns ws tree", func(t *testing.T) {
		wsDir := t.TempDir()
		sess.RunDir = wsDir
		require.NoError(t, repo.SaveSession(sess))

		wsTmpDir := filepath.Join(wsDir, "tmp")
		require.NoError(t, os.MkdirAll(wsTmpDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(wsTmpDir, "ws-tree-item.txt"), []byte("tree item"), 0644))

		req := httptest.NewRequest(http.MethodGet, "/api/files/tree?session_id="+chatID+"&path=tmp", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileTreeResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Entries, 1)
		assert.Equal(t, "ws-tree-item.txt", resp.Entries[0].Name)
	})

	t.Run("File Tree with path=tmp&scope=tmp", func(t *testing.T) {
		wsDir := t.TempDir()
		sess.RunDir = wsDir
		require.NoError(t, repo.SaveSession(sess))

		wsTmpDir := filepath.Join(wsDir, "tmp")
		require.NoError(t, os.MkdirAll(wsTmpDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(wsTmpDir, "ws-tree-item.txt"), []byte("tree item"), 0644))

		req := httptest.NewRequest(http.MethodGet, "/api/files/tree?session_id="+chatID+"&path=tmp&scope=tmp", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileTreeResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		// Should return session tmp entries (contains test.txt)
		foundTest := false
		for _, e := range resp.Entries {
			if e.Name == "test.txt" {
				foundTest = true
				break
			}
		}
		assert.True(t, foundTest)
	})

	t.Run("File Tree with path=tmp fallback to session tmp when ws tmp absent", func(t *testing.T) {
		wsDir := t.TempDir()
		sess.RunDir = wsDir
		require.NoError(t, repo.SaveSession(sess))

		req := httptest.NewRequest(http.MethodGet, "/api/files/tree?session_id="+chatID+"&path=tmp", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileTreeResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		foundTest := false
		for _, e := range resp.Entries {
			if e.Name == "test.txt" {
				foundTest = true
				break
			}
		}
		assert.True(t, foundTest)
	})
}

func TestFilesHandler_SessionResolution(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempHome, "data", chatID)
	})
	server := &Server{
		conf: &config.Config{Host: "http://localhost:8080"},
		repo: repo,
	}
	server.mux = server.buildMuxLocked()

	chatID := uuid.NewV7().String()
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	wsDir := t.TempDir()
	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	sess.RunDir = wsDir
	require.NoError(t, repo.SaveSession(sess))

	sessionDir := filepath.Join(tempHome, "data", chatID)
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	testFile := filepath.Join(sessionDir, "notes.md")
	require.NoError(t, os.WriteFile(testFile, []byte("hello from session ns"), 0644))

	t.Run("File Tree from /session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/tree?session_id="+chatID+"&path=/session", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileTreeResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Entries, 1)
		assert.Equal(t, "notes.md", resp.Entries[0].Name)
		assert.Equal(t, "/session/notes.md", resp.Entries[0].Path)
	})

	t.Run("File Tree subdir via /session/session-id", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(filepath.Join(sessionDir, "sub"), 0755))
		req := httptest.NewRequest(http.MethodGet, "/api/files/tree?session_id="+chatID+"&path=/session/session-id", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileTreeResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		found := false
		for _, e := range resp.Entries {
			if e.Name == "sub" {
				found = true
				assert.True(t, e.IsDir)
				assert.Equal(t, "/session/sub", e.Path)
			}
		}
		assert.True(t, found, "expected sub dir in session tree")
	})

	t.Run("File Content via /session path", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=/session/notes.md", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "hello from session ns", resp.Content)
		assert.Equal(t, "/session/notes.md", resp.Path)
	})

	t.Run("File Content via session/ relative path with scope=session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=session/notes.md&scope=session", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "hello from session ns", resp.Content)
	})

	t.Run("File Content session path traversal rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/content?session_id="+chatID+"&path=/session/../../etc/passwd", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		// /session/../../etc/passwd is not an explicit /session/... subpath after Clean
		// (Clean resolves ..), so it lands in workspace resolution and must not escape.
		assert.NotEqual(t, http.StatusOK, rec.Code)
	})

	t.Run("File Search includes session scope with /session prefix", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/search?session_id="+chatID+"&query=notes", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Files, 1)
		assert.Equal(t, "/session/notes.md", resp.Files[0].Path)
		assert.Equal(t, "session", resp.Files[0].Scope)
	})

	t.Run("RunDir under session namespace resolves like tmp", func(t *testing.T) {
		sessRunDirChat := uuid.NewV7().String()
		require.NoError(t, repo.UpdateAgentSession(sessRunDirChat, "test-agent", "", "", nil))

		sessRunDirSess, sErr := repo.GetSession(sessRunDirChat)
		require.NoError(t, sErr)
		sessRunDirSess.RunDir = "session/session-id"
		require.NoError(t, repo.SaveSession(sessRunDirSess))

		sessRunDirPath := filepath.Join(tempHome, "data", sessRunDirChat)
		require.NoError(t, os.MkdirAll(sessRunDirPath, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sessRunDirPath, "rd.txt"), []byte("rd"), 0644))

		req := httptest.NewRequest(http.MethodGet, "/api/files/tree?session_id="+sessRunDirChat, nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileTreeResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Entries, 1)
		assert.Equal(t, "rd.txt", resp.Entries[0].Name)
		assert.Equal(t, "rd.txt", resp.Entries[0].Path)
	})
}

func TestFilesSearchHandler_TmpIntegration(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempHome, "data", chatID)
	})
	server := &Server{
		conf: &config.Config{Host: "http://localhost:8080"},
		repo: repo,
	}
	server.mux = server.buildMuxLocked()

	chatID := uuid.NewV7().String()
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)

	wsDir := t.TempDir()
	sess.RunDir = wsDir
	require.NoError(t, repo.SaveSession(sess))

	sessionTmp := filepath.Join(tempHome, "tmp", chatID)

	t.Run("Joint search in workspace and session tmp with scope and prefix", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(sessionTmp, 0755))

		// Workspace file
		require.NoError(t, os.WriteFile(filepath.Join(wsDir, "ws_sample.txt"), []byte("ws sample"), 0644))
		// Session tmp file
		require.NoError(t, os.WriteFile(filepath.Join(sessionTmp, "tmp_sample.txt"), []byte("tmp sample"), 0644))

		req := httptest.NewRequest(http.MethodGet, "/api/files/search?session_id="+chatID+"&query=sample", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Files, 2)

		var wsFound, tmpFound bool
		for _, f := range resp.Files {
			switch f.Name {
			case "ws_sample.txt":
				wsFound = true
				assert.Equal(t, "ws_sample.txt", f.Path)
				assert.Equal(t, "workspace", f.Scope)
			case "tmp_sample.txt":
				tmpFound = true
				assert.Equal(t, "/tmp/tmp_sample.txt", f.Path)
				assert.Equal(t, "tmp", f.Scope)
			}
		}
		assert.True(t, wsFound, "ws_sample.txt should be found")
		assert.True(t, tmpFound, "tmp_sample.txt should be found with /tmp prefix and scope=tmp")
	})

	t.Run("Anti-starvation quota preserves quota for tmp directory", func(t *testing.T) {
		starveWsDir := t.TempDir()
		starveChatID := uuid.NewV7().String()
		require.NoError(t, repo.UpdateAgentSession(starveChatID, "test-agent", "", "", nil))

		starveSess, sErr := repo.GetSession(starveChatID)
		require.NoError(t, sErr)
		starveSess.RunDir = starveWsDir
		require.NoError(t, repo.SaveSession(starveSess))

		starveTmp := filepath.Join(tempHome, "tmp", starveChatID)
		require.NoError(t, os.MkdirAll(starveTmp, 0755))

		// Create 20 matching files in workspace
		for i := 0; i < 20; i++ {
			fName := filepath.Join(starveWsDir, "starve_item_"+string(rune('a'+i))+".go")
			require.NoError(t, os.WriteFile(fName, []byte("package main"), 0644))
		}

		// Create 5 matching files in tmp
		for i := 0; i < 5; i++ {
			fName := filepath.Join(starveTmp, "starve_tmp_"+string(rune('a'+i))+".go")
			require.NoError(t, os.WriteFile(fName, []byte("package main"), 0644))
		}

		// Limit = 10 -> wsLimit = 10 - 2 = 8, tmp gets remaining 2
		req := httptest.NewRequest(http.MethodGet, "/api/files/search?session_id="+starveChatID+"&query=starve&limit=10", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Files, 10)

		var wsCount, tmpCount int
		for _, f := range resp.Files {
			switch f.Scope {
			case "workspace":
				wsCount++
			case "tmp":
				tmpCount++
				assert.True(t, strings.HasPrefix(f.Path, "/tmp/"))
			}
		}
		assert.Equal(t, 8, wsCount, "ws count should be capped at wsLimit (8)")
		assert.Equal(t, 2, tmpCount, "tmp count should take remaining quota (2)")
	})

	t.Run("Overlapping/Coinciding directory deduplication", func(t *testing.T) {
		overlapChatID := uuid.NewV7().String()
		require.NoError(t, repo.UpdateAgentSession(overlapChatID, "test-agent", "", "", nil))

		overlapSess, sErr := repo.GetSession(overlapChatID)
		require.NoError(t, sErr)
		overlapSess.RunDir = "/tmp/session-id"
		require.NoError(t, repo.SaveSession(overlapSess))

		overlapTmp := filepath.Join(tempHome, "tmp", overlapChatID)
		require.NoError(t, os.MkdirAll(overlapTmp, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(overlapTmp, "overlap_test.go"), []byte("package main"), 0644))

		req := httptest.NewRequest(http.MethodGet, "/api/files/search?session_id="+overlapChatID+"&query=overlap", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Files, 1)
		assert.Equal(t, "overlap_test.go", resp.Files[0].Path)
		assert.Equal(t, "workspace", resp.Files[0].Scope)
	})

	t.Run("Symlink security matrix", func(t *testing.T) {
		symChatID := uuid.NewV7().String()
		require.NoError(t, repo.UpdateAgentSession(symChatID, "test-agent", "", "", nil))

		symWsDir := t.TempDir()
		symSess, sErr := repo.GetSession(symChatID)
		require.NoError(t, sErr)
		symSess.RunDir = symWsDir
		require.NoError(t, repo.SaveSession(symSess))

		symTmp := filepath.Join(tempHome, "tmp", symChatID)
		require.NoError(t, os.MkdirAll(symTmp, 0755))

		// 1. Real file in tmp
		realTmpFile := filepath.Join(symTmp, "real_target.txt")
		require.NoError(t, os.WriteFile(realTmpFile, []byte("real content"), 0644))

		// 2. Symlink inside tmp pointing to realTmpFile (should be deduplicated)
		symlinkInside := filepath.Join(symTmp, "symlink_inside.txt")
		require.NoError(t, os.Symlink(realTmpFile, symlinkInside))

		// 3. Symlink pointing outside (escaping tmp to outside temp directory)
		outsideDir := t.TempDir()
		outsideFile := filepath.Join(outsideDir, "outside_secret.txt")
		require.NoError(t, os.WriteFile(outsideFile, []byte("secret"), 0644))
		symlinkEscaping := filepath.Join(symTmp, "symlink_escaping.txt")
		require.NoError(t, os.Symlink(outsideFile, symlinkEscaping))

		// 4. Broken symlink
		brokenSymlink := filepath.Join(symTmp, "broken_link.txt")
		require.NoError(t, os.Symlink(filepath.Join(symTmp, "non_existent.txt"), brokenSymlink))

		// 5. Symlink directory (e.g., node_modules -> external dir) should not prune sibling entries
		realNodeModules := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(realNodeModules, "mod.txt"), []byte("mod"), 0644))
		require.NoError(t, os.Symlink(realNodeModules, filepath.Join(symWsDir, "node_modules")))
		require.NoError(t, os.WriteFile(filepath.Join(symWsDir, "package.json"), []byte("{}"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(symWsDir, "zzz_last.txt"), []byte("last"), 0644))

		req := httptest.NewRequest(http.MethodGet, "/api/files/search?session_id="+symChatID+"&query=txt", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

		// Should contain real_target.txt (from tmp) and zzz_last.txt (from ws, sorted after node_modules symlink)
		require.Len(t, resp.Files, 2)
		names := []string{resp.Files[0].Name, resp.Files[1].Name}
		assert.Contains(t, names, "real_target.txt")
		assert.Contains(t, names, "zzz_last.txt")
	})

	t.Run("Non-existent session tmp directory does not error", func(t *testing.T) {
		noTmpChatID := uuid.NewV7().String()
		require.NoError(t, repo.UpdateAgentSession(noTmpChatID, "test-agent", "", "", nil))

		noTmpWs := t.TempDir()
		noTmpSess, sErr := repo.GetSession(noTmpChatID)
		require.NoError(t, sErr)
		noTmpSess.RunDir = noTmpWs
		require.NoError(t, repo.SaveSession(noTmpSess))

		require.NoError(t, os.WriteFile(filepath.Join(noTmpWs, "lonely.txt"), []byte("lonely"), 0644))

		// Ensure tmp directory does not exist
		_ = os.RemoveAll(filepath.Join(tempHome, "tmp", noTmpChatID))

		req := httptest.NewRequest(http.MethodGet, "/api/files/search?session_id="+noTmpChatID+"&query=lonely", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Files, 1)
		assert.Equal(t, "lonely.txt", resp.Files[0].Name)
	})

	t.Run("Non-existent workspace directory gracefully returns empty search results", func(t *testing.T) {
		nonExistChatID := uuid.NewV7().String()
		require.NoError(t, repo.UpdateAgentSession(nonExistChatID, "test-agent", "", "", nil))

		nonExistSess, sErr := repo.GetSession(nonExistChatID)
		require.NoError(t, sErr)
		nonExistSess.RunDir = filepath.Join(t.TempDir(), "does_not_exist")
		require.NoError(t, repo.SaveSession(nonExistSess))

		req := httptest.NewRequest(http.MethodGet, "/api/files/search?session_id="+nonExistChatID+"&query=test", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Empty(t, resp.Files)
	})

	t.Run("Files named with dotdot prefix are not misclassified as escaping", func(t *testing.T) {
		dotChatID := uuid.NewV7().String()
		require.NoError(t, repo.UpdateAgentSession(dotChatID, "test-agent", "", "", nil))

		dotWs := t.TempDir()
		dotSess, sErr := repo.GetSession(dotChatID)
		require.NoError(t, sErr)
		dotSess.RunDir = dotWs
		require.NoError(t, repo.SaveSession(dotSess))

		dotFileName := "..custom_file.txt"
		require.NoError(t, os.WriteFile(filepath.Join(dotWs, dotFileName), []byte("content"), 0644))

		req := httptest.NewRequest(http.MethodGet, "/api/files/search?session_id="+dotChatID+"&query=custom", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp FileSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Files, 1)
		assert.Equal(t, dotFileName, resp.Files[0].Name)
		assert.Equal(t, dotFileName, resp.Files[0].Path)
	})
}

func TestSearchDirectory_ExcludesSessionTranscriptAndWorkflows(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	sessionID := uuid.NewV7().String()
	sessBase := filepath.Join(tempHome, "data", sessionID)
	require.NoError(t, os.MkdirAll(sessBase, 0755))

	// Create user-visible session file
	userFile := filepath.Join(sessBase, "notes.txt")
	require.NoError(t, os.WriteFile(userFile, []byte("some notes"), 0644))

	// Create messages.jsonl
	transcriptFile := filepath.Join(sessBase, "messages.jsonl")
	require.NoError(t, os.WriteFile(transcriptFile, []byte(`{"role":"user","content":"hello"}`), 0644))

	// Create workflows/<runID>/nodes/node1.log
	wfDir := filepath.Join(sessBase, "workflows", "run-1", "nodes")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	wfLog := filepath.Join(wfDir, "node1.log")
	require.NoError(t, os.WriteFile(wfLog, []byte("node log output"), 0644))

	visited := make(map[string]bool)
	results := searchDirectory(sessBase, "/session", "session", "", 50, visited)

	require.Len(t, results, 1)
	assert.Equal(t, "notes.txt", results[0].Name)
	assert.Equal(t, "/session/notes.txt", results[0].Path)
}

func TestSearchDirectory_WorkspaceScopeNotAffectedBySessionNames(t *testing.T) {
	t.Parallel()

	tempWorkspace := t.TempDir()
	// Create directory named "session" inside workspace, containing a workflows dir and messages.jsonl
	wsSessionDir := filepath.Join(tempWorkspace, "session", "workflows")
	require.NoError(t, os.MkdirAll(wsSessionDir, 0755))
	wsWorkflowFile := filepath.Join(wsSessionDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wsWorkflowFile, []byte("steps: []"), 0644))

	wsMsgFile := filepath.Join(tempWorkspace, "session", "messages.jsonl")
	require.NoError(t, os.WriteFile(wsMsgFile, []byte("data"), 0644))

	visited := make(map[string]bool)
	results := searchDirectory(tempWorkspace, "", "workspace", "", 50, visited)

	require.Len(t, results, 2)
	paths := []string{results[0].Path, results[1].Path}
	assert.Contains(t, paths, "session/workflows/workflow.yaml")
	assert.Contains(t, paths, "session/messages.jsonl")
}
