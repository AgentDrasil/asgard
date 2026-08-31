package api

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
)

var mediaMimeMap = map[string]string{
	// Images
	"png":  "image/png",
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"gif":  "image/gif",
	"webp": "image/webp",
	"svg":  "image/svg+xml",
	"ico":  "image/x-icon",
	"bmp":  "image/bmp",
	"avif": "image/avif",

	// Videos
	"mp4":  "video/mp4",
	"webm": "video/webm",
	"ogv":  "video/ogv",
	"mov":  "video/quicktime",

	// Audio
	"ogg":  "audio/ogg",
	"mp3":  "audio/mpeg",
	"wav":  "audio/wav",
	"oga":  "audio/ogg",
	"aac":  "audio/aac",
	"m4a":  "audio/mp4",
	"flac": "audio/flac",

	// Documents
	"pdf": "application/pdf",
}

// isMediaExt reports whether the file extension (with or without leading dot) corresponds to an allowed media/PDF format.
func isMediaExt(ext string) bool {
	normalized := strings.ToLower(strings.TrimPrefix(ext, "."))
	_, ok := mediaMimeMap[normalized]
	return ok
}

// detectMimeType returns the content type for a given extension and optional sample bytes.
func detectMimeType(ext string, sample []byte) string {
	normalized := strings.ToLower(strings.TrimPrefix(ext, "."))
	if mimeType, ok := mediaMimeMap[normalized]; ok {
		return mimeType
	}

	if normalized != "" {
		if mimeType := mime.TypeByExtension("." + normalized); mimeType != "" {
			return mimeType
		}
	}

	if len(sample) > 0 {
		return http.DetectContentType(sample)
	}

	return "application/octet-stream"
}

// isBinaryOrMediaFile checks if a file by extension or preview bytes is binary/media.
// It reads at most 512 bytes without loading the whole file.
func isBinaryOrMediaFile(filePath string, ext string) (bool, error) {
	if isMediaExt(ext) {
		return true, nil
	}

	f, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 512)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}

	if n > 0 {
		sample := buf[:n]
		if bytes.IndexByte(sample, 0) != -1 {
			return true, nil
		}
		mimeType := http.DetectContentType(sample)
		if strings.HasPrefix(mimeType, "application/octet-stream") {
			return true, nil
		}
	}

	return false, nil
}

// serveRawMedia handles raw media streaming with whitelisting and security headers.
func serveRawMedia(w http.ResponseWriter, r *http.Request, absPath string, ext string, name string, modTime time.Time) {
	if !isMediaExt(ext) {
		writeJSONError(w, http.StatusForbidden, "access denied: streaming is only permitted for media files")
		return
	}

	file, err := os.Open(absPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to open file")
		return
	}
	defer func() { _ = file.Close() }()

	mimeType := detectMimeType(ext, nil)
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")

	http.ServeContent(w, r, name, modTime, file)
}
