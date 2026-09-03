package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// scopedDirRefRe matches `${tmp_dir}/...` and `${session_dir}/...` references
// in raw (un-interpolated) node prompts, e.g. `${tmp_dir}/plan/plan.md`.
var scopedDirRefRe = regexp.MustCompile(`\$\{(tmp_dir|session_dir)\}(/[^\s"'` + "`" + `\)\]}>,;]*)`)

// absPathRe matches absolute filesystem paths in interpolated prompt text,
// e.g. /home/user/tmp/0198a.../plan/plan.md.
var absPathRe = regexp.MustCompile(`/(?:[A-Za-z0-9._~@+-]+/)*[A-Za-z0-9._~@+-]+`)

// DefaultTmpDir returns the per-run temporary directory for a session:
// <home>/tmp/<sessionID>, the same host directory the sandbox binds as /tmp
// (see bwrap.setupTmpDir). This keeps workflow tmp files, sandbox agent
// output and session cleanup (dbmodels.CleanExpiredSessions) consistent.
// Falls back to os.TempDir()/<sessionID> when no home dir is resolvable.
func DefaultTmpDir(sessionID string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "tmp", sessionID)
	}
	return filepath.Join(os.TempDir(), sessionID)
}

// DefaultSessionDir returns the per-run session directory for a session:
// <home>/data/<sessionID>, the same host directory the sandbox binds as /session
// (see bwrap.setupSessionDir). Falls back to os.TempDir()/<sessionID> when no
// home dir is resolvable.
func DefaultSessionDir(sessionID string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "data", sessionID)
	}
	return filepath.Join(os.TempDir(), sessionID)
}

// ExtractArtifactPaths collects artifact file paths referenced by a human
// node prompt: explicit `${tmp_dir}/...` / `${session_dir}/...` references
// from the raw prompt plus absolute paths under the run's tmp/session/run
// directories found in the interpolated prompt. Only paths that exist as
// regular files are returned, in order of first appearance.
func ExtractArtifactPaths(rawPrompt, interpolatedPrompt, tmpDir, runDir string) []string {
	return ExtractArtifactPathsInSession(rawPrompt, interpolatedPrompt, tmpDir, runDir, "")
}

// ExtractArtifactPathsInSession extends ExtractArtifactPaths with the run's
// session directory (sandbox /session).
func ExtractArtifactPathsInSession(rawPrompt, interpolatedPrompt, tmpDir, runDir, sessionDir string) []string {
	var ordered []string
	seen := make(map[string]bool)
	add := func(p string) {
		p = filepath.Clean(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		ordered = append(ordered, p)
	}

	scopedDirs := map[string]string{"tmp_dir": tmpDir, "session_dir": sessionDir}
	for _, m := range scopedDirRefRe.FindAllStringSubmatch(rawPrompt, -1) {
		if dir := scopedDirs[m[1]]; dir != "" {
			add(filepath.Join(dir, m[2]))
		}
	}
	for _, m := range absPathRe.FindAllString(interpolatedPrompt, -1) {
		p := filepath.Clean(strings.TrimRight(m, ".,;:!?"))
		if isUnderDir(p, tmpDir) || isUnderDir(p, runDir) || isUnderDir(p, sessionDir) {
			add(p)
		}
	}

	out := make([]string, 0, len(ordered))
	for _, p := range ordered {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// ViewerArtifactPath translates a host artifact path into the form the
// workspace file API accepts. Paths under the run's tmp dir (the sandbox /tmp)
// are presented as /tmp/<rel> so the file endpoint can remap them back to
// <home>/tmp/<sessionID>/<rel>; other paths pass through unchanged.
func ViewerArtifactPath(path, tmpDir string) string {
	return ViewerArtifactPathInSession(path, tmpDir, "")
}

// ViewerArtifactPathInSession extends ViewerArtifactPath with the run's session
// dir (the sandbox /session): paths under sessionDir are presented as
// /session/<rel> so the file endpoint can remap them back to
// <home>/session/<sessionID>/<rel>.
func ViewerArtifactPathInSession(path, tmpDir, sessionDir string) string {
	clean := filepath.Clean(path)
	if tmpDir != "" {
		if rel, err := filepath.Rel(filepath.Clean(tmpDir), clean); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.Join("/tmp", rel)
		}
	}
	if sessionDir != "" {
		if rel, err := filepath.Rel(filepath.Clean(sessionDir), clean); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.Join("/session", rel)
		}
	}
	if strings.HasPrefix(clean, ".tmp/") {
		return "/tmp/" + strings.TrimPrefix(clean, ".tmp/")
	}
	if clean == ".tmp" {
		return "/tmp"
	}
	if strings.HasPrefix(clean, ".session/") {
		return "/session/" + strings.TrimPrefix(clean, ".session/")
	}
	if clean == ".session" {
		return "/session"
	}
	if strings.HasPrefix(clean, "/tmp/") || clean == "/tmp" || strings.HasPrefix(clean, "/session/") || clean == "/session" {
		return clean
	}
	return path
}

// ArtifactViewerPaths converts a node result's artifact map (declared name →
// host path) into a stable, sorted list of viewer-facing paths.
func ArtifactViewerPaths(artifacts map[string]string, tmpDir string) []string {
	return ArtifactViewerPathsInSession(artifacts, tmpDir, "")
}

// ArtifactViewerPathsInSession extends ArtifactViewerPaths with the run's
// session dir (see ViewerArtifactPathInSession).
func ArtifactViewerPathsInSession(artifacts map[string]string, tmpDir, sessionDir string) []string {
	if len(artifacts) == 0 {
		return nil
	}
	paths := make([]string, 0, len(artifacts))
	for _, p := range artifacts {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, ViewerArtifactPathInSession(p, tmpDir, sessionDir))
	}
	return out
}

func isUnderDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && !strings.HasPrefix(rel, ".."))
}
