package api

import (
	"io"
	"net/http"
	"time"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
)

func isMediaExt(ext string) bool { return runnerfs.IsMediaExt(ext) }

func detectMimeType(ext string, sample []byte) string { return runnerfs.DetectMimeType(ext, sample) }

func isBinaryOrMediaFile(filePath string, ext string) (bool, error) {
	return runnerfs.IsBinaryOrMediaFile(filePath, ext)
}

// serveRawMedia handles raw media streaming with whitelisting and security headers.
func serveRawMedia(w http.ResponseWriter, r *http.Request, content io.ReadSeeker, ext string, name string, modTime time.Time) {
	if !isMediaExt(ext) {
		writeJSONError(w, http.StatusForbidden, "access denied: streaming is only permitted for media files")
		return
	}

	mimeType := detectMimeType(ext, nil)
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")

	http.ServeContent(w, r, name, modTime, content)
}
