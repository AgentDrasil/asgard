package api

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
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

	var storedRunDir string
	var sessionModifiedFiles []string

	if s.repo != nil {
		sess, err := s.repo.GetSession(sessionID)
		if err != nil {
			log.Error().Err(err).Str("session_id", sessionID).Msg("Failed to retrieve session for file read")
			writeJSONError(w, http.StatusInternalServerError, "failed to query session")
			return
		}
		if sess != nil {
			storedRunDir = sess.RunDir
			sessionModifiedFiles = sess.Artifacts
		}
	}

	fs, ferr := s.chatFS(sessionID)
	if ferr != nil {
		writeFSError(w, ferr)
		return
	}
	// The runner resolves the stored run dir against its own layout; the
	// backend only needs a path-shaped run dir for the authorization check.
	authRunDir := ""
	if storedRunDir != "" {
		authRunDir = NormalizeSessionRunDir(storedRunDir, sessionID)
	}

	candidates := agents.AuthCandidates(reqPath, r.URL.Query().Get("scope"), sessionID)
	for _, c := range candidates {
		if c.Ns != "" {
			continue
		}
		// Only workspace paths depend on the run dir; an ambiguous
		// "tmp/x" form may still be the namespace, which the runner decides.
		if storedRunDir == "" && len(candidates) > 1 {
			break
		}
		if code, msg := s.checkWorkspaceRunDir(sessionID, storedRunDir); code != 0 {
			writeJSONError(w, code, msg)
			return
		}
		break
	}

	// Authorize lexically before touching the runner so unauthorized paths
	// learn nothing about what exists.
	preAuthorized := false
	for _, c := range candidates {
		if isPathAuthorized(c.Path, sessionModifiedFiles, authRunDir, sessionID, c.Ns) {
			preAuthorized = true
			break
		}
	}
	if !preAuthorized {
		writeJSONError(w, http.StatusForbidden, "access denied: file not authorized in session")
		return
	}

	isRaw := false
	rawParam := r.URL.Query().Get("raw")
	isRaw = rawParam == "1" || strings.EqualFold(rawParam, "true")

	f, err := fs.Open(r.Context(), runnerfs.ReadRequest{
		RunDir: storedRunDir, Path: reqPath, Scope: r.URL.Query().Get("scope"), Raw: isRaw,
	})
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer func() { _ = f.Close() }()

	if !isPathAuthorized(f.Path, sessionModifiedFiles, authRunDir, sessionID, f.Namespace()) {
		writeJSONError(w, http.StatusForbidden, "access denied: file not authorized in session")
		return
	}

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

	resp := WorkspaceFileResponse{
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
