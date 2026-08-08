package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsArtifact(t *testing.T) {
	// Setup temporary workspace directory with git repo
	workspaceDir, err := os.MkdirTemp("", "asgard-test-workspace-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	// Initialize git repo
	cmd := exec.Command("git", "init", workspaceDir)
	require.NoError(t, cmd.Run())

	// Create .gitignore in workspace
	gitignoreContent := "ignored.txt\nscratch/\n*.tmp\n"
	err = os.WriteFile(filepath.Join(workspaceDir, ".gitignore"), []byte(gitignoreContent), 0644)
	require.NoError(t, err)

	// Create agent config with custom RW paths
	customRW := filepath.Join(os.TempDir(), "asgard-agent-rw-dir")
	err = os.MkdirAll(customRW, 0755)
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(customRW) }()

	config := &AgentConfig{
		MountDirs: MountConfig{
			ReadWrite: []string{customRW},
		},
		RunDirs: []string{"/tmp/run_dir"},
	}

	t.Run("Agent Config RW Path", func(t *testing.T) {
		// File inside ReadWrite path -> true
		targetInRW := filepath.Join(customRW, "output.json")
		assert.True(t, IsArtifact(targetInRW, config, workspaceDir))

		// File inside RunDirs path -> true
		targetInRunDir := "/tmp/run_dir/temp_file.txt"
		assert.True(t, IsArtifact(targetInRunDir, config, workspaceDir))
	})

	t.Run("Gitignored Files in Workspace", func(t *testing.T) {
		// Ignored file -> true
		assert.True(t, IsArtifact("ignored.txt", config, workspaceDir))
		assert.True(t, IsArtifact(filepath.Join(workspaceDir, "scratch/test.py"), config, workspaceDir))
		assert.True(t, IsArtifact("data.tmp", config, workspaceDir))
	})

	t.Run("Unignored Project Code Files in Workspace", func(t *testing.T) {
		// Tracked or untracked project files not matching .gitignore -> false
		assert.False(t, IsArtifact("src/main.go", config, workspaceDir))
		assert.False(t, IsArtifact(filepath.Join(workspaceDir, "webui/App.vue"), config, workspaceDir))
		assert.False(t, IsArtifact("README.md", config, workspaceDir))
	})

	t.Run("Empty target path", func(t *testing.T) {
		assert.False(t, IsArtifact("", config, workspaceDir))
	})
}

// TestIsArtifactWorkspaceIsRunDir guards against the regression where the agent's
// workspace directory is also listed in config.RunDirs. In that setup, absolute
// paths reported by the agent must still be routed through the gitignore check
// instead of being unconditionally classified as artifacts.
func TestIsArtifactWorkspaceIsRunDir(t *testing.T) {
	workspaceDir, err := os.MkdirTemp("", "asgard-test-run-workspace-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	require.NoError(t, exec.Command("git", "init", workspaceDir).Run())
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, ".gitignore"), []byte("scratch/\n"), 0644))

	config := &AgentConfig{
		RunDirs: []string{workspaceDir},
	}

	// Absolute path to an unignored source file -> not an artifact.
	absMain := filepath.Join(workspaceDir, "src", "main.go")
	assert.False(t, IsArtifact(absMain, config, workspaceDir))

	// Absolute path to a gitignored file -> artifact.
	absScratch := filepath.Join(workspaceDir, "scratch", "demo.py")
	assert.True(t, IsArtifact(absScratch, config, workspaceDir))

	// Relative path to an unignored source file -> not an artifact.
	assert.False(t, IsArtifact("src/main.go", config, workspaceDir))
}
