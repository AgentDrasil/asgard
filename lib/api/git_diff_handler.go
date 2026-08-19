package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

// GitDiffFile holds the before/after content and raw unified-diff hunks for one file.
type GitDiffFile struct {
	OldPath    string   `json:"oldPath"`
	NewPath    string   `json:"newPath"`
	OldContent string   `json:"oldContent"`
	NewContent string   `json:"newContent"`
	Hunks      []string `json:"hunks"`
}

// GitDiffResponse is the response payload for GET /api/git/diff.
type GitDiffResponse struct {
	Files []GitDiffFile `json:"files"`
}

// GitCommit represents a commit entry in git log.
type GitCommit struct {
	Hash         string `json:"hash"`
	ShortHash    string `json:"shortHash"`
	Author       string `json:"author"`
	AuthorEmail  string `json:"authorEmail"`
	Date         string `json:"date"`
	RelativeDate string `json:"relativeDate"`
	Refs         string `json:"refs"`
	Message      string `json:"message"`
}

// GitLogResponse is the response payload for GET /api/git/log.
type GitLogResponse struct {
	Commits        []GitCommit `json:"commits"`
	CurrentBranch  string      `json:"currentBranch"`
	TrackingBranch string      `json:"trackingBranch"`
	Ahead          int         `json:"ahead"`
	Behind         int         `json:"behind"`
	UnstashedCount int         `json:"unstashedCount"`
}

// GitActionRequest is the request payload for git actions like push/pull.
type GitActionRequest struct {
	Dir string `json:"dir"`
}

// GitActionResponse is the response payload for git push/pull.
type GitActionResponse struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

// handleGitDiff handles GET /api/git/diff?dir=<path>&commit=<hash>.
// When commit is empty or "unstash", it collects tracked changes and untracked files.
// When commit is specified, it returns the changes introduced in that commit.
func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	dirParam := r.URL.Query().Get("dir")
	if dirParam == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "dir parameter is required"})
		return
	}

	cleanDir := filepath.Clean(dirParam)
	if _, err := os.Stat(cleanDir); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "directory does not exist"})
		return
	}

	// Get git root for the directory
	gitRoot := findGitRoot(cleanDir)
	if gitRoot == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "directory is not inside a git repository"})
		return
	}

	commitParam := strings.TrimSpace(r.URL.Query().Get("commit"))
	if commitParam != "" && commitParam != "unstash" && commitParam != "working" {
		// Fetch diff for a specific commit
		files, err := getCommitDiff(gitRoot, commitParam)
		if err != nil {
			log.Error().Err(err).Str("dir", gitRoot).Str("commit", commitParam).Msg("failed to get commit diff")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("failed to get commit diff: %v", err)})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(GitDiffResponse{Files: files})
		return
	}

	// ── Tracked changed files (unstaged + staged) ─────────────────────────────
	nameStatusCmd := exec.Command("git", "-C", gitRoot, "diff", "HEAD", "--name-status")
	nameStatusOut, err := nameStatusCmd.Output()
	if err != nil {
		// Fallback for repos with no commits yet
		nameStatusCmd2 := exec.Command("git", "-C", gitRoot, "diff", "--name-status")
		nameStatusOut, err = nameStatusCmd2.Output()
		if err != nil {
			log.Error().Err(err).Str("dir", gitRoot).Msg("git diff --name-status failed")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "git diff failed"})
			return
		}
	}

	stagedCmd := exec.Command("git", "-C", gitRoot, "diff", "--cached", "--name-status")
	stagedOut, _ := stagedCmd.Output()

	changedFiles := parseNameStatus(string(nameStatusOut))
	stagedFiles := parseNameStatus(string(stagedOut))
	merged := mergeFileMaps(changedFiles, stagedFiles)

	// ── Untracked new files ───────────────────────────────────────────────────
	lsCmd := exec.Command("git", "-C", gitRoot, "ls-files", "--others", "--exclude-standard")
	lsOut, _ := lsCmd.Output()
	var untrackedPaths []string
	for _, line := range strings.Split(strings.TrimSpace(string(lsOut)), "\n") {
		if line != "" {
			untrackedPaths = append(untrackedPaths, line)
		}
	}

	// Build diff entries for tracked files
	files := make([]GitDiffFile, 0, len(merged)+len(untrackedPaths))
	for newPath, oldPath := range merged {
		entry, err := buildDiffEntry(gitRoot, oldPath, newPath)
		if err != nil {
			log.Warn().Err(err).Str("file", newPath).Msg("skipping file in git diff")
			continue
		}
		files = append(files, entry)
	}

	// Build diff entries for untracked files (show entire file as additions)
	for _, relPath := range untrackedPaths {
		entry, err := buildUntrackedEntry(gitRoot, relPath)
		if err != nil {
			log.Warn().Err(err).Str("file", relPath).Msg("skipping untracked file in git diff")
			continue
		}
		files = append(files, entry)
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(GitDiffResponse{Files: files})
}

