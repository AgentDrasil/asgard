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

	absPath, err := resolveAndValidatePath(runDir, reqPath, sessionModifiedFiles, sessionID)
	if err != nil {
		if strings.HasPrefix(err.Error(), "access denied") {
			writeJSONError(w, http.StatusForbidden, err.Error())
		} else {
			writeJSONError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	info, err := os.Stat(absPath)
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

	ext := strings.TrimPrefix(filepath.Ext(absPath), ".")
	name := filepath.Base(absPath)

	rawParam := r.URL.Query().Get("raw")
	isRaw := rawParam == "1" || strings.EqualFold(rawParam, "true")

	if isRaw {
		serveRawMedia(w, r, absPath, ext, name, info.ModTime())
		return
	}

	// Non-raw mode: check if media or binary without full memory buffer
	isBinary, err := isBinaryOrMediaFile(absPath, ext)
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

		contentBytes, err := os.ReadFile(absPath)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read file content: "+err.Error())
			return
		}
		contentStr = string(contentBytes)
	}

	resp := WorkspaceFileResponse{
		Path:      reqPath,
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

func resolveAndValidatePath(runDir, reqPath string, allowedFiles []string, sessionID string) (string, error) {
	if !isPathAuthorized(reqPath, allowedFiles, runDir, sessionID) {
		return "", errors.New("access denied: file not authorized in session")
	}

	cleanReq := filepath.Clean(reqPath)

	if isTmp, sub := ResolveSessionTmpPath(cleanReq, sessionID); isTmp {
		baseTmp := GetSessionTmpBaseDir(sessionID)
		targetAbs := filepath.Join(baseTmp, sub)

		// Ensure it stays inside the session's temporary directory
		rel, err := filepath.Rel(baseTmp, targetAbs)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", errors.New("access denied: path escapes temporary directory boundary")
		}
		return targetAbs, nil
	}

	// Normal workspace relative or absolute path
	var targetAbs string
	if filepath.IsAbs(cleanReq) {
		targetAbs = cleanReq
	} else {
		targetAbs = filepath.Join(runDir, cleanReq)
	}

	cleanRunDir := filepath.Clean(runDir)
	rel, err := filepath.Rel(cleanRunDir, targetAbs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("access denied: path escapes workspace boundary")
	}

	return targetAbs, nil
}

func isPathAuthorized(reqPath string, allowedFiles []string, runDir, sessionID string) bool {
	cleanReq := filepath.Clean(reqPath)
	cleanRunDir := ""
	if runDir != "" {
		cleanRunDir = filepath.Clean(runDir)
	}

	isTmpReq, normReqTmp := NormalizeTmpPathForAuth(cleanReq, sessionID)

	for _, allowed := range allowedFiles {
		if allowed == "" {
			continue
		}
		cleanAllowed := filepath.Clean(allowed)
		if isTmpReq {
			if isAllowedTmp, normAllowedTmp := NormalizeTmpPathForAuth(cleanAllowed, sessionID); isAllowedTmp {
				if normReqTmp == normAllowedTmp {
					return true
				}
			}
			continue
		}

		if cleanReq == cleanAllowed {
			return true
		}

		var absReq, absAllowed string
		if filepath.IsAbs(cleanReq) {
			absReq = cleanReq
		} else if cleanRunDir != "" {
			absReq = filepath.Clean(filepath.Join(cleanRunDir, cleanReq))
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
