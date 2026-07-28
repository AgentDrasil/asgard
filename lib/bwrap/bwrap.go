package bwrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/lib/agents"
)

// agentSystemPrompts holds the CLI-specific system prompt additions injected
// before any user-supplied AGENTS.md content.
var agentSystemPrompts = map[string]string{
	"agy": strings.Join([]string{
		"## Important Instructions",
		"",
		"- Forget the `ask_question` tool. When you want to ask the user a question, use a regular text response instead.",
	}, "\n"),
	"opencode": strings.Join([]string{
		"## Important Instructions",
		"",
		"- Forget the `question` tool. When you want to ask the user a question, use a regular text response instead.",
	}, "\n"),
}

// buildSystemPrompt constructs the full system prompt for the given CLI.
// It starts with the CLI-specific instructions and appends the content of
// agentsMDPath if the file exists.
func buildSystemPrompt(cli string, agentsMDPath string) (string, error) {
	var sb strings.Builder

	if base, ok := agentSystemPrompts[cli]; ok {
		sb.WriteString(base)
	}

	if agentsMDPath != "" {
		data, err := os.ReadFile(agentsMDPath)
		if err == nil && len(data) > 0 {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.Write(data)
		} else if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("reading AGENTS.md at %q: %w", agentsMDPath, err)
		}
	}

	return sb.String(), nil
}

// writeSystemPromptFile writes the combined system prompt for the given CLI to
// a file named ".asgard_system_prompt" inside dir, and returns the host path.
func writeSystemPromptFile(dir string, cli string, agentsMDPath string) (string, error) {
	content, err := buildSystemPrompt(cli, agentsMDPath)
	if err != nil {
		return "", err
	}
	if content == "" {
		return "", nil
	}
	destPath := filepath.Join(dir, ".asgard_system_prompt")
	if err := os.WriteFile(destPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing system prompt file: %w", err)
	}
	return destPath, nil
}

// setupTmpDir determines the host directory for sandbox /tmp (e.g. /home/user/tmp/<chatID>) and ensures it exists.
func setupTmpDir(home string, chatID string) (string, error) {
	if chatID == "" {
		chatID = "default"
	}
	tmpDir := filepath.Join(home, "tmp", chatID)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("creating tmp directory %q: %w", tmpDir, err)
	}
	return tmpDir, nil
}

// appendBaseSandboxArgs appends shared bubblewrap flags, mounts, and env vars (die-with-parent, unshare flags, /tmp, PATH/system/lib mounts, proc/dev, HOME, PATH).
func appendBaseSandboxArgs(args []string, home string, chatID string) ([]string, error) {
	// Basic safety isolation flags
	args = append(args, "--die-with-parent")
	args = append(args, "--unshare-pid")
	args = append(args, "--unshare-ipc")
	args = append(args, "--unshare-uts")
	args = append(args, "--unshare-cgroup")

	// Mount chatID tmp directory to /tmp
	tmpDir, err := setupTmpDir(home, chatID)
	if err != nil {
		return nil, err
	}
	args = append(args, "--bind", tmpDir, "/tmp")

	// Mount system paths and all PATH directories as read-only
	mountedPaths := make(map[string]bool)
	systemROPaths := []string{"/bin", "/usr/bin", "/usr/local/bin"}
	if pathEnv := os.Getenv("PATH"); pathEnv != "" {
		for _, p := range filepath.SplitList(pathEnv) {
			if p != "" {
				systemROPaths = append(systemROPaths, p)
			}
		}
	}
	for _, p := range systemROPaths {
		if !mountedPaths[p] {
			if _, err := os.Stat(p); err == nil {
				args = append(args, "--ro-bind", p, p)
				mountedPaths[p] = true
			}
		}
	}

	// Mount library/etc/proc/dev paths as ro/proc/dev if they exist for binary dynamic linking compatibility
	extraROPaths := []string{"/lib", "/lib64", "/usr/lib", "/etc"}
	for _, p := range extraROPaths {
		if !mountedPaths[p] {
			if _, err := os.Stat(p); err == nil {
				args = append(args, "--ro-bind", p, p)
				mountedPaths[p] = true
			}
		}
	}
	if _, err := os.Stat("/proc"); err == nil {
		args = append(args, "--proc", "/proc")
	}
	if _, err := os.Stat("/dev"); err == nil {
		args = append(args, "--dev", "/dev")
	}

	// Set HOME and PATH env
	args = append(args, "--setenv", "HOME", home)
	if pathEnv := os.Getenv("PATH"); pathEnv != "" {
		args = append(args, "--setenv", "PATH", pathEnv)
	}

	return args, nil
}

