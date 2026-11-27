package bwrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

// ReadOnlyWorkspacePrompt is appended to the contract prompt body when an agent
// is configured with lock_mode: readonly.
const ReadOnlyWorkspacePrompt = "IMPORTANT: This agent is running in READ-ONLY mode (`lock_mode: readonly`). The workspace directory and mounted project directories are mounted read-only; you are NOT allowed to modify or write files in the workspace. If you need to create temporary files, scripts, scratchpads, or run experiments that write to disk, use `/tmp` (which is writable and isolated to this session)."

// ReadOnlySharedPrompt is appended after ReadOnlyWorkspacePrompt when the run
// has a shared spawn-tree dir mounted at /shared.
const ReadOnlySharedPrompt = "NOTE: `/tmp` is private to this chat and is not visible to other agents. Anything another agent must read (reports, findings, large outputs) must be written under `/shared`, which is writable and shared with the other agents in this spawn tree."

// buildContractBody assembles the CLI-agnostic prompt body for the AW_AGENTS.md
// contract: the global language rules (if non-empty) followed by the agent's
// own AGENTS.md content (when the file exists). The CLI-specific protocol
// headers (ask-user / peer delegation) are composed later by the aw dispatcher
// inside the sandbox via common.ComposePrompt.
func buildContractBody(agentsMDPath string, langRules string) (string, error) {
	var sb strings.Builder

	if trimmed := strings.TrimSpace(langRules); trimmed != "" {
		sb.WriteString(trimmed)
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

// writeContractFile renders the AW_AGENTS.md contract (identity metadata in a
// frontmatter block plus the prompt body) to a file named ".aw_agents.md"
// inside dir, and returns the host path. It returns "" when the contract
// would be empty.
func writeContractFile(dir string, cfg *agentspec.AgentConfig, agentsMDPath string, langRules string) (string, error) {
	body, err := buildContractBody(agentsMDPath, langRules)
	if err != nil {
		return "", err
	}
	if cfg != nil && cfg.IsReadOnly() {
		if body != "" {
			body += "\n\n" + ReadOnlyWorkspacePrompt
		} else {
			body = ReadOnlyWorkspacePrompt
		}
		if cfg.SharedChatID != "" {
			body += " " + ReadOnlySharedPrompt
		}
	}
	if cfg != nil && cfg.WorktreeRun != nil {
		if body != "" {
			body += "\n\n"
		}
		body += worktreePrompt(cfg.WorktreeRun)
	}
	contract := &common.Contract{Body: body}
	if cfg != nil {
		contract.AgentID = strings.TrimSpace(cfg.ID)
		contract.AgentName = strings.TrimSpace(cfg.Name)
		contract.Team = strings.TrimSpace(cfg.Team)
	}
	content := common.Render(contract)
	if content == "" {
		return "", nil
	}
	destPath := filepath.Join(dir, ".aw_agents.md")
	if err := os.WriteFile(destPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing AW_AGENTS.md contract file: %w", err)
	}
	return destPath, nil
}

// setupTmpDir determines the host directory for sandbox /tmp
// (/session-data/tmp/<chatID>) and ensures it exists.
func setupTmpDir(chatID string) (string, error) {
	if chatID == "" {
		chatID = "default"
	}
	tmpDir := runnerpaths.SessionTmpDir(chatID)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("creating tmp directory %q: %w", tmpDir, err)
	}
	return tmpDir, nil
}

// setupSessionDir determines the host directory for sandbox /session
// (/session-data/sessions/<chatID>) and ensures it exists.
func setupSessionDir(chatID string) (string, error) {
	if chatID == "" {
		chatID = "default"
	}
	sessionDir := runnerpaths.SessionDir(chatID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return "", fmt.Errorf("creating session directory %q: %w", sessionDir, err)
	}
	return sessionDir, nil
}

// SharedMountPoint is where the scratch directory of a spawn tree is mounted
// in the agent and command sandboxes.
const SharedMountPoint = "/shared"

// appendSharedArgs binds the spawn tree's scratch directory
// (/session-data/shared/<root chat>) read-write at /shared for a run that
// belongs to one. It is not the checkout, so it stays writable for a readonly
// run too; parents and children exchange large outputs through it.
func appendSharedArgs(args []string, cfg *agentspec.AgentConfig) ([]string, error) {
	if cfg == nil || cfg.SharedChatID == "" {
		return args, nil
	}
	id := cfg.SharedChatID
	if id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return nil, fmt.Errorf("invalid shared chat id %q", id)
	}
	dir := runnerpaths.SharedChatDir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating shared directory %q: %w", dir, err)
	}
	return append(args, "--bind", dir, SharedMountPoint), nil
}

