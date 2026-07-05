package bwrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/lib/agents"
)

// buildArgsForAgent constructs the bubblewrap arguments for the given config, target, prompt, optional session, and runDir.
// It returns the list of arguments to pass to the bwrap executable.
func buildArgsForAgent(cfg *agents.AgentConfig, target agents.CLITarget, prompt string, session optional.Option[string], runDir string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting user home directory: %w", err)
	}

	var args []string

	// Basic safety isolation flags
	args = append(args, "--die-with-parent")
	args = append(args, "--unshare-pid")
	args = append(args, "--unshare-ipc")
	args = append(args, "--unshare-uts")
	args = append(args, "--unshare-cgroup")

	// Mount tmpfs for /tmp
	args = append(args, "--tmpfs", "/tmp")

	// Mount system paths as read-only
	systemROPaths := []string{"/bin", "/usr/bin", "/usr/local/bin"}
	for _, p := range systemROPaths {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		}
	}

	// Mount library/etc/proc/dev paths as ro/proc/dev if they exist for binary dynamic linking compatibility
	extraROPaths := []string{"/lib", "/lib64", "/etc"}
	for _, p := range extraROPaths {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		}
	}
	if _, err := os.Stat("/proc"); err == nil {
		args = append(args, "--proc", "/proc")
	}
	if _, err := os.Stat("/dev"); err == nil {
		args = append(args, "--dev", "/dev")
	}

	// Mount roles config run_dirs as read-write
	if cfg != nil {
		for _, dir := range cfg.RunDirs {
			if _, err := os.Stat(dir); err != nil {
				return nil, fmt.Errorf("run directory %q does not exist: %w", dir, err)
			}
			args = append(args, "--bind", dir, dir)
		}

		// Mount additional directories from MountDirs
		for _, dir := range cfg.MountDirs.ReadOnly {
			if _, err := os.Stat(dir); err != nil {
				return nil, fmt.Errorf("mount readonly directory %q does not exist: %w", dir, err)
			}
			args = append(args, "--ro-bind", dir, dir)
		}
		for _, dir := range cfg.MountDirs.ReadWrite {
			if _, err := os.Stat(dir); err != nil {
				return nil, fmt.Errorf("mount readwrite directory %q does not exist: %w", dir, err)
			}
			args = append(args, "--bind", dir, dir)
		}
	}

	// Ensure runDir is mounted as read-write
	runDirMounted := false
	if cfg != nil {
		for _, dir := range cfg.RunDirs {
			if dir == runDir {
				runDirMounted = true
				break
			}
		}
		if !runDirMounted {
			for _, dir := range cfg.MountDirs.ReadWrite {
				if dir == runDir {
					runDirMounted = true
					break
				}
			}
		}
	}
	if !runDirMounted {
		if _, err := os.Stat(runDir); err != nil {
			return nil, fmt.Errorf("run directory %q does not exist: %w", runDir, err)
		}
		args = append(args, "--bind", runDir, runDir)
	}

	// Change working directory to runDir in the sandbox
	args = append(args, "--chdir", runDir)

	// Set HOME env
	args = append(args, "--setenv", "HOME", home)

	// Target-specific mounts
	switch target.CLI {
	case "agy":
		geminiDir := filepath.Join(home, ".gemini")
		if _, err := os.Stat(geminiDir); err != nil {
			return nil, fmt.Errorf("gemini directory %q does not exist: %w", geminiDir, err)
		}
		args = append(args, "--bind", geminiDir, geminiDir)
	case "opencode":
		dirs := []string{
			filepath.Join(home, ".cache"),
			filepath.Join(home, ".config"),
			filepath.Join(home, ".local"),
		}
		for _, dir := range dirs {
			if _, err := os.Stat(dir); err != nil {
				return nil, fmt.Errorf("directory %q does not exist: %w", dir, err)
			}
			args = append(args, "--bind", dir, dir)
		}
	}

	// End of bubblewrap arguments
	args = append(args, "--")

	// Target executable and its arguments
	args = append(args, "aw")
	args = append(args, target.CLI)
	args = append(args, "--model", target.Model)
	if session.IsSome() {
		sessVal := session.Unwrap()
		if sessVal != "" {
			args = append(args, "--session", sessVal)
		}
	}
	if prompt != "" {
		args = append(args, "--prompt", prompt)
	}

	return args, nil
}

// CommandForAgent creates an exec.Cmd initialized to run the target CLI inside bubblewrap sandbox.
func CommandForAgent(cfg *agents.AgentConfig, target agents.CLITarget, prompt string, session optional.Option[string], runDir string) (*exec.Cmd, error) {
	bwrapArgs, err := buildArgsForAgent(cfg, target, prompt, session, runDir)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("bwrap", bwrapArgs...)
	return cmd, nil
}
