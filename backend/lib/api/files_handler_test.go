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

func setupTestServer(t *testing.T) (*Server, *dbmodels.SessionRepository, string, string) {
	t.Helper()

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

	tempWorkspaceDir := t.TempDir()
	chatID := uuid.Must(uuid.NewV7()).String()

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

	nonExistentChatID := uuid.Must(uuid.NewV7()).String()

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

	tests := []struct {
		name          string
		sessionID     string
		filePath      string
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:         "Valid source file read",
			sessionID:    chatID,
			filePath:     "main.go",
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
			expectedCode: http.StatusNotFound,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "file not found")
			},
		},
		{
			name:         "Directory requested instead of file",
			sessionID:    chatID,
			filePath:     "somedir",
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "requested path is a directory, not a file")
			},
		},
		{
			name:         "Oversized file (>5MB)",
			sessionID:    chatID,
			filePath:     "large.txt",
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "file size exceeds maximum allowed limit")
			},
		},
		{
			name:         "Binary file detection",
			sessionID:    chatID,
			filePath:     "data.bin",
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
			expectedCode: http.StatusForbidden,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "access denied")
			},
		},
		{
			name:         "Symlink escape attack",
			sessionID:    chatID,
			filePath:     "leak_symlink.txt",
			expectedCode: http.StatusForbidden,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Contains(t, rec.Body.String(), "access denied: path escapes workspace boundary")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reqURL := "/api/files/content?session_id=" + tt.sessionID + "&path=" + tt.filePath
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
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	server := &Server{
		conf: &config.Config{Host: "http://localhost:8080"},
		repo: repo,
	}
	server.mux = server.buildMuxLocked()

	chatID := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	sess.RunDir = "/tmp"
	require.NoError(t, repo.SaveSession(sess))

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	sessionTmp := filepath.Join(home, "tmp", chatID)
	require.NoError(t, os.MkdirAll(sessionTmp, 0755))
	defer func() { _ = os.RemoveAll(sessionTmp) }()

	testFile := filepath.Join(sessionTmp, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("hello from session tmp"), 0644))

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
}
