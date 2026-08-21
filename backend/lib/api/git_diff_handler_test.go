package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

// initTestGitRepo creates a valid git repository with an initial commit in a temp dir.
func initTestGitRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test User",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test User",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, string(out))
	}

	runGit("init")
	runGit("config", "user.name", "Test User")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "commit.gpgsign", "false")

	// Create initial file & commit
	initFile := filepath.Join(dir, "README.md")
	err := os.WriteFile(initFile, []byte("# Test Repo\nInitial content\n"), 0644)
	require.NoError(t, err)

	runGit("add", "README.md")
	runGit("commit", "-m", "Initial commit")

	return dir
}

func TestHandleGitDiff_WorkingAndCommit(t *testing.T) {
	t.Parallel()

	repoDir := initTestGitRepo(t)

	// Create second commit
	file2 := filepath.Join(repoDir, "feature.txt")
	err := os.WriteFile(file2, []byte("line 1\nline 2\n"), 0644)
	require.NoError(t, err)

	cmd := exec.Command("git", "-C", repoDir, "add", "feature.txt")
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "-C", repoDir, "commit", "-m", "Add feature.txt")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	require.NoError(t, cmd.Run())

	// Get second commit hash
	revCmd := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD")
	revOut, err := revCmd.Output()
	require.NoError(t, err)
	secondCommitHash := string(bytes.TrimSpace(revOut))

	// Create uncommitted working changes (modify README.md, add untracked new.txt)
	err = os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test Repo\nModified content\n"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(repoDir, "untracked.txt"), []byte("untracked file\n"), 0644)
	require.NoError(t, err)

	// Setup test server with repository for session tests
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	sessionRepo := dbmodels.NewSessionRepository(testDB)
	srvWithRepo := &Server{repo: sessionRepo}

	validSessionID := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, sessionRepo.UpdateAgentSession(validSessionID, "test-agent", "", "", nil))
	sess, err := sessionRepo.GetSession(validSessionID)
	require.NoError(t, err)
	sess.RunDir = repoDir
	require.NoError(t, sessionRepo.SaveSession(sess))

	nonExistentSessionID := uuid.Must(uuid.NewV7()).String()

	// Session whose RunDir is not inside a git repository
	nonGitSessionID := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, sessionRepo.UpdateAgentSession(nonGitSessionID, "test-agent", "", "", nil))
	nonGitSess, err := sessionRepo.GetSession(nonGitSessionID)
	require.NoError(t, err)
	nonGitSess.RunDir = t.TempDir()
	require.NoError(t, sessionRepo.SaveSession(nonGitSess))

	tests := []struct {
		name          string
		server        *Server
		sessionParam  string
		commitParam   string
		expectStatus  int
		expectFiles   int
		expectError   bool
		checkFilename string
	}{
		{
			name:          "working tree diff with session_id param",
			server:        srvWithRepo,
			sessionParam:  validSessionID,
			commitParam:   "",
			expectStatus:  http.StatusOK,
			expectFiles:   2,
			expectError:   false,
			checkFilename: "README.md",
		},
		{
			name:          "commit specific diff with session_id param",
			server:        srvWithRepo,
			sessionParam:  validSessionID,
			commitParam:   secondCommitHash,
			expectStatus:  http.StatusOK,
			expectFiles:   1,
			expectError:   false,
			checkFilename: "feature.txt",
		},
		{
			name:         "nil repo with session_id returns 500",
			server:       &Server{},
			sessionParam: validSessionID,
			expectStatus: http.StatusInternalServerError,
			expectError:  true,
		},
		{
			name:         "invalid session_id format",
			server:       srvWithRepo,
			sessionParam: "invalid/session/id",
			expectStatus: http.StatusBadRequest,
			expectError:  true,
		},
		{
			name:         "non-existent session_id",
			server:       srvWithRepo,
			sessionParam: nonExistentSessionID,
			expectStatus: http.StatusNotFound,
			expectError:  true,
		},
		{
			name:         "session rundir not inside git repository",
			server:       srvWithRepo,
			sessionParam: nonGitSessionID,
			expectStatus: http.StatusBadRequest,
			expectError:  true,
		},
		{
			name:         "missing session_id param returns 400",
			server:       &Server{},
			sessionParam: "",
			expectStatus: http.StatusBadRequest,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			testSrv := srvWithRepo
			if tt.server != nil {
				testSrv = tt.server
			}

			q := url.Values{}
			if tt.sessionParam != "" {
				q.Set("session_id", tt.sessionParam)
			}
			if tt.commitParam != "" {
				q.Set("commit", tt.commitParam)
			}

			reqURL := "/api/git/diff"
			if encoded := q.Encode(); encoded != "" {
				reqURL += "?" + encoded
			}

			req := httptest.NewRequest(http.MethodGet, reqURL, nil)
			w := httptest.NewRecorder()

			testSrv.handleGitDiff(w, req)

			assert.Equal(t, tt.expectStatus, w.Code)
			if tt.expectError {
				var errResp map[string]string
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err)
				assert.Contains(t, errResp, "error")
			} else {
				var diffResp GitDiffResponse
				err := json.Unmarshal(w.Body.Bytes(), &diffResp)
				require.NoError(t, err)
				assert.Len(t, diffResp.Files, tt.expectFiles)
				if tt.checkFilename != "" {
					found := false
					for _, f := range diffResp.Files {
						if f.NewPath == tt.checkFilename {
							found = true
							break
						}
					}
					assert.True(t, found, "expected file %s in diff files", tt.checkFilename)
				}
			}
		})
	}
}

