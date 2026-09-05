package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MaskSecret masks sensitive secrets for logging or safe inspection.
// It detects and preserves case-insensitive "Bearer " prefix.
// If the token length after stripping the prefix is <= 8, it is replaced with "******".
// If the token length > 8, it preserves the first 4 and last 4 characters,
// and masks the middle with "****" (e.g. "sk-proj-****1234").
func MaskSecret(s string) string {
	if s == "" {
		return ""
	}

	prefix := ""
	token := s
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "bearer ") {
		prefix = s[:7] // preserve original case of "Bearer "
		token = s[7:]
	}

	n := len(token)
	if n <= 8 {
		return prefix + "******"
	}

	return prefix + token[:4] + "****" + token[n-4:]
}

// safeDumpAndRestoreBody reads the entire request body without loss,
// immediately restores req.Body with io.NopCloser(bytes.NewReader(bodyBytes)),
// and returns a truncated snippet for logging if body length exceeds maxBytes.
func safeDumpAndRestoreBody(req *http.Request, maxBytes int64) (string, error) {
	if req == nil || req.Body == nil {
		return "", nil
	}

	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return "", err
	}

	// Lossless restoration
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	if len(bodyBytes) == 0 {
		return "", nil
	}

	if maxBytes <= 0 {
		maxBytes = 4096
	}

	total := int64(len(bodyBytes))
	if total <= maxBytes {
		return string(bodyBytes), nil
	}

	truncated := bodyBytes[:maxBytes]
	return fmt.Sprintf("%s...[TRUNCATED %d bytes of total %d bytes]...", string(truncated), total-maxBytes, total), nil
}
