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
				runDir = sess.RunDir
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
		writeJSONError(w, http.StatusBadRequest, err.Error())
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

	if info.Size() > maxReadFileSize {
		writeJSONError(w, http.StatusBadRequest, "file size exceeds maximum allowed limit (5MB)")
		return
	}

	contentBytes, err := os.ReadFile(absPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read file content: "+err.Error())
		return
	}

	ext := strings.TrimPrefix(filepath.Ext(absPath), ".")
	name := filepath.Base(absPath)

	resp := WorkspaceFileResponse{
		Path:      reqPath,
		Name:      name,
		Ext:       ext,
		Size:      info.Size(),
		Content:   string(contentBytes),
		UpdatedAt: info.ModTime(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func resolveAndValidatePath(runDir, reqPath string, allowedFiles []string, sessionID string) (string, error) {
	cleanReq := filepath.Clean(reqPath)

	var targetAbs string
	// Check for /tmp paths (remap or direct tmp access)
	if strings.HasPrefix(cleanReq, "/tmp/") || cleanReq == "/tmp" || strings.HasPrefix(cleanReq, ".tmp/") {
		trimmed := strings.TrimPrefix(cleanReq, ".tmp/")
		if strings.HasPrefix(cleanReq, "/tmp/") {
			trimmed = strings.TrimPrefix(cleanReq, "/tmp/")
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("access denied: failed to determine user home directory")
		}

		if sessionID == "" {
			sessionID = "default"
		}
		targetAbs = filepath.Join(home, "tmp", sessionID, trimmed)

		// Ensure it stays inside the session's temporary directory
		sessionTmpDir := filepath.Join(home, "tmp", sessionID)
		rel, err := filepath.Rel(sessionTmpDir, targetAbs)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", errors.New("access denied: path escapes temporary directory boundary")
		}

		// Validation check against session modified files
		authorized := false
		normalizeTmpPath := func(p string) string {
			p = filepath.Clean(p)
			p = strings.TrimPrefix(p, ".tmp/")
			p = strings.TrimPrefix(p, "tmp/") // in case clean stripped leading slash
			p = strings.TrimPrefix(p, "/tmp/")
			return "/tmp/" + p
		}

		normReq := normalizeTmpPath(cleanReq)
		for _, allowed := range allowedFiles {
			if normalizeTmpPath(allowed) == normReq {
				authorized = true
				break
			}
		}
		if !authorized {
			return "", errors.New("access denied: file not authorized in session")
		}
		return targetAbs, nil
	}

	// Normal workspace relative or absolute path
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

func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