// buildArgsForAgent constructs the bubblewrap arguments for the given config, target, prompt, optional session, and runDir.
// It returns the list of arguments to pass to the bwrap executable.
func buildArgsForAgent(cfg *agents.AgentConfig, agentPath string, target agents.CLITarget, prompt string, session optional.Option[string], runDir string, sockDir string, chatID string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting user home directory: %w", err)
	}

	var args []string
	args, err = appendBaseSandboxArgs(args, home, chatID)
	if err != nil {
		return nil, err
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
		if _, err := os.Stat(runDir); err == nil {
			args = append(args, "--bind", runDir, runDir)
		} else {
			return nil, fmt.Errorf("run directory %q does not exist: %w", runDir, err)
		}
	}

	// Bind logs directory
	logDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logDir, 0755); err == nil {
		args = append(args, "--bind", logDir, logDir)
	}

	// Mount the socket directory to /fakebash
	if sockDir != "" {
		args = append(args, "--dir", "/fakebash")
		args = append(args, "--bind", sockDir, "/fakebash")
	}

	args = append(args, "--ro-bind", "/bin/fakebash", "/bin/bash")
	args = append(args, "--ro-bind", "/bin/fakebash", "/usr/bin/bash")

	// Change working directory to runDir in the sandbox
	args = append(args, "--chdir", runDir)

	if chatID != "" {
		args = append(args, "--setenv", "ASGARD_CHAT_ID", chatID)
	}

	// Target-specific mounts
	switch target.CLI {
	case "agy":
		geminiDir := filepath.Join(home, ".gemini")
		if _, err := os.Stat(geminiDir); err == nil {
			args = append(args, "--bind", geminiDir, geminiDir)
		}
	case "opencode":
		dirs := []string{
			filepath.Join(home, ".cache"),
			filepath.Join(home, ".config"),
			filepath.Join(home, ".local"),
			filepath.Join(home, ".npm"),
		}
		for _, dir := range dirs {
			if _, err := os.Stat(dir); err == nil {
				args = append(args, "--bind", dir, dir)
			}
		}
	}

	// Ignore ssh dir to prevent key leak
	sshDir := filepath.Join(home, ".ssh")
	if _, err := os.Stat(sshDir); err == nil {
		args = append(args, "--tmpfs", sshDir)
	}

	// Build and mount the system prompt, and mount skills/ if present in agentPath.
	// The system prompt is written to the chat tmpDir on the host so that bwrap
	// can bind-mount it read-only at the CLI's expected configuration path.
	{
		var agentsMDPath string
		var skillsPath string
		if agentPath != "" {
			candidate := filepath.Join(agentPath, "AGENTS.md")
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				agentsMDPath = candidate
			}
			skillsPath = filepath.Join(agentPath, "skills")
			if st, err := os.Stat(skillsPath); err != nil || !st.IsDir() {
				skillsPath = ""
			}
		}

		// Determine the host dir where we can write the prompt file (already mounted as /tmp).
		promptHostDir, err := setupTmpDir(home, chatID)
		if err != nil {
			return nil, err
		}

		switch target.CLI {
		case "agy":
			promptFile, err := writeSystemPromptFile(promptHostDir, "agy", agentsMDPath)
			if err != nil {
				return nil, err
			}
			if promptFile != "" {
				args = append(args, "--ro-bind", promptFile, filepath.Join(home, ".gemini", "GEMINI.md"))
			}
			if skillsPath != "" {
				args = append(args, "--ro-bind", skillsPath, filepath.Join(home, ".gemini", "antigravity-cli", "skills"))
			}
		case "opencode":
			promptFile, err := writeSystemPromptFile(promptHostDir, "opencode", agentsMDPath)
			if err != nil {
				return nil, err
			}
			if promptFile != "" {
				args = append(args, "--ro-bind", promptFile, filepath.Join(home, ".config", "opencode", "AGENTS.md"))
			}
			if skillsPath != "" {
				args = append(args, "--ro-bind", skillsPath, filepath.Join(home, ".config", "opencode", "skills"))
			}
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
func CommandForAgent(cfg *agents.AgentConfig, agentPath string, target agents.CLITarget, prompt string, session optional.Option[string], runDir string, sockDir string, chatID string) (*exec.Cmd, error) {
	bwrapArgs, err := buildArgsForAgent(cfg, agentPath, target, prompt, session, runDir, sockDir, chatID)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("bwrap", bwrapArgs...)
	return cmd, nil
}

// CommandForCommandExec creates an exec.Cmd initialized to run fakebashd inside a bubblewrap sandbox.
func CommandForCommandExec(runDir string, sockDir string, chatID string) (*exec.Cmd, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting user home directory: %w", err)
	}

	var args []string
	args, err = appendBaseSandboxArgs(args, home, chatID)
	if err != nil {
		return nil, err
	}

	// Bind HOME
	args = append(args, "--bind", home, home)

	// Ignore auth dir for agy and opencode, and ssh dir to prevent key leak
	agyAuthDir := filepath.Join(home, ".gemini")
	if _, err := os.Stat(agyAuthDir); err == nil {
		args = append(args, "--tmpfs", agyAuthDir)
	}
	opencodeAuthDir := filepath.Join(home, ".local", "share", "opencode")
	if _, err := os.Stat(opencodeAuthDir); err == nil {
		args = append(args, "--tmpfs", opencodeAuthDir)
	}
	sshDir := filepath.Join(home, ".ssh")
	if _, err := os.Stat(sshDir); err == nil {
		args = append(args, "--tmpfs", sshDir)
	}

	if runDir != "" {
		if _, err := os.Stat(runDir); err == nil {
			args = append(args, "--bind", runDir, runDir)
			args = append(args, "--chdir", runDir)
		} else {
			args = append(args, "--chdir", home)
		}
	} else {
		args = append(args, "--chdir", home)
	}

	// Mount the socket directory to /fakebash
	if sockDir != "" {
		args = append(args, "--dir", "/fakebash")
		args = append(args, "--bind", sockDir, "/fakebash")
	}

	args = append(args, "--")
	args = append(args, "/bin/fakebashd")

	cmd := exec.Command("bwrap", args...)
	return cmd, nil
}
