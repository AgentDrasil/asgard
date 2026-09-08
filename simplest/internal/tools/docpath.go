package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DocToolOptions configures the markdown-only doc tools (write_doc, edit_doc).
type DocToolOptions struct {
	// AllowedDirs restricts the doc tools to markdown files under these
	// directories (absolute paths). When empty, any .md path is allowed.
	// Directories may be symlinks; they are resolved before matching.
	// Empty and relative entries are ignored.
	AllowedDirs []string
}

// DocPathPolicy validates paths submitted to the doc tools: the path must be
// a markdown file both as written and after resolving symlinks, and — when
// AllowedDirs is configured — the resolved absolute path must fall under one
// of the (resolved) allowed directories.
type DocPathPolicy struct {
	AllowedDirs []string
}

// resolveStrict resolves abs like filepath.EvalSymlinks but tolerates a
// non-existing tail (a file about to be created). Dangling symlinks anywhere
// in the path are rejected: os.WriteFile would follow them and create the
// file outside the validated location.
func resolveStrict(abs string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	// EvalSymlinks failed: either the tail does not exist (fine for new
	// files) or a symlink dangles (must reject).
	if fi, err := os.Lstat(abs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("dangling symlink at %q", abs)
	}
	// Walk up to the deepest existing ancestor; if it exists but does not
	// resolve, a symlink at or below it dangles.
	prefix := filepath.Dir(abs)
	for prefix != abs {
		_, err := os.Lstat(prefix)
		if os.IsNotExist(err) {
			prefix = filepath.Dir(prefix)
			continue
		}
		if err != nil {
			return "", fmt.Errorf("cannot stat %q: %w", prefix, err)
		}
		resolvedPrefix, err := filepath.EvalSymlinks(prefix)
		if err != nil {
			return "", fmt.Errorf("dangling symlink in path %q (at %q)", abs, prefix)
		}
		return filepath.Join(resolvedPrefix, strings.TrimPrefix(abs, prefix)), nil
	}
	return filepath.Clean(abs), nil
}

// matches reports whether resolved falls under dir (both absolute).
func matches(resolved, dir string) bool {
	return resolved == dir || strings.HasPrefix(resolved, dir+string(filepath.Separator))
}

// Resolve validates rawPath (a path exactly as provided by the model),
// resolving it against cwd, and returns the symlink-resolved absolute path.
// Callers must write to the returned path, not the original input: re-writing
// the original would resolve symlinks a second time, reintroducing a
// time-of-check/time-of-use gap between validation and the actual write.
func (p DocPathPolicy) Resolve(rawPath, cwd string) (string, error) {
	if !strings.EqualFold(filepath.Ext(rawPath), ".md") {
		return "", fmt.Errorf("only markdown files ending in .md are supported, got %q", rawPath)
	}
	abs := resolveToCwd(rawPath, cwd)
	resolved, err := resolveStrict(abs)
	if err != nil {
		return "", fmt.Errorf("cannot safely resolve %q: %w", rawPath, err)
	}
	if !strings.EqualFold(filepath.Ext(resolved), ".md") {
		return "", fmt.Errorf("path %q resolves to non-markdown target %q", rawPath, resolved)
	}
	if len(p.AllowedDirs) == 0 {
		return resolved, nil
	}
	for _, dir := range p.AllowedDirs {
		// Skip empty entries (Clean("") would become "." and match the
		// process working directory) and relative entries (they would
		// resolve against the process working directory, not the tool cwd).
		if strings.TrimSpace(dir) == "" || !filepath.IsAbs(dir) {
			continue
		}
		resolvedDir, err := resolveStrict(filepath.Clean(dir))
		if err != nil {
			// Unresolvable (e.g. dangling) allowed dir can never match.
			continue
		}
		if matches(resolved, resolvedDir) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path %q resolves to %q, outside the allowed directories %q", rawPath, resolved, p.AllowedDirs)
}