func TestHandleGitDiff_DeletedFiles(t *testing.T) {
	t.Parallel()

	repoDir := initTestGitRepo(t)

	currentHash := func() string {
		t.Helper()
		out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
		require.NoError(t, err)
		return string(bytes.TrimSpace(out))
	}

	// Commit 2: add feature.txt
	err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("line 1\nline 2\n"), 0644)
	require.NoError(t, err)
	out, err := exec.Command("git", "-C", repoDir, "add", "feature.txt").CombinedOutput()
	require.NoError(t, err, "%s", out)
	out, err = exec.Command("git", "-C", repoDir, "commit", "-m", "Add feature.txt").CombinedOutput()
	require.NoError(t, err, "%s", out)
	addCommitHash := currentHash()

	// Commit 3: delete feature.txt
	err = os.Remove(filepath.Join(repoDir, "feature.txt"))
	require.NoError(t, err)
	out, err = exec.Command("git", "-C", repoDir, "add", "-A").CombinedOutput()
	require.NoError(t, err, "%s", out)
	out, err = exec.Command("git", "-C", repoDir, "commit", "-m", "Delete feature.txt").CombinedOutput()
	require.NoError(t, err, "%s", out)
	deleteCommitHash := currentHash()

	// Working tree: delete README.md (uncommitted)
	err = os.Remove(filepath.Join(repoDir, "README.md"))
	require.NoError(t, err)

	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	sessionRepo := dbmodels.NewSessionRepository(testDB)
	srv := &Server{repo: sessionRepo}

	sessionID := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, sessionRepo.UpdateAgentSession(sessionID, "test-agent", "", "", nil))
	sess, err := sessionRepo.GetSession(sessionID)
	require.NoError(t, err)
	sess.RunDir = repoDir
	require.NoError(t, sessionRepo.SaveSession(sess))

	doRequest := func(commitParam string) GitDiffResponse {
		t.Helper()
		reqURL := "/api/git/diff?session_id=" + sessionID
		if commitParam != "" {
			reqURL += "&commit=" + url.QueryEscape(commitParam)
		}
		req := httptest.NewRequest(http.MethodGet, reqURL, nil)
		w := httptest.NewRecorder()
		srv.handleGitDiff(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		var resp GitDiffResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		return resp
	}

	findFile := func(files []GitDiffFile, path string) *GitDiffFile {
		for i := range files {
			if files[i].NewPath == path {
				return &files[i]
			}
		}
		return nil
	}

	t.Run("working tree includes deleted file", func(t *testing.T) {
		resp := doRequest("")
		f := findFile(resp.Files, "README.md")
		if assert.NotNil(t, f, "deleted README.md should be listed") {
			assert.Equal(t, "D", f.Status)
			assert.Equal(t, "README.md", f.OldPath)
			assert.Contains(t, f.OldContent, "Initial content")
			assert.Empty(t, f.NewContent)
			assert.NotEmpty(t, f.Hunks)
			assert.Contains(t, f.Hunks[0], "-Initial content")
		}
	})

	t.Run("commit diff includes deleted file", func(t *testing.T) {
		resp := doRequest(deleteCommitHash)
		f := findFile(resp.Files, "feature.txt")
		if assert.NotNil(t, f, "deleted feature.txt should be listed in commit diff") {
			assert.Equal(t, "D", f.Status)
			assert.Contains(t, f.OldContent, "line 1")
			assert.Empty(t, f.NewContent)
			assert.NotEmpty(t, f.Hunks)
		}
	})

	t.Run("commit diff reports added file status", func(t *testing.T) {
		resp := doRequest(addCommitHash)
		f := findFile(resp.Files, "feature.txt")
		if assert.NotNil(t, f) {
			assert.Equal(t, "A", f.Status)
		}
	})
}

