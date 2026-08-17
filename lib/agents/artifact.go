package agents

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// IsArtifact determines whether a given target file path should be treated as an artifact.
//
// Evaluation order:
//  1. Workspace membership takes priority. If targetPath is inside workspaceDir,
//     gitignore is the sole authority:
//     -> Runs `git check-ignore` on the path relative to workspaceDir.
//     -> Exit status 0 (ignored by git): Returns true.
//     -> Otherwise (tracked or unignored project file): Returns false.
//     This must run before any RW-mount check, because a configured run_dir is frequently
//     an ancestor of the workspace (e.g. run_dir=/home/user/src, workspace=/home/user/src/proj).
//     Without this priority, every tracked source file under the workspace would be
//     misclassified as an artifact. A non-git workspace yields a non-zero exit, so its
//     files are treated as non-artifacts.
//  2. Otherwise (target is outside the workspace): if it falls under any ReadWrite mount
//     or RunDir from config, treat it as an auxiliary artifact output area -> Returns true.
//  3. Fallback: Returns false.
func IsArtifact(targetPath string, config *AgentConfig, workspaceDir string) bool {
	if targetPath == "" {
		return false
	}

	cleanTarget := filepath.Clean(targetPath)
	trimmedTarget := strings.TrimSpace(targetPath)

	absWorkspace := ""
	if workspaceDir != "" {
		if aw, err := filepath.Abs(workspaceDir); err == nil {
			absWorkspace = filepath.Clean(aw)
		}
	}

	// Resolve target to absolute if possible, but keep /tmp and .tmp separate from workspace.
	var absTarget string
	if filepath.IsAbs(cleanTarget) {
		absTarget = cleanTarget
	} else if cleanTarget != "/tmp" && !strings.HasPrefix(cleanTarget, "/tmp/") &&
		cleanTarget != ".tmp" && !strings.HasPrefix(cleanTarget, ".tmp/") && absWorkspace != "" {
		absTarget = filepath.Clean(filepath.Join(absWorkspace, cleanTarget))
	}

	// 1. Primary: if target is inside workspaceDir, gitignore decides.
	if absWorkspace != "" && absTarget != "" {
		relPath, err := filepath.Rel(absWorkspace, absTarget)
		if err == nil && !strings.HasPrefix(relPath, "..") && relPath != "." {
			cmd := exec.Command("git", "-C", absWorkspace, "check-ignore", "-q", relPath)
			if err := cmd.Run(); err == nil {
				// Exit status 0: path is ignored by git -> artifact.
				return true
			}
			// Non-zero (incl. not-a-git-repo): tracked or unignored project file.
			return false
		}
	}

	// 2. Sandbox temp files (/tmp/ or .tmp/) outside the workspace are always artifacts.
	if trimmedTarget == "/tmp" || strings.HasPrefix(trimmedTarget, "/tmp/") ||
		trimmedTarget == ".tmp" || strings.HasPrefix(trimmedTarget, ".tmp/") {
		return true
	}

	// 3. Secondary: target outside the workspace, but under a configured RW mount or
	// RunDir -> auxiliary artifact output area.
	if config != nil && absTarget != "" {
		rwPaths := make([]string, 0, len(config.MountDirs.ReadWrite)+len(config.RunDirs))
		rwPaths = append(rwPaths, config.MountDirs.ReadWrite...)
		rwPaths = append(rwPaths, config.RunDirs...)

		for _, rwDir := range rwPaths {
			if rwDir == "" {
				continue
			}
			cleanRW := filepath.Clean(rwDir)
			if absTarget == cleanRW || strings.HasPrefix(absTarget, cleanRW+string(filepath.Separator)) {
				return true
			}
		}
	}

	return false
}
