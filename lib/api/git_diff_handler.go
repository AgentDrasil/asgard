package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// handleGitDiff handles GET /api/git/diff?dir=<path>.
// It collects tracked changes (git diff HEAD + staged) and untracked new files.
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
	// `git ls-files --others --exclude-standard` lists files not yet tracked by git.
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