// getCommitDiff returns the diff files for a specific commit.
func getCommitDiff(gitRoot, commit string) ([]GitDiffFile, error) {
	// Use diff-tree to find changed files in commit
	diffTreeCmd := exec.Command("git", "-C", gitRoot, "diff-tree", "--no-commit-id", "--name-status", "-r", commit)
	diffTreeOut, err := diffTreeCmd.Output()
	if err != nil {
		// Fallback to git show --name-status
		showCmd := exec.Command("git", "-C", gitRoot, "show", "--name-status", "--pretty=format:", commit)
		diffTreeOut, err = showCmd.Output()
		if err != nil {
			return nil, err
		}
	}

	changedFiles := parseNameStatus(string(diffTreeOut))
	files := make([]GitDiffFile, 0, len(changedFiles))

	for newPath, oldPath := range changedFiles {
		// Old content: commit~1:<oldPath>
		oldContent := ""
		oldShowCmd := exec.Command("git", "-C", gitRoot, "show", fmt.Sprintf("%s~1:%s", commit, oldPath))
		if out, err := oldShowCmd.Output(); err == nil {
			oldContent = string(out)
		}

		// New content: commit:<newPath>
		newContent := ""
		newShowCmd := exec.Command("git", "-C", gitRoot, "show", fmt.Sprintf("%s:%s", commit, newPath))
		if out, err := newShowCmd.Output(); err == nil {
			newContent = string(out)
		}

		// Unified diff for this file in the commit
		hunks := []string{}
		diffCmd := exec.Command("git", "-C", gitRoot, "show", "--pretty=format:", "--patch", commit, "--", newPath)
		if diffOut, err := diffCmd.Output(); err == nil && len(diffOut) > 0 {
			hunks = []string{strings.TrimSpace(string(diffOut))}
		}

		files = append(files, GitDiffFile{
			OldPath:    oldPath,
			NewPath:    newPath,
			OldContent: oldContent,
			NewContent: newContent,
			Hunks:      hunks,
		})
	}

	return files, nil
}

