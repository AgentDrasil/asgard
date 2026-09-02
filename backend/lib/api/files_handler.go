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
	Path  string `json:"path"` // relative to session runDir, or prefixed with /tmp for session tmp
	Name  string `json:"name"`
	Ext   string `json:"ext"`
	Size  int64  `json:"size"`
	Scope string `json:"scope,omitempty"` // "workspace" | "tmp" | "session"
}

type FileSearchResponse struct {
	Files []FileSearchResult `json:"files"`
}

func searchDirectory(rootDir string, pathPrefix string, scope string, queryLower string, maxResults int, visitedCanonical map[string]bool) []FileSearchResult {
	var results []FileSearchResult
	if maxResults <= 0 {
		return results
	}

	evalRootDir, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		evalRootDir = rootDir
	}

	_ = filepath.WalkDir(rootDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			if p == rootDir {
				return nil
			}
		}

		var evalP string
		var isSymlink bool
		if d.Type()&os.ModeSymlink != 0 {
			isSymlink = true
			info, statErr := os.Stat(p)
			if statErr != nil {
				// Broken symlink: silently ignore
				return nil
			}
			if info.IsDir() {
				// WalkDir never descends into symlinks; SkipDir here would skip remaining siblings.
				return nil
			}

			// Resolve symlink target
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil {
				return nil
			}
			evalP = resolved
		} else if d.IsDir() {
			return nil
		} else {
			// Non-symlink file under evalRootDir: construct canonical path directly
			relToRoot, err := filepath.Rel(rootDir, p)
			if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
				return nil
			}
			evalP = filepath.Join(evalRootDir, relToRoot)
		}

		// Out-of-bounds escape check: evalP must be inside evalRootDir
		relToEvalRoot, err := filepath.Rel(evalRootDir, evalP)
		if err != nil || relToEvalRoot == ".." || strings.HasPrefix(relToEvalRoot, ".."+string(filepath.Separator)) {
			return nil
		}

		// Deduplicate physical paths using visitedCanonical
		if visitedCanonical[evalP] {
			return nil
		}
		visitedCanonical[evalP] = true

		rel, relErr := filepath.Rel(rootDir, p)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}

		var fullRelPath string
		relSlash := filepath.ToSlash(rel)
		if pathPrefix != "" {
			fullRelPath = filepath.ToSlash(filepath.Join(pathPrefix, relSlash))
		} else {
			fullRelPath = relSlash
		}

		if queryLower == "" || strings.Contains(strings.ToLower(fullRelPath), queryLower) {
			var size int64
			if isSymlink {
				if statInfo, statErr := os.Stat(p); statErr == nil {
					size = statInfo.Size()
				}
			} else if info, infoErr := d.Info(); infoErr == nil {
				size = info.Size()
			} else if statInfo, statErr := os.Stat(p); statErr == nil {
				size = statInfo.Size()
			}

			results = append(results, FileSearchResult{
				Path:  fullRelPath,
				Name:  name,
				Ext:   strings.TrimPrefix(filepath.Ext(name), "."),
				Size:  size,
				Scope: scope,
			})

			if len(results) >= maxResults {
				return filepath.SkipAll
			}
		}

		return nil
	})

	return results
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

