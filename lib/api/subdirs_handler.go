package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// SubdirsResponse represents the response payload for listing subdirectories.
type SubdirsResponse struct {
	Subdirs []string `json:"subdirs"`
}

// handleSubdirs handles GET /api/subdirs?dir={path}.
func (s *Server) handleSubdirs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}

	dirParam := r.URL.Query().Get("dir")
	if dirParam == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "dir parameter is required"})
		return
	}

	cleanDir := filepath.Clean(dirParam)
	info, err := os.Stat(cleanDir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("directory %q does not exist: %v", cleanDir, err)})
		return
	}

	if !info.IsDir() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("%q is not a directory", cleanDir)})
		return
	}

	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		log.Error().Err(err).Str("dir", cleanDir).Msg("Failed to read directory for subdirs endpoint")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("failed to read directory: %v", err)})
		return
	}

	subdirs := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}

		isDir := entry.IsDir()
		if !isDir && entry.Type()&os.ModeSymlink != 0 {
			targetPath := filepath.Join(cleanDir, name)
			if st, err := os.Stat(targetPath); err == nil && st.IsDir() {
				isDir = true
			}
		}

		if isDir {
			subdirs = append(subdirs, name)
		}
	}

	sort.Strings(subdirs)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(SubdirsResponse{Subdirs: subdirs})
}