// handleGitLog handles GET /api/git/log?dir=<path>&limit=10.
func (s *Server) handleGitLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	dirParam := r.URL.Query().Get("dir")
	if dirParam == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "dir parameter is required"})
		return
	}

	cleanDir := filepath.Clean(dirParam)
	if _, err := os.Stat(cleanDir); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "directory does not exist"})
		return
	}

	gitRoot := findGitRoot(cleanDir)
	if gitRoot == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "directory is not inside a git repository"})
		return
	}

	limit := 10
	if limitParam := r.URL.Query().Get("limit"); limitParam != "" {
		if parsed, err := strconv.Atoi(limitParam); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	// 1. Fetch recent commits using null-byte separated fields
	logCmd := exec.Command("git", "-C", gitRoot, "log", fmt.Sprintf("-n%d", limit), "--pretty=format:%H%x00%h%x00%an%x00%ae%x00%aI%x00%ar%x00%D%x00%s")
	logOut, _ := logCmd.Output()

	commits := make([]GitCommit, 0)
	rawLog := strings.TrimSpace(string(logOut))
	if rawLog != "" {
		lines := strings.Split(rawLog, "\n")
		for _, line := range lines {
			parts := strings.Split(line, "\x00")
			if len(parts) >= 8 {
				commits = append(commits, GitCommit{
					Hash:         parts[0],
					ShortHash:    parts[1],
					Author:       parts[2],
					AuthorEmail:  parts[3],
					Date:         parts[4],
					RelativeDate: parts[5],
					Refs:         parts[6],
					Message:      parts[7],
				})
			}
		}
	}

	// 2. Fetch current branch name
	branchCmd := exec.Command("git", "-C", gitRoot, "branch", "--show-current")
	branchOut, err := branchCmd.Output()
	currentBranch := strings.TrimSpace(string(branchOut))
	if currentBranch == "" || err != nil {
		// Fallback for detached HEAD
		revCmd := exec.Command("git", "-C", gitRoot, "rev-parse", "--short", "HEAD")
		if revOut, err2 := revCmd.Output(); err2 == nil && len(revOut) > 0 {
			currentBranch = "HEAD (" + strings.TrimSpace(string(revOut)) + ")"
		}
	}

	// 3. Fetch tracking upstream branch
	trackingCmd := exec.Command("git", "-C", gitRoot, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	trackingOut, _ := trackingCmd.Output()
	trackingBranch := strings.TrimSpace(string(trackingOut))

	// 4. Fetch ahead/behind counts if tracking branch exists
	ahead := 0
	behind := 0
	if trackingBranch != "" {
		countCmd := exec.Command("git", "-C", gitRoot, "rev-list", "--left-right", "--count", "HEAD...@{u}")
		if countOut, err := countCmd.Output(); err == nil {
			fields := strings.Fields(strings.TrimSpace(string(countOut)))
			if len(fields) >= 2 {
				ahead, _ = strconv.Atoi(fields[0])
				behind, _ = strconv.Atoi(fields[1])
			}
		}
	}

	// 5. Fetch count of unstashed/uncommitted changes
	statusCmd := exec.Command("git", "-C", gitRoot, "status", "--porcelain")
	statusOut, _ := statusCmd.Output()
	unstashedCount := 0
	for _, l := range strings.Split(strings.TrimSpace(string(statusOut)), "\n") {
		if strings.TrimSpace(l) != "" {
			unstashedCount++
		}
	}

	resp := GitLogResponse{
		Commits:        commits,
		CurrentBranch:  currentBranch,
		TrackingBranch: trackingBranch,
		Ahead:          ahead,
		Behind:         behind,
		UnstashedCount: unstashedCount,
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleGitPush handles POST /api/git/push.
func (s *Server) handleGitPush(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req GitActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Dir == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(GitActionResponse{Success: false, Error: "dir is required in request body"})
		return
	}

	cleanDir := filepath.Clean(req.Dir)
	gitRoot := findGitRoot(cleanDir)
	if gitRoot == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(GitActionResponse{Success: false, Error: "directory is not inside a git repository"})
		return
	}

	cmd := exec.Command("git", "-C", gitRoot, "push")
	out, err := cmd.CombinedOutput()
	if err != nil {
		errMsg := strings.TrimSpace(string(out))
		if errMsg == "" {
			errMsg = err.Error()
		}
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(GitActionResponse{Success: false, Output: string(out), Error: errMsg})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(GitActionResponse{Success: true, Output: strings.TrimSpace(string(out))})
}

// handleGitPull handles POST /api/git/pull.
func (s *Server) handleGitPull(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req GitActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Dir == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(GitActionResponse{Success: false, Error: "dir is required in request body"})
		return
	}

	cleanDir := filepath.Clean(req.Dir)
	gitRoot := findGitRoot(cleanDir)
	if gitRoot == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(GitActionResponse{Success: false, Error: "directory is not inside a git repository"})
		return
	}

	cmd := exec.Command("git", "-C", gitRoot, "pull")
	out, err := cmd.CombinedOutput()
	if err != nil {
		errMsg := strings.TrimSpace(string(out))
		if errMsg == "" {
			errMsg = err.Error()
		}
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(GitActionResponse{Success: false, Output: string(out), Error: errMsg})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(GitActionResponse{Success: true, Output: strings.TrimSpace(string(out))})
}

// parseNameStatus parses the output of `git diff --name-status` into a map of newPath → oldPath.
// For renames: oldPath != newPath. For add/modify/delete: oldPath == newPath.
func parseNameStatus(output string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
		// Skip deleted files (no new content)
		if strings.HasPrefix(status, "D") {
			continue
		}
		// Rename: R<score> old new
		if strings.HasPrefix(status, "R") && len(parts) >= 3 {
			result[parts[2]] = parts[1]
			continue
		}
		// Add / Modify / Copy
		newPath := parts[1]
		result[newPath] = newPath
	}
	return result
}

