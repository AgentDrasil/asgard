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
	Scope string `json:"scope,omitempty"` // "workspace" | "tmp" | "session"
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

// workspaceFS resolves what a files request needs: the chat's FS and its
// (normalized) run dir, after the backend-side run dir check. It answers the
// error itself and returns ok=false when the request is over.
func (s *Server) workspaceFS(w http.ResponseWriter, sessionID string, checkRunDir bool) (runnerfs.FS, string, bool) {
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

	fs, runDir, ok := s.workspaceFS(w, sessionID, needsWorkspace(subPath, scope, sessionID))
	if !ok {
		return
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

	fs, runDir, ok := s.workspaceFS(w, sessionID, needsWorkspace(filePath, scope, sessionID))
	if !ok {
		return
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

	fs, runDir, ok := s.workspaceFS(w, sessionID, true)
	if !ok {
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

	hits, err := fs.Search(r.Context(), runnerfs.SearchRequest{RunDir: runDir, Query: r.URL.Query().Get("query"), Limit: limit})
	if err != nil {
		writeFSError(w, err)
		return
	}
	results := make([]FileSearchResult, 0, len(hits))
	for _, h := range hits {
		results = append(results, FileSearchResult{Path: h.Path, Name: h.Name, Ext: h.Ext, Size: h.Size, Scope: h.Scope})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(FileSearchResponse{Files: results})
}
