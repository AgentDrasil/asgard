package agents

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// StatusArtifact is an artifact file named by an agent status update.
type StatusArtifact struct {
	// ViewerPath is the form the file API accepts (/tmp/<rel>, /session/<rel>,
	// workspace-relative or absolute).
	ViewerPath string
	// HostPath is the file's absolute path on the machine running the agent.
	HostPath string
}

// StatusArtifacts decides which of the status update's target_files are
// artifacts of the run, exactly as the single-agent path records them: the
// relative tmp/ and session/ forms are resolved against the workspace and the
// session directories (with stats), then agents.IsArtifact applies
// (gitignore, tmp/session areas, read-write mounts). It is shared by the
// backend and the runner.
func StatusArtifacts(chatID string, targetFiles []string, agentConfig *agentspec.AgentConfig, workspaceDir string) []StatusArtifact {
	baseTmp := GetSessionTmpBaseDir(chatID)
	baseSession := GetSessionScopedBaseDir("session", chatID)
	isRunDirSessionTmp := (workspaceDir != "" && (workspaceDir == baseTmp || NormalizeSessionRunDir(workspaceDir, chatID) == baseTmp))

	var artifacts []StatusArtifact
	for _, tf := range targetFiles {
		processedPath := tf

		if isRelTmp, sub := RelativeScopedPrefixedPath(tf, chatID, "tmp"); isRelTmp {
			sub = filepath.Clean(sub)
			if sub == "." {
				sub = ""
			}
			canonicalTmpPath := "/tmp"
			if sub != "" {
				canonicalTmpPath = "/tmp/" + sub
			}

			if isRunDirSessionTmp {
				processedPath = canonicalTmpPath
			} else {
				// workspaceDir is regular project workspace
				wsFilePath := tf
				if workspaceDir != "" && !filepath.IsAbs(tf) {
					wsFilePath = filepath.Join(workspaceDir, tf)
				}
				if _, err := os.Stat(wsFilePath); err == nil {
					// Exists in workspace -> keep original workspace relative path
					processedPath = tf
				} else {
					// Does not exist in workspace, check session tmp
					tmpFilePath := filepath.Join(baseTmp, sub)
					if _, err := os.Stat(tmpFilePath); err == nil {
						processedPath = canonicalTmpPath
					} else {
						processedPath = tf
					}
				}
			}
		} else if isRelSess, sub := RelativeScopedPrefixedPath(tf, chatID, "session"); isRelSess {
			sub = filepath.Clean(sub)
			if sub == "." {
				sub = ""
			}
			canonicalSessionPath := "/session"
			if sub != "" {
				canonicalSessionPath = "/session/" + sub
			}

			// workspaceDir is regular project workspace
			wsFilePath := tf
			if workspaceDir != "" && !filepath.IsAbs(tf) {
				wsFilePath = filepath.Join(workspaceDir, tf)
			}
			if _, err := os.Stat(wsFilePath); err == nil {
				// Exists in workspace -> keep original workspace relative path
				processedPath = tf
			} else {
				// Does not exist in workspace, check session namespace
				sessFilePath := filepath.Join(baseSession, sub)
				if _, err := os.Stat(sessFilePath); err == nil {
					processedPath = canonicalSessionPath
				} else {
					processedPath = tf
				}
			}
		}

		if IsArtifact(processedPath, agentConfig, workspaceDir) {
			normalizedViewerPath := ViewerArtifactPath(processedPath, baseTmp, baseSession)
			artifacts = append(artifacts, StatusArtifact{
				ViewerPath: normalizedViewerPath,
				HostPath:   statusArtifactHostPath(normalizedViewerPath, workspaceDir, baseTmp, baseSession),
			})
		}
	}
	return artifacts
}

// statusArtifactHostPath maps a viewer path onto the agent machine's filesystem.
func statusArtifactHostPath(viewer, workspaceDir, baseTmp, baseSession string) string {
	for _, m := range []struct{ prefix, base string }{{"/tmp", baseTmp}, {"/session", baseSession}} {
		if viewer == m.prefix {
			return m.base
		}
		if rel, ok := strings.CutPrefix(viewer, m.prefix+"/"); ok {
			return filepath.Join(m.base, rel)
		}
	}
	if filepath.IsAbs(viewer) || workspaceDir == "" {
		return filepath.Clean(viewer)
	}
	return filepath.Join(workspaceDir, viewer)
}