// mergeFileMaps merges two newPath→oldPath maps, preferring entries from a over b.
func mergeFileMaps(a, b map[string]string) map[string]string {
	merged := make(map[string]string)
	for k, v := range b {
		merged[k] = v
	}
	for k, v := range a {
		merged[k] = v
	}
	return merged
}

// buildDiffEntry fetches old content from git HEAD and new content from disk,
// then returns the full unified diff output as a single string in the hunks slice.
// The @git-diff-view library's DiffParser.parse() requires a complete unified diff
// (including the --- +++ header lines); splitting by @@ would break parsing.
func buildDiffEntry(gitRoot, oldPath, newPath string) (GitDiffFile, error) {
	// Get old content via `git show HEAD:<oldPath>`
	oldContent := ""
	showCmd := exec.Command("git", "-C", gitRoot, "show", "HEAD:"+oldPath)
	if out, err := showCmd.Output(); err == nil {
		oldContent = string(out)
	}

	// Get new content from the working-tree file on disk
	newAbsPath := filepath.Join(gitRoot, newPath)
	newBytes, err := os.ReadFile(newAbsPath)
	newContent := ""
	if err == nil {
		newContent = string(newBytes)
	}

	// Get the complete unified diff for this file.
	// Pass it as a single element so DiffParser.parse() sees the full diff string.
	hunks := []string{}
	diffCmd := exec.Command("git", "-C", gitRoot, "diff", "HEAD", "--", newPath)
	if diffOut, err := diffCmd.Output(); err == nil && len(diffOut) > 0 {
		hunks = []string{string(diffOut)}
	} else {
		// Fall back to staged changes
		diffCmd2 := exec.Command("git", "-C", gitRoot, "diff", "--cached", "--", newPath)
		if diffOut2, err2 := diffCmd2.Output(); err2 == nil && len(diffOut2) > 0 {
			hunks = []string{string(diffOut2)}
		}
	}

	return GitDiffFile{
		OldPath:    oldPath,
		NewPath:    newPath,
		OldContent: oldContent,
		NewContent: newContent,
		Hunks:      hunks,
	}, nil
}

// buildUntrackedEntry handles files that are not yet tracked by git (untracked new files).
// It reads file content from disk and uses `git diff --no-index /dev/null <file>` to
// generate a proper unified diff showing all lines as additions.
// Note: `git diff --no-index` exits with code 1 when differences exist (always for new files),
// so we use cmd.Output() and accept any output we get regardless of exit code.
func buildUntrackedEntry(gitRoot, relPath string) (GitDiffFile, error) {
	absPath := filepath.Join(gitRoot, relPath)

	// Read file content from disk
	newBytes, err := os.ReadFile(absPath)
	if err != nil {
		return GitDiffFile{}, err
	}
	newContent := string(newBytes)

	// Generate a unified diff using git diff --no-index /dev/null <file>.
	// This always exits with code 1 (differences found), so we capture output
	// regardless of the exit error.
	diffCmd := exec.Command("git", "-C", gitRoot, "diff", "--no-index", "/dev/null", relPath)
	diffOut, _ := diffCmd.Output() // exit code 1 is expected — ignore the error

	hunks := []string{}
	if len(diffOut) > 0 {
		hunks = []string{string(diffOut)}
	}

	return GitDiffFile{
		OldPath:    "/dev/null",
		NewPath:    relPath,
		OldContent: "",
		NewContent: newContent,
		Hunks:      hunks,
	}, nil
}
