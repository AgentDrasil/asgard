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
		// Whitelist check: only allow media/PDF files
		if !isMediaExt(ext) {
			writeJSONError(w, http.StatusForbidden, "access denied: streaming is only permitted for media files")
			return
		}

		file, err := os.Open(absPath)
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
	if !isPathAuthorized(reqPath, allowedFiles, runDir) {
		return "", errors.New("access denied: file not authorized in session")
	}

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

func isPathAuthorized(reqPath string, allowedFiles []string, runDir string) bool {
	cleanReq := filepath.Clean(reqPath)
	cleanRunDir := ""
	if runDir != "" {
		cleanRunDir = filepath.Clean(runDir)
	}

	normalizeTmpPath := func(p string) string {
		p = filepath.Clean(p)
		p = strings.TrimPrefix(p, ".tmp/")
		p = strings.TrimPrefix(p, "tmp/")
		p = strings.TrimPrefix(p, "/tmp/")
		return "/tmp/" + p
	}

	isTmpReq := strings.HasPrefix(cleanReq, "/tmp/") || cleanReq == "/tmp" || strings.HasPrefix(cleanReq, ".tmp/")

	for _, allowed := range allowedFiles {
		if allowed == "" {
			continue
		}
		cleanAllowed := filepath.Clean(allowed)
		if isTmpReq {
			if strings.HasPrefix(cleanAllowed, "/tmp/") || cleanAllowed == "/tmp" || strings.HasPrefix(cleanAllowed, ".tmp/") || strings.HasPrefix(cleanAllowed, "tmp/") {
				if normalizeTmpPath(cleanReq) == normalizeTmpPath(cleanAllowed) {
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
