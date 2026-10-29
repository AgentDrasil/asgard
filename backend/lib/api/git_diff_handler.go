package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
)

// GitDiffFile holds the before/after content and raw unified-diff hunks for one file.
type GitDiffFile struct {
	OldPath    string   `json:"oldPath"`
	NewPath    string   `json:"newPath"`
	Status     string   `json:"status"` // "A" | "M" | "D" | "R"
	OldContent string   `json:"oldContent"`
	NewContent string   `json:"newContent"`
	Hunks      []string `json:"hunks"`
	// ContentTruncated/HunksTruncated are set when the runner cut the file
	// contents (4 MiB per side) or the hunks (4 MiB) to its caps.
	ContentTruncated bool `json:"contentTruncated,omitempty"`
	HunksTruncated   bool `json:"hunksTruncated,omitempty"`
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
	SessionID string `json:"session_id"`
}

// GitActionResponse is the response payload for git push/pull.
type GitActionResponse struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

// handleGitDiff handles GET /api/git/diff?session_id=<id>&commit=<hash>.
// When commit is empty or "unstash", it collects tracked changes and untracked files.
// When commit is specified, it returns the changes introduced in that commit.
// The repository work happens on the chat's runner.
func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	fs, runDir, ok := s.workspaceFS(w, sessionID, true)
	if !ok {
		return
	}

	files := make([]GitDiffFile, 0)
	err := fs.GitDiff(r.Context(), runnerfs.GitDiffRequest{RunDir: runDir, Commit: strings.TrimSpace(r.URL.Query().Get("commit"))}, func(f runnerfs.GitDiffFile) bool {
		files = append(files, GitDiffFile{
			OldPath: f.OldPath, NewPath: f.NewPath, Status: f.Status, OldContent: f.OldContent,
			NewContent: f.NewContent, Hunks: f.Hunks, ContentTruncated: f.ContentTruncated, HunksTruncated: f.HunksTruncated,
		})
		return true
	})
	if err != nil {
		if runnerfs.CodeOf(err) == runnerfs.CodeInternal {
			log.Error().Err(err).Str("session_id", sessionID).Msg("git diff failed")
		}
		writeFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(GitDiffResponse{Files: files})
}

// handleGitLog handles GET /api/git/log?session_id=<id>&limit=10.
func (s *Server) handleGitLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	fs, runDir, ok := s.workspaceFS(w, sessionID, true)
	if !ok {
		return
	}

	limit := 10
	if limitParam := r.URL.Query().Get("limit"); limitParam != "" {
		if parsed, err := strconv.Atoi(limitParam); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	res, err := fs.GitLog(r.Context(), runDir, limit)
	if err != nil {
		writeFSError(w, err)
		return
	}
	commits := make([]GitCommit, 0, len(res.Commits))
	for _, c := range res.Commits {
		commits = append(commits, GitCommit{
			Hash: c.Hash, ShortHash: c.ShortHash, Author: c.Author, AuthorEmail: c.AuthorEmail,
			Date: c.Date, RelativeDate: c.RelativeDate, Refs: c.Refs, Message: c.Message,
		})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(GitLogResponse{
		Commits: commits, CurrentBranch: res.CurrentBranch, TrackingBranch: res.TrackingBranch,
		Ahead: res.Ahead, Behind: res.Behind, UnstashedCount: res.UnstashedCount,
	})
}

func writeGitActionError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(GitActionResponse{Success: false, Error: msg})
}

// handleGitAction serves push and pull: the git command runs on the chat's
// runner; a retried call is idempotent by request id.
func (s *Server) handleGitAction(w http.ResponseWriter, r *http.Request, op func(runnerfs.FS, *http.Request, string, string) (runnerfs.GitOpResult, error)) {
	w.Header().Set("Content-Type", "application/json")

	var req GitActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SessionID == "" {
		writeGitActionError(w, http.StatusBadRequest, "session_id is required in request body")
		return
	}

	fs, runDir, ok := s.gitActionFS(w, req.SessionID)
	if !ok {
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	res, err := op(fs, r, runDir, hex.EncodeToString(b))
	if err != nil {
		status, msg := fsErrorStatus(err)
		writeGitActionError(w, status, msg)
		return
	}
	if !res.Success {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(GitActionResponse{Success: false, Output: res.Output, Error: res.Error})
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(GitActionResponse{Success: true, Output: res.Output})
}

// gitActionFS is workspaceFS with GitActionResponse-shaped errors.
func (s *Server) gitActionFS(w http.ResponseWriter, sessionID string) (runnerfs.FS, string, bool) {
	runDir, code, err := s.resolveSessionWorkspace(sessionID)
	if err != nil {
		writeGitActionError(w, code, err.Error())
		return nil, "", false
	}
	if code, msg := s.checkWorkspaceRunDir(sessionID, runDir); code != 0 {
		writeGitActionError(w, code, msg)
		return nil, "", false
	}
	fs, ferr := s.chatFS(sessionID)
	if ferr != nil {
		status, msg := fsErrorStatus(ferr)
		writeGitActionError(w, status, msg)
		return nil, "", false
	}
	return fs, runDir, true
}

// handleGitPush handles POST /api/git/push.
func (s *Server) handleGitPush(w http.ResponseWriter, r *http.Request) {
	s.handleGitAction(w, r, func(fs runnerfs.FS, r *http.Request, runDir, id string) (runnerfs.GitOpResult, error) {
		return fs.GitPush(r.Context(), runDir, id)
	})
}

// handleGitPull handles POST /api/git/pull.
func (s *Server) handleGitPull(w http.ResponseWriter, r *http.Request) {
	s.handleGitAction(w, r, func(fs runnerfs.FS, r *http.Request, runDir, id string) (runnerfs.GitOpResult, error) {
		return fs.GitPull(r.Context(), runDir, id)
	})
}