func TestHandleGitLog_TableDriven(t *testing.T) {
	t.Parallel()

	repoDir := initTestGitRepo(t)

	// Add 2 more commits
	for i := 1; i <= 2; i++ {
		fname := filepath.Join(repoDir, "file"+string(rune('0'+i))+".txt")
		err := os.WriteFile(fname, []byte("content\n"), 0644)
		require.NoError(t, err)
		cmd := exec.Command("git", "-C", repoDir, "add", ".")
		require.NoError(t, cmd.Run())
		cmd = exec.Command("git", "-C", repoDir, "commit", "-m", "Commit "+string(rune('0'+i)))
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test User", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test User", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		require.NoError(t, cmd.Run())
	}

	// Create unstashed change
	err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("modified\n"), 0644)
	require.NoError(t, err)

	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	sessionRepo := dbmodels.NewSessionRepository(testDB)
	srvWithRepo := &Server{repo: sessionRepo}

	validSessionID := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, sessionRepo.UpdateAgentSession(validSessionID, "test-agent", "", "", nil))
	sess, err := sessionRepo.GetSession(validSessionID)
	require.NoError(t, err)
	sess.RunDir = repoDir
	require.NoError(t, sessionRepo.SaveSession(sess))

	// Session whose RunDir is not inside a git repository
	nonGitSessionID := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, sessionRepo.UpdateAgentSession(nonGitSessionID, "test-agent", "", "", nil))
	nonGitSess, err := sessionRepo.GetSession(nonGitSessionID)
	require.NoError(t, err)
	nonGitSess.RunDir = t.TempDir()
	require.NoError(t, sessionRepo.SaveSession(nonGitSess))

	tests := []struct {
		name          string
		server        *Server
		sessionParam  string
		limitParam    string
		expectStatus  int
		expectCommits int
		expectUnstash int
		expectError   bool
	}{
		{
			name:          "valid git log with 3 commits and 1 unstashed",
			server:        srvWithRepo,
			sessionParam:  validSessionID,
			limitParam:    "10",
			expectStatus:  http.StatusOK,
			expectCommits: 3,
			expectUnstash: 1,
			expectError:   false,
		},
		{
			name:          "limit commits to 2",
			server:        srvWithRepo,
			sessionParam:  validSessionID,
			limitParam:    "2",
			expectStatus:  http.StatusOK,
			expectCommits: 2,
			expectUnstash: 1,
			expectError:   false,
		},
		{
			name:         "missing session_id param",
			server:       srvWithRepo,
			sessionParam: "",
			expectStatus: http.StatusBadRequest,
			expectError:  true,
		},
		{
			name:         "invalid session_id format",
			server:       srvWithRepo,
			sessionParam: "invalid/id",
			expectStatus: http.StatusBadRequest,
			expectError:  true,
		},
		{
			name:         "session rundir not inside git repository",
			server:       srvWithRepo,
			sessionParam: nonGitSessionID,
			expectStatus: http.StatusBadRequest,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			q := url.Values{}
			if tt.sessionParam != "" {
				q.Set("session_id", tt.sessionParam)
			}
			if tt.limitParam != "" {
				q.Set("limit", tt.limitParam)
			}

			reqURL := "/api/git/log"
			if encoded := q.Encode(); encoded != "" {
				reqURL += "?" + encoded
			}

			req := httptest.NewRequest(http.MethodGet, reqURL, nil)
			w := httptest.NewRecorder()

			tt.server.handleGitLog(w, req)

			assert.Equal(t, tt.expectStatus, w.Code)
			if tt.expectError {
				var errResp map[string]string
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err)
				assert.Contains(t, errResp, "error")
			} else {
				var logResp GitLogResponse
				err := json.Unmarshal(w.Body.Bytes(), &logResp)
				require.NoError(t, err)
				assert.Len(t, logResp.Commits, tt.expectCommits)
				assert.Equal(t, tt.expectUnstash, logResp.UnstashedCount)
				assert.NotEmpty(t, logResp.Commits[0].Hash)
				assert.NotEmpty(t, logResp.Commits[0].ShortHash)
				assert.NotEmpty(t, logResp.Commits[0].Message)
			}
		})
	}
}

func TestHandleGitPushPull_Validation(t *testing.T) {
	t.Parallel()

	srv := &Server{}

	t.Run("missing session_id in push body", func(t *testing.T) {
		t.Parallel()
		body := bytes.NewBufferString(`{}`)
		req := httptest.NewRequest(http.MethodPost, "/api/git/push", body)
		w := httptest.NewRecorder()

		srv.handleGitPush(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("missing session_id in pull body", func(t *testing.T) {
		t.Parallel()
		body := bytes.NewBufferString(`{}`)
		req := httptest.NewRequest(http.MethodPost, "/api/git/pull", body)
		w := httptest.NewRecorder()

		srv.handleGitPull(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	// Sessions with a RunDir that is not inside a git repository must be
	// rejected before invoking git push/pull.
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	sessionRepo := dbmodels.NewSessionRepository(testDB)
	srvWithRepo := &Server{repo: sessionRepo}

	nonGitSessionID := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, sessionRepo.UpdateAgentSession(nonGitSessionID, "test-agent", "", "", nil))
	nonGitSess, err := sessionRepo.GetSession(nonGitSessionID)
	require.NoError(t, err)
	nonGitSess.RunDir = t.TempDir()
	require.NoError(t, sessionRepo.SaveSession(nonGitSess))

	for _, action := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"push", srvWithRepo.handleGitPush},
		{"pull", srvWithRepo.handleGitPull},
	} {
		t.Run("non-git rundir "+action.name, func(t *testing.T) {
			t.Parallel()
			body := bytes.NewBufferString(`{"session_id":"` + nonGitSessionID + `"}`)
			req := httptest.NewRequest(http.MethodPost, "/api/git/"+action.name, body)
			w := httptest.NewRecorder()

			action.handler(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)

			var resp GitActionResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.False(t, resp.Success)
			assert.Contains(t, resp.Error, "not inside a git repository")
		})
	}
}