func isPathEscaping(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveNamespacedScopedPath handles paths targeting a session namespace ("tmp" or "session"):
// explicit forms (/<ns>, .<ns>, /<ns>/...) and relative forms (<ns>, <ns>/...).
// It returns handled=false when cleanReq does not target the namespace.
func resolveNamespacedScopedPath(ns string, sessionID, cleanRunDir, evalRunDir, cleanReq, scope string) (bool, string, string, int, error) {
	base := GetSessionScopedBaseDir(ns, sessionID)
	evalBase := base
	if eb, evalErr := filepath.EvalSymlinks(base); evalErr == nil {
		evalBase = eb
	}

	// Explicit namespace path (/<ns>, .<ns>, /<ns>/...)
	if isNs, sub := ResolveSessionScopedPath(cleanReq, sessionID, ns); isNs {
		sub = filepath.Clean(sub)
		if sub == "." || sub == "" {
			relResult := ""
			if evalRunDir != evalBase {
				relResult = "/" + ns
			}
			return true, evalBase, relResult, http.StatusOK, nil
		}

		absTarget := filepath.Join(base, sub)
		relLex, errLex := filepath.Rel(base, absTarget)
		if errLex != nil || isPathEscaping(relLex) {
			return true, "", "", http.StatusForbidden, errors.New("access denied: path escapes temporary directory boundary")
		}

		evalTarget, err := filepath.EvalSymlinks(absTarget)
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
				return true, "", "", http.StatusNotFound, fmt.Errorf("file not found: %s", cleanReq)
			}
			return true, "", "", http.StatusBadRequest, fmt.Errorf("failed to evaluate path: %w", err)
		}

		rel, err := filepath.Rel(evalBase, evalTarget)
		if err != nil || isPathEscaping(rel) {
			return true, "", "", http.StatusForbidden, errors.New("access denied: path escapes temporary directory boundary")
		}

		relClean := filepath.ToSlash(rel)
		if relClean == "." {
			relClean = ""
		}
		if evalRunDir != evalBase {
			relClean = filepath.ToSlash(filepath.Join("/"+ns, relClean))
		}
		return true, evalTarget, relClean, http.StatusOK, nil
	}

	// Relative namespace path (<ns>, <ns>/...)
	if isRelNs, sub := isRelativeScopedPrefixedPath(cleanReq, sessionID, ns); isRelNs {
		sub = filepath.Clean(sub)
		if sub == "." {
			sub = ""
		}

		resolveAsNs := func() (string, string, int, error) {
			if sub == "" {
				relResult := ""
				if evalRunDir != evalBase {
					relResult = "/" + ns
				}
				return evalBase, relResult, http.StatusOK, nil
			}
			absTarget := filepath.Join(base, sub)
			relLex, errLex := filepath.Rel(base, absTarget)
			if errLex != nil || isPathEscaping(relLex) {
				return "", "", http.StatusForbidden, errors.New("access denied: path escapes temporary directory boundary")
			}
			evalTarget, err := filepath.EvalSymlinks(absTarget)
			if err != nil {
				if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
					return "", "", http.StatusNotFound, fmt.Errorf("file not found: %s", cleanReq)
				}
				return "", "", http.StatusBadRequest, fmt.Errorf("failed to evaluate path: %w", err)
			}
			rel, err := filepath.Rel(evalBase, evalTarget)
			if err != nil || isPathEscaping(rel) {
				return "", "", http.StatusForbidden, errors.New("access denied: path escapes temporary directory boundary")
			}
			relClean := filepath.ToSlash(rel)
			if relClean == "." {
				relClean = ""
			}
			if evalRunDir != evalBase {
				relClean = filepath.ToSlash(filepath.Join("/"+ns, relClean))
			}
			return evalTarget, relClean, http.StatusOK, nil
		}

		resolveAsWs := func() (string, string, int, error) {
			absTarget := filepath.Join(cleanRunDir, cleanReq)
			relLex, errLex := filepath.Rel(cleanRunDir, absTarget)
			if errLex != nil || isPathEscaping(relLex) {
				return "", "", http.StatusForbidden, errors.New("access denied: path escapes workspace boundary")
			}
			evalTarget, err := filepath.EvalSymlinks(absTarget)
			if err != nil {
				if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
					return "", "", http.StatusNotFound, fmt.Errorf("file not found: %s", cleanReq)
				}
				return "", "", http.StatusBadRequest, fmt.Errorf("failed to evaluate path: %w", err)
			}
			rel, err := filepath.Rel(evalRunDir, evalTarget)
			if err != nil || isPathEscaping(rel) {
				return "", "", http.StatusForbidden, errors.New("access denied: path escapes workspace boundary")
			}
			relClean := filepath.ToSlash(rel)
			if relClean == "." {
				relClean = ""
			}
			return evalTarget, relClean, http.StatusOK, nil
		}

		if scope == ns {
			target, rel, code, err := resolveAsNs()
			return true, target, rel, code, err
		}
		if scope == "workspace" {
			target, rel, code, err := resolveAsWs()
			return true, target, rel, code, err
		}

		// Auto disambiguation
		isRunDirNsBase := (cleanRunDir == base || cleanRunDir == evalBase || evalRunDir == evalBase)
		if isRunDirNsBase {
			target, rel, code, err := resolveAsNs()
			return true, target, rel, code, err
		}

		// runDir is project workspace: check workspace existence first
		wsTarget := filepath.Join(cleanRunDir, cleanReq)
		if _, err := os.Stat(wsTarget); err == nil {
			target, rel, code, err := resolveAsWs()
			return true, target, rel, code, err
		}

		// wsTarget does not exist, check if session namespace target exists
		nsTarget := filepath.Join(base, sub)
		if _, err := os.Stat(nsTarget); err == nil {
			target, rel, code, err := resolveAsNs()
			return true, target, rel, code, err
		}

		// Neither exists: fall back to workspace target so os.Stat returns standard 404
		target, rel, code, err := resolveAsWs()
		return true, target, rel, code, err
	}

	return false, "", "", http.StatusOK, nil
}