// appendBaseSandboxArgs appends shared bubblewrap flags, mounts, and env vars (unshare flags, /tmp, PATH/system/lib mounts, proc/dev, HOME, PATH, TZ).
func appendBaseSandboxArgs(args []string, home string, chatID string) ([]string, error) {
	// Basic safety isolation flags
	args = append(args, "--die-with-parent")
	args = append(args, "--unshare-pid")
	args = append(args, "--unshare-ipc")
	args = append(args, "--unshare-uts")
	args = append(args, "--unshare-cgroup")

	// Mount chatID tmp directory to /tmp
	tmpDir, err := setupTmpDir(chatID)
	if err != nil {
		return nil, err
	}
	args = append(args, "--bind", tmpDir, "/tmp")

	// Mount chatID session directory to /session
	sessionDir, err := setupSessionDir(chatID)
	if err != nil {
		return nil, err
	}
	args = append(args, "--bind", sessionDir, "/session")

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

	// Mount library/timezone/etc/proc/dev paths as ro/proc/dev if they exist for binary dynamic linking compatibility and timezone inheritance.
	// /usr/libexec carries the compiler backends (cc1/cc1plus/collect2) and /usr/include the libc headers, so cgo-based builds
	// (e.g. `go test -race`) work inside the sandbox.
	extraROPaths := []string{"/lib", "/lib64", "/usr/lib", "/usr/libexec", "/usr/include", "/usr/share/zoneinfo", "/usr/share/zoneinfo-icu", "/etc"}
	for _, p := range extraROPaths {
		if !mountedPaths[p] {
			if _, err := os.Stat(p); err == nil {
				args = append(args, "--ro-bind", p, p)
				mountedPaths[p] = true
			}
		}
	}
	// If /etc/localtime is a symlink resolving to a target outside the mounted paths, mount it as well
	if realLocaltime, err := filepath.EvalSymlinks("/etc/localtime"); err == nil && realLocaltime != "/etc/localtime" {
		if !mountedPaths[realLocaltime] {
			if _, err := os.Stat(realLocaltime); err == nil {
				args = append(args, "--ro-bind", realLocaltime, realLocaltime)
				mountedPaths[realLocaltime] = true
			}
		}
	}
	if _, err := os.Stat("/proc"); err == nil {
		args = append(args, "--proc", "/proc")
	}
	if _, err := os.Stat("/dev"); err == nil {
		args = append(args, "--dev", "/dev")
	}

	// Set HOME, PATH, and TZ env
	args = append(args, "--setenv", "HOME", home)
	if pathEnv := os.Getenv("PATH"); pathEnv != "" {
		args = append(args, "--setenv", "PATH", pathEnv)
	}
	if tzEnv := os.Getenv("TZ"); tzEnv != "" {
		args = append(args, "--setenv", "TZ", tzEnv)
	}

	return args, nil
}

// appendSSHSandboxArgs adds SSH-related mounts (~/.ssh tmpfs masking, config mount, GIT_SSH_COMMAND & SSH_AUTH_SOCK envs).
func appendSSHSandboxArgs(args []string, home string) []string {
	// Ensure .ssh is masked with tmpfs to prevent key leak to any sandbox
	sshDir := filepath.Join(home, ".ssh")
	args = append(args, "--tmpfs", sshDir)

	// If host ~/.ssh/config exists, mount it read-only so SSH host aliases (e.g. ghhy) still work safely
	sshConfigPath := filepath.Join(sshDir, "config")
	if _, err := os.Stat(sshConfigPath); err == nil {
		args = append(args, "--ro-bind", sshConfigPath, sshConfigPath)
	}

	// If host ~/.ssh/known_hosts exists, mount it read-only
	knownHostsPath := filepath.Join(sshDir, "known_hosts")
	if _, err := os.Stat(knownHostsPath); err == nil {
		args = append(args, "--ro-bind", knownHostsPath, knownHostsPath)
	}

	// Mount *.pub public keys read-only so ssh can compute key fingerprints for IdentitiesOnly=yes via ssh-agent
	entries, err := os.ReadDir(sshDir)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pub") {
				pubPath := filepath.Join(sshDir, entry.Name())
				args = append(args, "--ro-bind", pubPath, pubPath)
			}
		}
	}

	// Pass through GIT_SSH_COMMAND if set
	if gitSshCmd := os.Getenv("GIT_SSH_COMMAND"); gitSshCmd != "" {
		args = append(args, "--setenv", "GIT_SSH_COMMAND", gitSshCmd)
	}

	// Pass through SSH_AUTH_SOCK if set in host environment
	if authSock := os.Getenv("SSH_AUTH_SOCK"); authSock != "" {
		if _, err := os.Stat(authSock); err == nil {
			args = append(args, "--bind", authSock, authSock)
			args = append(args, "--setenv", "SSH_AUTH_SOCK", authSock)
		}
	}

	return args
}

// appendConfigMaskArgs masks the server configuration file if it exists, preventing credential leaks.
func appendConfigMaskArgs(args []string, configPath string) []string {
	if strings.TrimSpace(configPath) == "" {
		return args
	}
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return args
	}
	if fi, err := os.Stat(absPath); err == nil && !fi.IsDir() {
		args = append(args, "--ro-bind", "/dev/null", absPath)
	}
	return args
}

// SandboxedEnv is set to "1" inside every Asgard bwrap sandbox. There bash and
// dash are replaced by fakebash, so code (and tests) that need a real shell
// can detect the situation and skip.
const SandboxedEnv = "ASGARD_SANDBOXED"
