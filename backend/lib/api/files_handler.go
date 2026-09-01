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

	runDir := NormalizeSessionRunDir(sess.RunDir, sessionID)
	if runDir == "" {
		return "", http.StatusBadRequest, errors.New("session has no associated workspace directory")
	}

	return runDir, http.StatusOK, nil
}

func (s *Server) validateAndResolvePath(sessionID, runDir, reqPath, scope string) (string, string, int, error) {
	cleanRunDir := filepath.Clean(runDir)
	baseTmp := GetSessionTmpBaseDir(sessionID)

	evalRunDir, err := filepath.EvalSymlinks(cleanRunDir)
	if err != nil {
		evalRunDir = cleanRunDir
	}

	evalTmpBase := baseTmp
	if et, evalErr := filepath.EvalSymlinks(baseTmp); evalErr == nil {
		evalTmpBase = et
	}

	cleanReq := filepath.Clean(reqPath)
	if cleanReq == "." || reqPath == "" {
		return evalRunDir, "", http.StatusOK, nil
	}

	// Case 1: Explicit session tmp path (/tmp, .tmp, /tmp/..., .tmp/...)
	if isTmp, sub := ResolveSessionTmpPath(cleanReq, sessionID); isTmp {
		sub = filepath.Clean(sub)
		if sub == "." || sub == "" {
			relResult := ""
			if evalRunDir != evalTmpBase {
				relResult = "/tmp"
			}
			return evalTmpBase, relResult, http.StatusOK, nil
		}

		absTarget := filepath.Join(baseTmp, sub)
		relLex, errLex := filepath.Rel(baseTmp, absTarget)
		if errLex != nil || strings.HasPrefix(relLex, "..") {
			return "", "", http.StatusForbidden, errors.New("access denied: path escapes temporary directory boundary")
		}

		evalTarget, err := filepath.EvalSymlinks(absTarget)
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
				return "", "", http.StatusNotFound, fmt.Errorf("file not found: %s", reqPath)
			}
			return "", "", http.StatusBadRequest, fmt.Errorf("failed to evaluate path: %w", err)
		}

		rel, err := filepath.Rel(evalTmpBase, evalTarget)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", "", http.StatusForbidden, errors.New("access denied: path escapes temporary directory boundary")
		}

		relClean := filepath.ToSlash(rel)
		if relClean == "." {
			relClean = ""
		}
		if evalRunDir != evalTmpBase {
			relClean = filepath.ToSlash(filepath.Join("/tmp", relClean))
		}
		return evalTarget, relClean, http.StatusOK, nil
	}

	// Case 2: Relative tmp/... path
	if isRelTmp, sub := isRelativeTmpPrefixedPath(cleanReq, sessionID); isRelTmp {
		sub = filepath.Clean(sub)
		if sub == "." {
			sub = ""
		}

		resolveAsTmp := func() (string, string, int, error) {
			if sub == "" {
				relResult := ""
				if evalRunDir != evalTmpBase {
					relResult = "/tmp"
				}
				return evalTmpBase, relResult, http.StatusOK, nil
			}
			absTarget := filepath.Join(baseTmp, sub)
			relLex, errLex := filepath.Rel(baseTmp, absTarget)
			if errLex != nil || strings.HasPrefix(relLex, "..") {
				return "", "", http.StatusForbidden, errors.New("access denied: path escapes temporary directory boundary")
			}
			evalTarget, err := filepath.EvalSymlinks(absTarget)
			if err != nil {
				if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
					return "", "", http.StatusNotFound, fmt.Errorf("file not found: %s", reqPath)
				}
				return "", "", http.StatusBadRequest, fmt.Errorf("failed to evaluate path: %w", err)
			}
			rel, err := filepath.Rel(evalTmpBase, evalTarget)
			if err != nil || strings.HasPrefix(rel, "..") {
				return "", "", http.StatusForbidden, errors.New("access denied: path escapes temporary directory boundary")
			}
			relClean := filepath.ToSlash(rel)
			if relClean == "." {
				relClean = ""
			}
			if evalRunDir != evalTmpBase {
				relClean = filepath.ToSlash(filepath.Join("/tmp", relClean))
			}
			return evalTarget, relClean, http.StatusOK, nil
		}

		resolveAsWs := func() (string, string, int, error) {
			absTarget := filepath.Join(cleanRunDir, cleanReq)
			relLex, errLex := filepath.Rel(cleanRunDir, absTarget)
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

		if scope == "tmp" {
			return resolveAsTmp()
		}
		if scope == "workspace" {
			return resolveAsWs()
		}

		// Auto disambiguation
		isRunDirSessionTmp := (cleanRunDir == baseTmp || cleanRunDir == evalTmpBase)
		if isRunDirSessionTmp {
			return resolveAsTmp()
		}

		// runDir is project workspace: check workspace existence first
		wsTarget := filepath.Join(cleanRunDir, cleanReq)
		if _, err := os.Stat(wsTarget); err == nil {
			return resolveAsWs()
		}

		// wsTarget does not exist, check if session tmp exists
		tmpTarget := filepath.Join(baseTmp, sub)
		if _, err := os.Stat(tmpTarget); err == nil {
			return resolveAsTmp()
		}

		// Neither exists: fall back to workspace target so os.Stat returns standard 404
		return resolveAsWs()
	}

	// Case 3: Ordinary workspace relative or absolute path (scope ignored)
	cleanReqTrimmed := filepath.Clean(strings.TrimPrefix(reqPath, "/"))
	if cleanReqTrimmed == "." || reqPath == "" {
		return evalRunDir, "", http.StatusOK, nil
	}

	absTarget := filepath.Join(evalRunDir, cleanReqTrimmed)

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
	scope := r.URL.Query().Get("scope")
	targetDir, relPath, code, err := s.validateAndResolvePath(sessionID, runDir, subPath, scope)
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
	scope := r.URL.Query().Get("scope")

	if sessionID == "" || filePath == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id and path are required parameters")
		return
	}

	runDir, code, err := s.resolveSessionWorkspace(sessionID)
	if err != nil {
		writeJSONError(w, code, err.Error())
		return
	}

	absTarget, relPath, code, err := s.validateAndResolvePath(sessionID, runDir, filePath, scope)
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
		serveRawMedia(w, r, absTarget, ext, name, info.ModTime())
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
