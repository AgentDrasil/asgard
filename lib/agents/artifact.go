package agents

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// IsArtifact determines whether a given target file path should be treated as an artifact.
//
// Evaluation rules:
//  1. If targetPath falls under any ReadWrite mount or RunDir specified in config
//     (excluding the workspaceDir itself): -> Returns true.
//     The workspace is deliberately excluded here because it is the agent's project
//     tree and is handled by the gitignore check below; otherwise every source file
//     written into the workspace would be misclassified as an artifact.
//  2. If targetPath falls under workspaceDir:
//     -> Runs `git check-ignore` on the path relative to workspaceDir.
//     -> If git check-ignore returns exit status 0 (ignored by git): Returns true.
//     -> Otherwise (unignored project code modification or creation): Returns false.
//     Note: a non-git workspace yields a non-zero exit, so its files are treated as
//     non-artifacts.
//  3. Fallback: Returns false.
func IsArtifact(targetPath string, config *AgentConfig, workspaceDir string) bool {
	if targetPath == "" {
		return false
	}

	cleanTarget := filepath.Clean(targetPath)

	absWorkspace := ""
	if workspaceDir != "" {
		if aw, err := filepath.Abs(workspaceDir); err == nil {
			absWorkspace = filepath.Clean(aw)
		}
	}

	// Resolve target to absolute (relative to workspace) so Rule 1 and Rule 2
	// compare paths on the same basis regardless of how the agent reported them.
	absTarget := cleanTarget
	if absWorkspace != "" && !filepath.IsAbs(absTarget) {
		absTarget = filepath.Clean(filepath.Join(absWorkspace, cleanTarget))
	}

	// Rule 1: Check Agent Config RW Paths (MountDirs.ReadWrite & RunDirs),
	// skipping the workspace dir which is handled by Rule 2.
	if config != nil {
		rwPaths := make([]string, 0, len(config.MountDirs.ReadWrite)+len(config.RunDirs))
		rwPaths = append(rwPaths, config.MountDirs.ReadWrite...)
		rwPaths = append(rwPaths, config.RunDirs...)

		for _, rwDir := range rwPaths {
			if rwDir == "" {
				continue
			}
			cleanRW := filepath.Clean(rwDir)
			if absWorkspace != "" && cleanRW == absWorkspace {
				continue
			}
			if absTarget == cleanRW || strings.HasPrefix(absTarget, cleanRW+string(filepath.Separator)) {
				return true
			}
		}
	}

	// Rule 2: Workspace & Gitignore Check
	if absWorkspace != "" {
		relPath, err := filepath.Rel(absWorkspace, absTarget)
		if err == nil && !strings.HasPrefix(relPath, "..") && relPath != "." {
			// It is inside the workspace. Execute git check-ignore to test.
			cmd := exec.Command("git", "-C", absWorkspace, "check-ignore", "-q", relPath)
			if err := cmd.Run(); err == nil {
				// Exit status 0: path is ignored by git -> Artifact!
				return true
			}
			// Exit status 1 or non-zero: path is NOT ignored by git -> Not an artifact!
			return false
		}
	}

	return false
}
