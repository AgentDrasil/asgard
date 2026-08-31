package api

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
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
	"ogg":  "video/ogg",
	"ogv":  "video/ogg",
	"mov":  "video/quicktime",

	// Audio
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
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
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
