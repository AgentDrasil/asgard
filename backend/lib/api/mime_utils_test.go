package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectMimeType_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ext      string
		sample   []byte
		expected string
	}{
		// Images
		{"png with dot", ".png", nil, "image/png"},
		{"png without dot", "png", nil, "image/png"},
		{"jpg", "jpg", nil, "image/jpeg"},
		{"jpeg uppercase", "JPEG", nil, "image/jpeg"},
		{"gif", "gif", nil, "image/gif"},
		{"webp", "webp", nil, "image/webp"},
		{"svg", "svg", nil, "image/svg+xml"},
		{"ico", "ico", nil, "image/x-icon"},
		{"bmp", "bmp", nil, "image/bmp"},
		{"avif", "avif", nil, "image/avif"},

		// Videos
		{"mp4", "mp4", nil, "video/mp4"},
		{"webm", "webm", nil, "video/webm"},
		{"ogv", "ogv", nil, "video/ogv"},
		{"mov", "mov", nil, "video/quicktime"},

		// Audio
		{"ogg", "ogg", nil, "audio/ogg"},
		{"mp3", "mp3", nil, "audio/mpeg"},
		{"wav", "wav", nil, "audio/wav"},
		{"oga", "oga", nil, "audio/ogg"},
		{"aac", "aac", nil, "audio/aac"},
		{"m4a", "m4a", nil, "audio/mp4"},
		{"flac", "flac", nil, "audio/flac"},

		// Documents
		{"pdf", "pdf", nil, "application/pdf"},

		// Unknown fallback to sample detection
		{"unknown txt sample", "unknown", []byte("hello world text"), "text/plain; charset=utf-8"},
		{"unknown default fallback", "xyz123", nil, "application/octet-stream"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := detectMimeType(tt.ext, tt.sample)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestIsMediaExt_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ext      string
		expected bool
	}{
		// Media true
		{"png", "png", true},
		{"jpg", ".jpg", true},
		{"mp4", "mp4", true},
		{"mp3", ".MP3", true},
		{"pdf", "pdf", true},
		{"svg", ".svg", true},

		// Non-media false
		{"html", "html", false},
		{"go", ".go", false},
		{"sh", "sh", false},
		{"json", ".json", false},
		{"key", "key", false},
		{"env", ".env", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isMediaExt(tt.ext)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestIsBinaryOrMediaFile_TableDriven(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	mediaFile := filepath.Join(tempDir, "test.png")
	require.NoError(t, os.WriteFile(mediaFile, []byte("fake png data"), 0644))

	textFile := filepath.Join(tempDir, "test.txt")
	require.NoError(t, os.WriteFile(textFile, []byte("hello text world"), 0644))

	binFile := filepath.Join(tempDir, "test.dat")
	require.NoError(t, os.WriteFile(binFile, []byte{0x00, 0x01, 0x02, 0xFF}, 0644))

	tests := []struct {
		name     string
		filePath string
		ext      string
		expected bool
	}{
		{"media extension returns true without reading data", mediaFile, "png", true},
		{"plain text returns false", textFile, "txt", false},
		{"binary file with null byte returns true", binFile, "dat", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := isBinaryOrMediaFile(tt.filePath, tt.ext)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}
