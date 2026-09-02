package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const maxReadFileSize = 5 * 1024 * 1024 // 5MB

type WorkspaceFileResponse struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Ext       string    `json:"ext"`
	Size      int64     `json:"size"`
	Content   string    `json:"content"`
	IsBinary  bool      `json:"isBinary,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// handleWorkspaceFile handles GET /api/v1/workspace/file?session_id=xxx&path=yyy
func (s *Server) handleWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	reqPath := r.URL.Query().Get("path")

	if sessionID == "" || reqPath == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id and path are required parameters")
		return
	}

	if !IsValidChatID(sessionID) {
		writeJSONError(w, http.StatusBadRequest, "invalid session_id format")
		return
	}

	var runDir string
	var sessionModifiedFiles []string

	if s.repo != nil {
		sess, err := s.repo.GetSession(sessionID)
		if err != nil {
			log.Error().Err(err).Str("session_id", sessionID).Msg("Failed to retrieve session for file read")
			writeJSONError(w, http.StatusInternalServerError, "failed to query session")
			return
		}
		if sess != nil {
			if sess.RunDir != "" {
				runDir = NormalizeSessionRunDir(sess.RunDir, sessionID)
			}
			sessionModifiedFiles = sess.Artifacts
		}
	}

	if runDir == "" {
		// Fallback to current working directory if session runDir is empty
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to determine workspace directory")
			return
		}
	}

	scope := r.URL.Query().Get("scope")

	targetAbs, ns, authPath, err := resolveAndValidatePath(runDir, reqPath, scope, sessionID)
	if err != nil {
		if strings.HasPrefix(err.Error(), "access denied") {
			writeJSONError(w, http.StatusForbidden, err.Error())
		} else {
			writeJSONError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	if !isPathAuthorized(authPath, sessionModifiedFiles, runDir, sessionID, ns) {
		writeJSONError(w, http.StatusForbidden, "access denied: file not authorized in session")
		return
	}

	info, err := os.Stat(targetAbs)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSONError(w, http.StatusNotFound, "file not found: "+reqPath)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to stat file: "+err.Error())
		return
	}

	if info.IsDir() {
		writeJSONError(w, http.StatusBadRequest, "requested path is a directory, not a file")
		return
	}

	ext := strings.TrimPrefix(filepath.Ext(targetAbs), ".")
	name := filepath.Base(targetAbs)

	rawParam := r.URL.Query().Get("raw")
	isRaw := rawParam == "1" || strings.EqualFold(rawParam, "true")

	if isRaw {
		serveRawMedia(w, r, targetAbs, ext, name, info.ModTime())
		return
	}

	// Non-raw mode: check if media or binary without full memory buffer
	isBinary, err := isBinaryOrMediaFile(targetAbs, ext)
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

		contentBytes, err := os.ReadFile(targetAbs)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read file content: "+err.Error())
			return
		}
		contentStr = string(contentBytes)
	}

	displayPath := reqPath
	if ns != "" {
		displayPath = authPath
	}

	resp := WorkspaceFileResponse{
		Path:      displayPath,
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

// resolveAndValidateNamespacedPath handles workspace/file paths targeting a session
// namespace ("tmp" or "session"): explicit forms (/<ns>, .<ns>, /<ns>/...) and relative
// forms (<ns>, <ns>/...). It returns handled=false when cleanReq does not target the namespace.
func resolveAndValidateNamespacedPath(ns string, runDir, reqPath, scope, sessionID string) (bool, string, string, string, error) {
	cleanReq := filepath.Clean(reqPath)
	cleanRunDir := filepath.Clean(runDir)
	base := GetSessionScopedBaseDir(ns, sessionID)

	evalBase := base
	if eb, evalErr := filepath.EvalSymlinks(base); evalErr == nil {
		evalBase = eb
	}

	evalRunDir := cleanRunDir
	if er, evalErr := filepath.EvalSymlinks(cleanRunDir); evalErr == nil {
		evalRunDir = er
	}

	nsPrefix := "/" + ns

	// Explicit namespace path (/<ns>, .<ns>, /<ns>/...)
	if isExplicit, sub := ResolveSessionScopedPath(cleanReq, sessionID, ns); isExplicit {
		sub = filepath.Clean(sub)
		if sub == "." {
			sub = ""
		}
		target := filepath.Join(base, sub)
		relLex, errLex := filepath.Rel(base, target)
		if errLex != nil || strings.HasPrefix(relLex, "..") {
			return true, "", "", "", errors.New("access denied: path escapes temporary directory boundary")
		}

		evalTarget, errEval := filepath.EvalSymlinks(target)
		if errEval == nil {
			relEval, errRel := filepath.Rel(evalBase, evalTarget)
			if errRel != nil || strings.HasPrefix(relEval, "..") {
				return true, "", "", "", errors.New("access denied: path escapes temporary directory boundary")
			}
		}

		canonicalAuth := nsPrefix
		if sub != "" {
			canonicalAuth = nsPrefix + "/" + sub
		}
		return true, target, ns, canonicalAuth, nil
	}

	// Relative namespace path (<ns>, <ns>/...)
	if isRelNs, sub := isRelativeScopedPrefixedPath(cleanReq, sessionID, ns); isRelNs {
		sub = filepath.Clean(sub)
		if sub == "." {
			sub = ""
		}
		canonicalAuth := nsPrefix
		if sub != "" {
			canonicalAuth = nsPrefix + "/" + sub
		}

		resolveAsNs := func() (string, string, string, error) {
			target := filepath.Join(base, sub)
			relLex, errLex := filepath.Rel(base, target)
			if errLex != nil || strings.HasPrefix(relLex, "..") {
				return "", "", "", errors.New("access denied: path escapes temporary directory boundary")
			}
			evalTarget, errEval := filepath.EvalSymlinks(target)
			if errEval == nil {
				relEval, errRel := filepath.Rel(evalBase, evalTarget)
				if errRel != nil || strings.HasPrefix(relEval, "..") {
					return "", "", "", errors.New("access denied: path escapes temporary directory boundary")
				}
			}
			return target, ns, canonicalAuth, nil
		}

		resolveAsWs := func() (string, string, string, error) {
			target := filepath.Join(cleanRunDir, cleanReq)
			relLex, errLex := filepath.Rel(cleanRunDir, target)
			if errLex != nil || strings.HasPrefix(relLex, "..") {
				return "", "", "", errors.New("access denied: path escapes workspace boundary")
			}
			evalTarget, errEval := filepath.EvalSymlinks(target)
			if errEval == nil {
				relEval, errRel := filepath.Rel(evalRunDir, evalTarget)
				if errRel != nil || strings.HasPrefix(relEval, "..") {
					return "", "", "", errors.New("access denied: path escapes workspace boundary")
				}
			}
			return target, "", cleanReq, nil
		}

		if scope == ns {
			target, resolvedNs, auth, err := resolveAsNs()
			return true, target, resolvedNs, auth, err
		}

		if scope == "workspace" {
			target, resolvedNs, auth, err := resolveAsWs()
			return true, target, resolvedNs, auth, err
		}

		// scope unprovided or invalid -> auto disambiguation
		isRunDirNsBase := (cleanRunDir == base || cleanRunDir == evalBase || evalRunDir == evalBase)
		if isRunDirNsBase {
			target, resolvedNs, auth, err := resolveAsNs()
			return true, target, resolvedNs, auth, err
		}

		// runDir is project workspace
		wsTarget := filepath.Join(cleanRunDir, cleanReq)
		if _, err := os.Stat(wsTarget); err == nil {
			relLex, errLex := filepath.Rel(cleanRunDir, wsTarget)
			if errLex != nil || strings.HasPrefix(relLex, "..") {
				return true, "", "", "", errors.New("access denied: path escapes workspace boundary")
			}
			evalTarget, errEval := filepath.EvalSymlinks(wsTarget)
			if errEval == nil {
				relEval, errRel := filepath.Rel(evalRunDir, evalTarget)
				if errRel != nil || strings.HasPrefix(relEval, "..") {
					return true, "", "", "", errors.New("access denied: path escapes workspace boundary")
				}
			}
			return true, wsTarget, "", cleanReq, nil
		}

		// wsTarget does not exist, check if session namespace target exists
		nsTarget := filepath.Join(base, sub)
		if _, err := os.Stat(nsTarget); err == nil {
			relLex, errLex := filepath.Rel(base, nsTarget)
			if errLex != nil || strings.HasPrefix(relLex, "..") {
				return true, "", "", "", errors.New("access denied: path escapes temporary directory boundary")
			}
			evalTarget, errEval := filepath.EvalSymlinks(nsTarget)
			if errEval == nil {
				relEval, errRel := filepath.Rel(evalBase, evalTarget)
				if errRel != nil || strings.HasPrefix(relEval, "..") {
					return true, "", "", "", errors.New("access denied: path escapes temporary directory boundary")
				}
			}
			return true, nsTarget, ns, canonicalAuth, nil
		}

		// Neither exists: fall back to workspace target so os.Stat returns standard 404
		relLex, errLex := filepath.Rel(cleanRunDir, wsTarget)
		if errLex != nil || strings.HasPrefix(relLex, "..") {
			return true, "", "", "", errors.New("access denied: path escapes workspace boundary")
		}
		return true, wsTarget, "", cleanReq, nil
	}

	return false, "", "", "", nil
}

func resolveAndValidatePath(runDir, reqPath, scope, sessionID string) (targetAbs string, ns string, authPath string, err error) {
	// Namespaced paths (/tmp, /session and their relative forms)
	for _, namespace := range []string{"tmp", "session"} {
		handled, target, resolvedNs, auth, err := resolveAndValidateNamespacedPath(namespace, runDir, reqPath, scope, sessionID)
		if handled {
			return target, resolvedNs, auth, err
		}
	}

	// Case 3: Ordinary workspace relative or absolute path (scope ignored)
	cleanReq := filepath.Clean(reqPath)
	cleanRunDir := filepath.Clean(runDir)

	evalRunDir := cleanRunDir
	if er, evalErr := filepath.EvalSymlinks(cleanRunDir); evalErr == nil {
		evalRunDir = er
	}

	var targetAbsPath string
	if filepath.IsAbs(cleanReq) {
		targetAbsPath = cleanReq
	} else {
		targetAbsPath = filepath.Join(cleanRunDir, cleanReq)
	}

	relLex, errLex := filepath.Rel(cleanRunDir, targetAbsPath)
	if errLex != nil || strings.HasPrefix(relLex, "..") {
		return "", "", "", errors.New("access denied: path escapes workspace boundary")
	}
	evalTarget, errEval := filepath.EvalSymlinks(targetAbsPath)
	if errEval == nil {
		relEval, errRel := filepath.Rel(evalRunDir, evalTarget)
		if errRel != nil || strings.HasPrefix(relEval, "..") {
			return "", "", "", errors.New("access denied: path escapes workspace boundary")
		}
	}

	return targetAbsPath, "", cleanReq, nil
}

func isPathAuthorized(authPath string, allowedFiles []string, runDir, sessionID string, ns string) bool {
	cleanAuth := filepath.Clean(authPath)
	cleanRunDir := ""
	if runDir != "" {
		cleanRunDir = filepath.Clean(runDir)
	}

	for _, allowed := range allowedFiles {
		if allowed == "" {
			continue
		}
		cleanAllowed := filepath.Clean(allowed)

		if ns != "" {
			// Check if cleanAllowed is explicit namespace path
			if isAllowedNs, normAllowedNs := NormalizeScopedPathForAuth(cleanAllowed, sessionID, ns); isAllowedNs {
				if cleanAuth == normAllowedNs {
					return true
				}
			}
			// Md2: Check legacy relative namespace entry in allowedFiles
			if isRelNs, sub := isRelativeScopedPrefixedPath(cleanAllowed, sessionID, ns); isRelNs {
				normRelNs := "/" + ns
				if sub != "" && sub != "." {
					normRelNs = "/" + ns + "/" + sub
				}
				if cleanAuth == normRelNs {
					return true
				}
			}
			continue
		}

		// Non-tmp path cannot be authorized by legacy tmp entries
		if cleanAuth == cleanAllowed {
			return true
		}

		var absReq, absAllowed string
		if filepath.IsAbs(cleanAuth) {
			absReq = cleanAuth
		} else if cleanRunDir != "" {
			absReq = filepath.Clean(filepath.Join(cleanRunDir, cleanAuth))
		}

		if filepath.IsAbs(cleanAllowed) {
			absAllowed = cleanAllowed
		} else if cleanRunDir != "" {
			absAllowed = filepath.Clean(filepath.Join(cleanRunDir, cleanAllowed))
		}

		if absReq != "" && absAllowed != "" && absReq == absAllowed {
			return true
		}
	}
	return false
}

func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
