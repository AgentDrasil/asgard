package runnerfs

import (
	"bytes"
	"errors"
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

// IsMediaExt reports whether the file extension (with or without leading dot) corresponds to an allowed media/PDF format.
func IsMediaExt(ext string) bool {
	normalized := strings.ToLower(strings.TrimPrefix(ext, "."))
	_, ok := mediaMimeMap[normalized]
	return ok
}

// DetectMimeType returns the content type for a given extension and optional sample bytes.
func DetectMimeType(ext string, sample []byte) string {
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

// IsBinaryOrMediaFile checks if a file by extension or preview bytes is binary/media.
// It reads at most 512 bytes without loading the whole file.
func IsBinaryOrMediaFile(filePath string, ext string) (bool, error) {
	if IsMediaExt(ext) {
		return true, nil
	}

	f, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	return sniffBinary(f), nil
}

// sniffBinary reports whether the first 512 bytes of r look binary.
func sniffBinary(r io.Reader) bool {
	buf := make([]byte, 512)
	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	if n == 0 {
		return false
	}
	sample := buf[:n]
	if bytes.IndexByte(sample, 0) != -1 {
		return true
	}
	return strings.HasPrefix(http.DetectContentType(sample), "application/octet-stream")
}
