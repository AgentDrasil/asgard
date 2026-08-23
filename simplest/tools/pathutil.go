package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// resolveToCwd resolves a possibly-relative path against cwd, expanding a
// leading "~" to the user's home directory. It does not
// retry macOS-specific filename variants (NFD forms, narrow spaces, curly
// quotes).
func resolveToCwd(filePath, cwd string) string {
	if filePath == "" {
		return cwd
	}
	if filePath == "~" || strings.HasPrefix(filePath, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			filePath = filepath.Join(home, strings.TrimPrefix(filePath, "~"))
		}
	}
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(cwd, filePath)
	}
	return filepath.Clean(filePath)
}
