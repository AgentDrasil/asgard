package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaskSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty secret",
			input:    "",
			expected: "",
		},
		{
			name:     "short secret <= 8 chars",
			input:    "12345678",
			expected: "******",
		},
		{
			name:     "short secret very small",
			input:    "abc",
			expected: "******",
		},
		{
			name:     "longer secret > 8 chars",
			input:    "sk-proj-1234567890abcdef",
			expected: "sk-p****cdef",
		},
		{
			name:     "bearer prefix with long secret",
			input:    "Bearer sk-proj-1234567890abcdef",
			expected: "Bearer sk-p****cdef",
		},
		{
			name:     "lowercase bearer prefix with short secret",
			input:    "bearer 12345",
			expected: "bearer ******",
		},
		{
			name:     "mixed case BEARER prefix",
			input:    "BEARER token-abcdefghijk",
			expected: "BEARER toke****hijk",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := MaskSecret(tt.input)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestSafeDumpAndRestoreBody(t *testing.T) {
	t.Parallel()

	t.Run("nil request or body", func(t *testing.T) {
		t.Parallel()
		s, err := safeDumpAndRestoreBody(nil, 100)
		require.NoError(t, err)
		assert.Empty(t, s)

		req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
		require.NoError(t, err)
		s, err = safeDumpAndRestoreBody(req, 100)
		require.NoError(t, err)
		assert.Empty(t, s)
	})

	t.Run("small body within maxBytes", func(t *testing.T) {
		t.Parallel()
		payload := "hello world proxy"
		req, err := http.NewRequest(http.MethodPost, "http://example.com", io.NopCloser(bytes.NewBufferString(payload)))
		require.NoError(t, err)

		dump, err := safeDumpAndRestoreBody(req, 100)
		require.NoError(t, err)
		assert.Equal(t, payload, dump)

		// Verify body is restored and readable
		restorerBytes, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Equal(t, payload, string(restorerBytes))
	})

	t.Run("large body exceeding maxBytes gets truncated in dump but restored fully", func(t *testing.T) {
		t.Parallel()
		prefix := strings.Repeat("a", 50)
		suffix := strings.Repeat("b", 50)
		fullPayload := prefix + suffix // 100 bytes
		req, err := http.NewRequest(http.MethodPost, "http://example.com", io.NopCloser(bytes.NewBufferString(fullPayload)))
		require.NoError(t, err)

		dump, err := safeDumpAndRestoreBody(req, 40)
		require.NoError(t, err)

		assert.Contains(t, dump, "...[TRUNCATED 60 bytes of total 100 bytes]...")
		assert.True(t, strings.HasPrefix(dump, strings.Repeat("a", 40)))

		// Full body must be preserved without truncation
		restoredBytes, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Equal(t, fullPayload, string(restoredBytes))
	})
}
