package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	srv := &Server{}

	tests := []struct {
		name          string
		dirParam      string
		commitParam   string
		expectStatus  int
		expectFiles   int
		expectError   bool
		checkFilename string
	}{
		{
			name:          "working tree diff (unstaged + untracked)",
			dirParam:      repoDir,
			commitParam:   "",
			expectStatus:  http.StatusOK,
			expectFiles:   2,
			expectError:   false,
			checkFilename: "README.md",
		},
		{
			name:          "commit specific diff",
			dirParam:      repoDir,
			commitParam:   secondCommitHash,
			expectStatus:  http.StatusOK,
			expectFiles:   1,
			expectError:   false,
			checkFilename: "feature.txt",
		},
		{
			name:         "missing dir param",
			dirParam:     "",
			expectStatus: http.StatusBadRequest,
			expectError:  true,
		},
		{
			name:         "non git dir",
			dirParam:     t.TempDir(),
			expectStatus: http.StatusBadRequest,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reqURL := "/api/git/diff"
			if tt.dirParam != "" {
				reqURL += "?dir=" + tt.dirParam
			}
			if tt.commitParam != "" {
				reqURL += "&commit=" + tt.commitParam
			}

			req := httptest.NewRequest(http.MethodGet, reqURL, nil)
			w := httptest.NewRecorder()

			srv.handleGitDiff(w, req)

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

	srv := &Server{}

	tests := []struct {
		name          string
		dirParam      string
		limitParam    string
		expectStatus  int
		expectCommits int
		expectUnstash int
		expectError   bool
	}{
		{
			name:          "valid git log with 3 commits and 1 unstashed",
			dirParam:      repoDir,
			limitParam:    "10",
			expectStatus:  http.StatusOK,
			expectCommits: 3,
			expectUnstash: 1,
			expectError:   false,
		},
		{
			name:          "limit commits to 2",
			dirParam:      repoDir,
			limitParam:    "2",
			expectStatus:  http.StatusOK,
			expectCommits: 2,
			expectUnstash: 1,
			expectError:   false,
		},
		{
			name:         "missing dir param",
			dirParam:     "",
			expectStatus: http.StatusBadRequest,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reqURL := "/api/git/log"
			if tt.dirParam != "" {
				reqURL += "?dir=" + tt.dirParam
			}
			if tt.limitParam != "" {
				reqURL += "&limit=" + tt.limitParam
			}

			req := httptest.NewRequest(http.MethodGet, reqURL, nil)
			w := httptest.NewRecorder()

			srv.handleGitLog(w, req)

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

	t.Run("missing dir in push body", func(t *testing.T) {
		t.Parallel()
		body := bytes.NewBufferString(`{}`)
		req := httptest.NewRequest(http.MethodPost, "/api/git/push", body)
		w := httptest.NewRecorder()

		srv.handleGitPush(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("missing dir in pull body", func(t *testing.T) {
		t.Parallel()
		body := bytes.NewBufferString(`{}`)
		req := httptest.NewRequest(http.MethodPost, "/api/git/pull", body)
		w := httptest.NewRecorder()

		srv.handleGitPull(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}