func (s *Server) validateAndResolvePath(sessionID, runDir, reqPath, scope string) (string, string, int, error) {
	cleanRunDir := filepath.Clean(runDir)

	evalRunDir, err := filepath.EvalSymlinks(cleanRunDir)
	if err != nil {
		evalRunDir = cleanRunDir
	}

	cleanReq := filepath.Clean(reqPath)
	if cleanReq == "." || reqPath == "" {
		return evalRunDir, "", http.StatusOK, nil
	}

	// Namespaced paths (/tmp, /session and their relative forms)
	for _, ns := range []string{"tmp", "session"} {
		handled, target, rel, code, err := resolveNamespacedScopedPath(ns, sessionID, cleanRunDir, evalRunDir, cleanReq, scope)
		if handled {
			return target, rel, code, err
		}
	}

	// Case 3: Ordinary workspace relative or absolute path (scope ignored)
	cleanReqTrimmed := filepath.Clean(strings.TrimPrefix(reqPath, "/"))
	if cleanReqTrimmed == "." || reqPath == "" {
		return evalRunDir, "", http.StatusOK, nil
	}

	absTarget := filepath.Join(evalRunDir, cleanReqTrimmed)

	// Pre-check lexical containment before symlink resolution
	relLex, errLex := filepath.Rel(evalRunDir, absTarget)
	if errLex != nil || isPathEscaping(relLex) {
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
	if err != nil || isPathEscaping(rel) {
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
		evalRunDir = cleanRunDir
	}

	baseTmp := GetSessionTmpBaseDir(sessionID)
	evalTmpBase := baseTmp
	if et, evalErr := filepath.EvalSymlinks(baseTmp); evalErr == nil {
		evalTmpBase = et
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

	queryLower := strings.ToLower(query)
	visitedCanonical := make(map[string]bool)

	// Check if runDir is session tmp or has overlap / parent-child containment
	isRunDirSessionTmp := (cleanRunDir == baseTmp || cleanRunDir == evalTmpBase || evalRunDir == evalTmpBase)
	relWsToTmp, errWsToTmp := filepath.Rel(evalTmpBase, evalRunDir)
	relTmpToWs, errTmpToWs := filepath.Rel(evalRunDir, evalTmpBase)
	isOverlapping := isRunDirSessionTmp ||
		(errWsToTmp == nil && relWsToTmp != ".." && !strings.HasPrefix(relWsToTmp, ".."+string(filepath.Separator))) ||
		(errTmpToWs == nil && relTmpToWs != ".." && !strings.HasPrefix(relTmpToWs, ".."+string(filepath.Separator)))

	var results []FileSearchResult

	if isOverlapping {
		// Single scan on workspace to avoid duplicates
		results = searchDirectory(evalRunDir, "", "workspace", queryLower, limit, visitedCanonical)
	} else {
		// Disjoint directories: anti-starvation quota
		wsLimit := limit - (limit / 4)
		wsResults := searchDirectory(evalRunDir, "", "workspace", queryLower, wsLimit, visitedCanonical)
		results = append(results, wsResults...)

		tmpLimit := limit - len(results)
		if tmpLimit > 0 {
			if tmpInfo, statErr := os.Stat(evalTmpBase); statErr == nil && tmpInfo.IsDir() {
				tmpResults := searchDirectory(evalTmpBase, "/tmp", "tmp", queryLower, tmpLimit, visitedCanonical)
				results = append(results, tmpResults...)
			}
		}
	}

	// Session namespace (/session): scan when disjoint from runDir with remaining quota
	baseSession := GetSessionScopedBaseDir("session", sessionID)
	evalSessionBase := baseSession
	if es, evalErr := filepath.EvalSymlinks(baseSession); evalErr == nil {
		evalSessionBase = es
	}
	relWsToSess, errWsToSess := filepath.Rel(evalSessionBase, evalRunDir)
	relSessToWs, errSessToWs := filepath.Rel(evalRunDir, evalSessionBase)
	isSessionOverlapping := (errWsToSess == nil && relWsToSess != ".." && !strings.HasPrefix(relWsToSess, ".."+string(filepath.Separator))) ||
		(errSessToWs == nil && relSessToWs != ".." && !strings.HasPrefix(relSessToWs, ".."+string(filepath.Separator)))
	if !isSessionOverlapping {
		sessLimit := limit - len(results)
		if sessLimit > 0 {
			if sessInfo, statErr := os.Stat(evalSessionBase); statErr == nil && sessInfo.IsDir() {
				sessResults := searchDirectory(evalSessionBase, "/session", "session", queryLower, sessLimit, visitedCanonical)
				results = append(results, sessResults...)
			}
		}
	}

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
