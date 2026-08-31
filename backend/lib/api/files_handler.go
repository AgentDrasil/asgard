package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
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
	Path string `json:"path"` // relative to session runDir
	Name string `json:"name"`
	Ext  string `json:"ext"`
	Size int64  `json:"size"`
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

	sess, err := s.repo.GetSession(sessionID)
	if err != nil {
		log.Error().Err(err).Str("session_id", sessionID).Msg("Failed to query session for workspace")
		return "", http.StatusInternalServerError, errors.New("failed to query session")
	}
	if sess == nil {
		return "", http.StatusNotFound, errors.New("session not found")
	}
	if sess.RunDir == "" {
		return "", http.StatusBadRequest, errors.New("session has no associated workspace directory")
	}

	return NormalizeSessionRunDir(sess.RunDir, sessionID), http.StatusOK, nil
}

func (s *Server) validateAndResolvePath(runDir, reqPath string) (string, string, int, error) {
	cleanRunDir := filepath.Clean(runDir)
	evalRunDir, err := filepath.EvalSymlinks(cleanRunDir)
	if err != nil {
		return "", "", http.StatusInternalServerError, fmt.Errorf("failed to evaluate workspace directory: %w", err)
	}

	cleanReq := filepath.Clean(strings.TrimPrefix(reqPath, "/"))
	if cleanReq == "." || reqPath == "" {
		return evalRunDir, "", http.StatusOK, nil
	}

	absTarget := filepath.Join(evalRunDir, cleanReq)

	// Pre-check lexical containment before symlink resolution
	relLex, errLex := filepath.Rel(evalRunDir, absTarget)
	if errLex != nil || strings.HasPrefix(relLex, "..") {
		return "", "", http.StatusForbidden, errors.New("access denied: path escapes workspace boundary")
	}

	evalTarget, err := filepath.EvalSymlinks(absTarget)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
			return "", "", http.StatusNotFound, fmt.Errorf("file not found: %s", reqPath)
		}
		return "", "", http.StatusBadRequest, fmt.Errorf("failed to evaluate path: %w", err)
	}

	rel, err := filepath.Rel(evalRunDir, evalTarget)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", http.StatusForbidden, errors.New("access denied: path escapes workspace boundary")
	}

	relClean := filepath.ToSlash(rel)
	if relClean == "." {
		relClean = ""
	}

	return evalTarget, relClean, http.StatusOK, nil
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

	runDir, code, err := s.resolveSessionWorkspace(sessionID)
	if err != nil {
		writeJSONError(w, code, err.Error())
		return
	}

	subPath := r.URL.Query().Get("path")
	targetDir, relPath, code, err := s.validateAndResolvePath(runDir, subPath)
	if err != nil {
		writeJSONError(w, code, err.Error())
		return
	}

	info, err := os.Stat(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSONError(w, http.StatusNotFound, "directory not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to stat directory: "+err.Error())
		return
	}
	if !info.IsDir() {
		writeJSONError(w, http.StatusBadRequest, "requested path is a file, not a directory")
		return
	}

	dirEntries, err := os.ReadDir(targetDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read directory: "+err.Error())
		return
	}

	var entries []FileTreeEntry
	for _, entry := range dirEntries {
		name := entry.Name()
		if name == ".git" || name == "node_modules" {
			continue
		}

		entryPath := name
		if relPath != "" {
			entryPath = filepath.ToSlash(filepath.Join(relPath, name))
		}

		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			targetEntry := filepath.Join(targetDir, name)
			if statInfo, statErr := os.Stat(targetEntry); statErr == nil {
				isDir = statInfo.IsDir()
			}
		}

		if isDir {
			entries = append(entries, FileTreeEntry{
				Name:  name,
				Path:  entryPath,
				IsDir: true,
			})
		} else {
			var size int64
			if entryInfo, infoErr := entry.Info(); infoErr == nil {
				size = entryInfo.Size()
			} else if statInfo, statErr := os.Stat(filepath.Join(targetDir, name)); statErr == nil {
				size = statInfo.Size()
			}
			ext := strings.TrimPrefix(filepath.Ext(name), ".")
			entries = append(entries, FileTreeEntry{
				Name:  name,
				Path:  entryPath,
				IsDir: false,
				Size:  size,
				Ext:   ext,
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})

	if entries == nil {
		entries = []FileTreeEntry{}
	}

	resp := FileTreeResponse{
		Entries: entries,
		Path:    relPath,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleFilesContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	filePath := r.URL.Query().Get("path")

	if sessionID == "" || filePath == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id and path are required parameters")
		return
	}

	runDir, code, err := s.resolveSessionWorkspace(sessionID)
	if err != nil {
		writeJSONError(w, code, err.Error())
		return
	}

	absTarget, relPath, code, err := s.validateAndResolvePath(runDir, filePath)
	if err != nil {
		writeJSONError(w, code, err.Error())
		return
	}

	info, err := os.Stat(absTarget)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSONError(w, http.StatusNotFound, "file not found: "+filePath)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to stat file: "+err.Error())
		return
	}

	if info.IsDir() {
		writeJSONError(w, http.StatusBadRequest, "requested path is a directory, not a file")
		return
	}

	ext := strings.TrimPrefix(filepath.Ext(absTarget), ".")
	name := filepath.Base(absTarget)

	rawParam := r.URL.Query().Get("raw")
	isRaw := rawParam == "1" || strings.EqualFold(rawParam, "true")

	if isRaw {
		// Whitelist check: only allow media/PDF files
		if !isMediaExt(ext) {
			writeJSONError(w, http.StatusForbidden, "access denied: streaming is only permitted for media files")
			return
		}

		file, err := os.Open(absTarget)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to open file: "+err.Error())
			return
		}
		defer func() { _ = file.Close() }()

		mimeType := detectMimeType(ext, nil)
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")

		http.ServeContent(w, r, name, info.ModTime(), file)
		return
	}

	// Non-raw mode: check if media or binary without full memory buffer
	isBinary, err := isBinaryOrMediaFile(absTarget, ext)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to probe file: "+err.Error())
		return
	}

	var contentStr string
	if isBinary {
		contentStr = ""
	} else {
		// Only check size limit for text files
		if info.Size() > maxReadFileSize {
			writeJSONError(w, http.StatusBadRequest, "file size exceeds maximum allowed limit (5MB)")
			return
		}

		contentBytes, err := os.ReadFile(absTarget)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read file content: "+err.Error())
			return
		}
		contentStr = string(contentBytes)
	}

	resp := FileContentResponse{
		Path:      relPath,
		Name:      name,
		Ext:       ext,
		Size:      info.Size(),
		Content:   contentStr,
		IsBinary:  isBinary,
		UpdatedAt: info.ModTime(),
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

	runDir, code, err := s.resolveSessionWorkspace(sessionID)
	if err != nil {
		writeJSONError(w, code, err.Error())
		return
	}

	cleanRunDir := filepath.Clean(runDir)
	evalRunDir, err := filepath.EvalSymlinks(cleanRunDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to evaluate workspace directory: "+err.Error())
		return
	}

	query := r.URL.Query().Get("query")
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if parsed, parseErr := strconv.Atoi(limitStr); parseErr == nil {
			limit = parsed
		}
	}
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
	}

	var results []FileSearchResult
	queryLower := strings.ToLower(query)

	_ = filepath.WalkDir(evalRunDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}

		if d.Type()&os.ModeSymlink != 0 {
			info, statErr := os.Stat(p)
			if statErr == nil && info.IsDir() {
				if name == ".git" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
		}

		rel, relErr := filepath.Rel(evalRunDir, p)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			return nil
		}

		relSlash := filepath.ToSlash(rel)
		if queryLower == "" || strings.Contains(strings.ToLower(relSlash), queryLower) {
			var size int64
			if info, infoErr := d.Info(); infoErr == nil {
				size = info.Size()
			} else if statInfo, statErr := os.Stat(p); statErr == nil {
				size = statInfo.Size()
			}

			results = append(results, FileSearchResult{
				Path: relSlash,
				Name: name,
				Ext:  strings.TrimPrefix(filepath.Ext(name), "."),
				Size: size,
			})

			if len(results) >= limit {
				return filepath.SkipAll
			}
		}

		return nil
	})

	if results == nil {
		results = []FileSearchResult{}
	}

	resp := FileSearchResponse{
		Files: results,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
