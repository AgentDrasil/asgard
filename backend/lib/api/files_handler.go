package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
)

type FileTreeEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"` // relative to session runDir
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size,omitempty"`
	Ext   string `json:"ext,omitempty"`
}

type FileTreeResponse struct {
	Entries []FileTreeEntry `json:"entries"`
	Path    string          `json:"path"`
}

type FileContentResponse struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Ext       string    `json:"ext"`
	Size      int64     `json:"size"`
	Content   string    `json:"content"`
	IsBinary  bool      `json:"isBinary,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type FileSearchResult struct {
	Path  string `json:"path"` // relative to session runDir, or prefixed with /tmp for session tmp
	Name  string `json:"name"`
	Ext   string `json:"ext"`
	Size  int64  `json:"size"`
	Scope string `json:"scope,omitempty"` // "workspace" | "tmp" | "session" | "shared"
}

type FileSearchResponse struct {
	Files []FileSearchResult `json:"files"`
}

func (s *Server) resolveSessionWorkspace(sessionID string) (string, int, error) {
	if s.repo == nil {
		return "", http.StatusInternalServerError, errors.New("session repository unavailable")
	}

	if !IsValidChatID(sessionID) {
		return "", http.StatusBadRequest, errors.New("invalid session_id format")
	}

	sess, err := s.repo.GetSessionMeta(sessionID)
	if err != nil {
		log.Error().Err(err).Str("session_id", sessionID).Msg("Failed to query session for workspace")
		return "", http.StatusInternalServerError, errors.New("failed to query session")
	}
	if sess == nil {
		return "", http.StatusNotFound, errors.New("session not found")
	}

	runDir := NormalizeSessionRunDir(sess.RunDir, sessionID)
	if runDir == "" {
		return "", http.StatusBadRequest, errors.New("session has no associated workspace directory")
	}

	return runDir, http.StatusOK, nil
}

// needsWorkspace reports whether serving reqPath may touch the workspace (as
// opposed to an explicit /tmp or /session path), so the backend must vet the
// stored run dir before asking the runner.
func needsWorkspace(reqPath, scope, sessionID string) bool {
	for _, c := range agents.AuthCandidates(reqPath, scope, sessionID) {
		if c.Ns == "" {
			return true
		}
	}
	return false
}

// sharedFSFor resolves what a /shared request needs: the FS of the spawn tree's
// root chat, after the session ownership check. It answers the error itself and
// returns ok=false when the request is over.
func (s *Server) sharedFSFor(w http.ResponseWriter, r *http.Request, sessionID string) (runnerfs.FS, bool) {
	sess, aerr := s.authorizeSession(r, sessionID)
	if aerr != nil {
		writeJSONError(w, aerr.status, aerr.msg)
		return nil, false
	}
	fs, err := s.sharedFS(sessionID, sess)
	if err != nil {
		writeFSError(w, err)
		return nil, false
	}
	return fs, true
}

// workspaceFS resolves what a files request needs: the chat's FS and its
// (normalized) run dir, after the backend-side run dir check. It answers the
// error itself and returns ok=false when the request is over.
func (s *Server) workspaceFS(w http.ResponseWriter, r *http.Request, sessionID string, checkRunDir bool) (runnerfs.FS, string, bool) {
	if _, aerr := s.authorizeSession(r, sessionID); aerr != nil {
		writeJSONError(w, aerr.status, aerr.msg)
		return nil, "", false
	}
	runDir, code, err := s.resolveSessionWorkspace(sessionID)
	if err != nil {
		writeJSONError(w, code, err.Error())
		return nil, "", false
	}
	if checkRunDir {
		if code, msg := s.checkWorkspaceRunDir(sessionID, runDir); code != 0 {
			writeJSONError(w, code, msg)
			return nil, "", false
		}
	}
	fs, ferr := s.chatFS(sessionID)
	if ferr != nil {
		writeFSError(w, ferr)
		return nil, "", false
	}
	return fs, runDir, true
}

func (s *Server) handleFilesTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	subPath := r.URL.Query().Get("path")
	scope := r.URL.Query().Get("scope")

	var fs runnerfs.FS
	var runDir string
	if canon, shared, serr := sharedRequest(subPath, scope); shared {
		if serr != nil {
			writeFSError(w, serr)
			return
		}
		var ok bool
		if fs, ok = s.sharedFSFor(w, r, sessionID); !ok {
			return
		}
		subPath, scope = canon, agents.SharedNS
	} else {
		var ok bool
		if fs, runDir, ok = s.workspaceFS(w, r, sessionID, needsWorkspace(subPath, scope, sessionID)); !ok {
			return
		}
	}
	res, err := fs.Tree(r.Context(), runnerfs.TreeRequest{RunDir: runDir, Path: subPath, Scope: scope})
	if err != nil {
		writeFSError(w, err)
		return
	}

	entries := make([]FileTreeEntry, 0, len(res.Entries))
	for _, e := range res.Entries {
		entries = append(entries, FileTreeEntry{Name: e.Name, Path: e.Path, IsDir: e.IsDir, Size: e.Size, Ext: e.Ext})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(FileTreeResponse{Entries: entries, Path: res.Path})
}

func (s *Server) handleFilesContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	filePath := r.URL.Query().Get("path")
	scope := r.URL.Query().Get("scope")

	if sessionID == "" || filePath == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id and path are required parameters")
		return
	}

	var fs runnerfs.FS
	var runDir string
	if canon, shared, serr := sharedRequest(filePath, scope); shared {
		if serr != nil {
			writeFSError(w, serr)
			return
		}
		var ok bool
		if fs, ok = s.sharedFSFor(w, r, sessionID); !ok {
			return
		}
		filePath, scope = canon, agents.SharedNS
	} else {
		var ok bool
		if fs, runDir, ok = s.workspaceFS(w, r, sessionID, needsWorkspace(filePath, scope, sessionID)); !ok {
			return
		}
	}

	rawParam := r.URL.Query().Get("raw")
	isRaw := rawParam == "1" || strings.EqualFold(rawParam, "true")

	f, err := fs.Open(r.Context(), runnerfs.ReadRequest{RunDir: runDir, Path: filePath, Scope: scope, Raw: isRaw})
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer func() { _ = f.Close() }()

	if isRaw {
		serveRawMedia(w, r, f, f.Ext, f.Name, f.ModTime)
		return
	}

	var contentStr string
	if !f.IsBinary {
		// Only check size limit for text files
		if f.Size > maxReadFileSize {
			writeJSONError(w, http.StatusBadRequest, "file size exceeds maximum allowed limit (5MB)")
			return
		}
		contentBytes, err := io.ReadAll(f)
		if err != nil {
			writeFSError(w, runnerfs.ToError(err))
			return
		}
		contentStr = string(contentBytes)
	}

	resp := FileContentResponse{
		Path:      f.Path,
		Name:      f.Name,
		Ext:       f.Ext,
		Size:      f.Size,
		Content:   contentStr,
		IsBinary:  f.IsBinary,
		UpdatedAt: f.ModTime,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleFilesSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	limit := 50
	if parsed, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = parsed
	}
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
	}
	query := r.URL.Query().Get("query")

	var hits []runnerfs.SearchHit
	if r.URL.Query().Get("scope") == agents.SharedNS {
		// Only the spawn tree's /shared directory.
		fs, ok := s.sharedFSFor(w, r, sessionID)
		if !ok {
			return
		}
		shared, err := fs.Search(r.Context(), runnerfs.SearchRequest{Query: query, Limit: limit, Scope: agents.SharedNS})
		if err != nil {
			writeFSError(w, err)
			return
		}
		hits = onlySharedHits(shared)
	} else {
		// A chat that runs with a /shared directory reserves a quarter of the
		// limit for its files, so they are not starved by workspace hits.
		var sess *dbmodels.Session
		if s.repo != nil {
			sess, _ = s.repo.GetSessionMeta(sessionID)
		}
		// Nothing is reserved at limit 1 (the main search needs at least 1).
		withShared := limit >= 2 && s.chatHasShared(sess)
		mainLimit := limit
		if withShared {
			mainLimit = limit - max(limit/4, 1)
		}
		fs, runDir, ok := s.workspaceFS(w, r, sessionID, true)
		if !ok {
			return
		}
		var err error
		if hits, err = fs.Search(r.Context(), runnerfs.SearchRequest{RunDir: runDir, Query: query, Limit: mainLimit}); err != nil {
			writeFSError(w, err)
			return
		}
		// Best effort: a failing lookup must not fail the search.
		if rest := limit - len(hits); withShared && rest > 0 {
			hits = append(hits, s.searchSharedBestEffort(r, sessionID, sess, query, rest)...)
		}
		if len(hits) > limit {
			hits = hits[:limit]
		}
	}
	results := make([]FileSearchResult, 0, len(hits))
	for _, h := range hits {
		results = append(results, FileSearchResult{Path: h.Path, Name: h.Name, Ext: h.Ext, Size: h.Size, Scope: h.Scope})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(FileSearchResponse{Files: results})
}

// onlySharedHits keeps the hits that really come from /shared. A runner that
// predates the shared scope ignores it and answers with the root chat's
// workspace, tmp and session hits, which must not show up as shared files.
func onlySharedHits(hits []runnerfs.SearchHit) []runnerfs.SearchHit {
	out := make([]runnerfs.SearchHit, 0, len(hits))
	for _, h := range hits {
		if h.Scope == agents.SharedNS && strings.HasPrefix(h.Path, "/shared/") {
			out = append(out, h)
		}
	}
	return out
}

// searchSharedBestEffort searches the /shared directory of the session's spawn
// tree, returning nothing on any failure.
func (s *Server) searchSharedBestEffort(r *http.Request, sessionID string, sess *dbmodels.Session, query string, limit int) []runnerfs.SearchHit {
	fs, err := s.sharedFS(sessionID, sess)
	if err != nil {
		return nil
	}
	hits, err := fs.Search(r.Context(), runnerfs.SearchRequest{Query: query, Limit: limit, Scope: agents.SharedNS})
	if err != nil {
		return nil
	}
	return onlySharedHits(hits)
}
