package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
)

// SubdirsResponse represents the response payload for listing subdirectories.
type SubdirsResponse struct {
	Subdirs []string `json:"subdirs"`
	GitRoot string   `json:"git_root,omitempty"`
}

// handleSubdirs handles GET /api/subdirs?dir={path}[&runner={id}]: the
// directory is listed on the runner (the named one, or the only one known).
func (s *Server) handleSubdirs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}

	dirParam := r.URL.Query().Get("dir")
	if dirParam == "" {
		writeJSONError(w, http.StatusBadRequest, "dir parameter is required")
		return
	}

	fs, err := s.runnerOnlyFS(r)
	if err != nil {
		writeFSError(w, err)
		return
	}
	res, err := fs.Subdirs(r.Context(), runnerfs.SubdirsRequest{Dir: filepath.Clean(dirParam)})
	if err != nil {
		if runnerfs.CodeOf(err) == runnerfs.CodeInternal {
			log.Error().Err(err).Str("dir", dirParam).Msg("Failed to read directory for subdirs endpoint")
		}
		writeFSError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(SubdirsResponse{Subdirs: res.Subdirs, GitRoot: res.GitRoot})
}
