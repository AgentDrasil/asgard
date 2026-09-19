package bwrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/backend/lib/proxy"
	"github.com/AgentDrasil/asgard/fakebash"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

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
	contract := &common.Contract{Body: body}
	if cfg != nil {
		contract.AgentID = strings.TrimSpace(cfg.ID)
		contract.AgentName = strings.TrimSpace(cfg.Name)
		contract.ToolAccess = strings.TrimSpace(cfg.ToolAccess)
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
// (~/asgard/data/tmp/<chatID>) and ensures it exists.
func setupTmpDir(chatID string) (string, error) {
	if chatID == "" {
		chatID = "default"
	}
	tmpDir := paths.SessionTmpDir(chatID)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("creating tmp directory %q: %w", tmpDir, err)
	}
	return tmpDir, nil
}

// setupSessionDir determines the host directory for sandbox /session
// (~/asgard/data/sessions/<chatID>) and ensures it exists.
func setupSessionDir(chatID string) (string, error) {
	if chatID == "" {
		chatID = "default"
	}
	sessionDir := paths.SessionDir(chatID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return "", fmt.Errorf("creating session directory %q: %w", sessionDir, err)
	}
	return sessionDir, nil
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

	// Mount library/timezone/etc/proc/dev paths as ro/proc/dev if they exist for binary dynamic linking compatibility and timezone inheritance
	extraROPaths := []string{"/lib", "/lib64", "/usr/lib", "/usr/share/zoneinfo", "/usr/share/zoneinfo-icu", "/etc"}
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

// ProxySandboxConfig defines proxy configuration options for Bubblewrap sandbox.
type ProxySandboxConfig struct {
	Enabled         bool
	ProxyAddr       string            // e.g. "http://127.0.0.1:8082"
	CACert          string            // Host Asgard CA cert path (absolute)
	CAKey           string            // Host Asgard CA private key path (absolute)
	ProxyConfigPath string            // Host standalone proxy config path (absolute, if any)
	EnvVars         map[string]string // Custom environment variables (e.g. dummy secrets) to expose to sandbox
}

// appendProxySensitiveMaskArgs masks the Asgard config directory (which holds
// config.yaml, proxy.yaml and the CA private key) to prevent code inside the
// sandbox from reading sensitive credentials.
func appendProxySensitiveMaskArgs(args []string, caKey, proxyConfigPath string) []string {
	configDir := paths.ConfigDir()
	if fi, err := os.Stat(configDir); err == nil && fi.IsDir() {
		args = append(args, "--tmpfs", configDir)
	}

	if caKey != "" {
		if fi, err := os.Stat(caKey); err == nil && !fi.IsDir() {
			args = append(args, "--ro-bind", "/dev/null", caKey)
		}
	} else {
		// Defense against leftover private keys in the default path when proxy is disabled
		defaultKey := paths.CAKeyFile()
		if fi, err := os.Stat(defaultKey); err == nil && !fi.IsDir() {
			args = append(args, "--ro-bind", "/dev/null", defaultKey)
		}
	}

	if proxyConfigPath != "" {
		if fi, err := os.Stat(proxyConfigPath); err == nil && !fi.IsDir() {
			args = append(args, "--ro-bind", "/dev/null", proxyConfigPath)
		}
	}

	return args
}

// appendCrossSessionMaskArgs masks the host runtime-data root (~/asgard/data),
// which contains every session's tmp/session directories and the SQLite
// database, when allowCrossSession is false. The current session's /tmp and
// /session are separate bind mounts and stay visible.
func appendCrossSessionMaskArgs(args []string) []string {
	args = append(args, "--tmpfs", paths.DataDir())
	return args
}

// buildArgsForAgent constructs the bubblewrap arguments for the given config, target, prompt, optional session, and runDir.
// It returns the list of arguments to pass to the bwrap executable.
func buildArgsForAgent(cfg *agentspec.AgentConfig, agentPath string, target agentspec.CLITarget, prompt string, session optional.Option[string], runDir string, sockDir string, chatID string, langRules string, configPath string, proxyOpts ...ProxySandboxConfig) ([]string, error) {
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
	logDir := paths.LogsDir()
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
	// ASGARD_MODEL identifies the selected CLI target's model inside the
	// sandbox so aw status reports and ask-user questions can be attributed
	// to it by the host.
	if target.Model != "" {
		args = append(args, "--setenv", "ASGARD_MODEL", target.Model)
	}

	spec := agentwrapper.GetSandboxSpec(target.CLI)

	// Target-specific mounts
	if spec != nil {
		for _, dir := range spec.MountDirectories(home) {
			if _, err := os.Stat(dir); err == nil {
				args = append(args, "--bind", dir, dir)
			}
		}
	}

	// Append unified SSH sandbox mounts and environment variables
	args = appendSSHSandboxArgs(args, home)

	// Append config file and proxy sensitive file masking after all directory mounts
	// to prevent subsequent binds from shadowing /dev/null masking (D1/R1/N2)
	args = appendConfigMaskArgs(args, configPath)
	var caKey, proxyConfigPath string
	if len(proxyOpts) > 0 {
		caKey = proxyOpts[0].CAKey
		proxyConfigPath = proxyOpts[0].ProxyConfigPath
	}
	args = appendProxySensitiveMaskArgs(args, caKey, proxyConfigPath)

	// Build and mount the AW_AGENTS.md contract, and mount skills/ if present
	// in agentPath. The contract is written to the chat tmpDir on the host and
	// bind-mounted read-only at the single canonical sandbox path
	// /session/AW_AGENTS.md (also exported as $AW_AGENTS_PATH) regardless of
	// the target CLI. The aw dispatcher inside the sandbox translates the
	// contract for each CLI, so this layer never addresses CLI-specific
	// configuration paths.
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

		// Determine the host dir where we can write the contract file (already mounted as /tmp).
		promptHostDir, err := setupTmpDir(chatID)
		if err != nil {
			return nil, err
		}

		contractFile, err := writeContractFile(promptHostDir, cfg, agentsMDPath, langRules)
		if err != nil {
			return nil, err
		}
		args = append(args, "--setenv", common.EnvVar, common.DefaultPath)
		if contractFile != "" {
			args = append(args, "--ro-bind", contractFile, common.DefaultPath)
		}
		if spec != nil && skillsPath != "" {
			if dest := spec.SkillsMountPath(home); dest != "" {
				args = append(args, "--ro-bind", skillsPath, dest)
			}
		}
	}

	// End of bubblewrap arguments
	args = append(args, "--")

	// Target executable and its arguments
	args = append(args, "aw")
	args = append(args, target.CLI)
	// Agent identity: the sandbox-side adapter uses it to materialize
	// CLI-native agent definitions from the AW_AGENTS.md contract.
	if cfg != nil && strings.TrimSpace(cfg.ID) != "" {
		args = append(args, "--agent", cfg.ID)
	}
	args = append(args, "--model", target.Model)
	if spec != nil {
		args = append(args, spec.ExtraArgs()...)
	}
	// Only the simplest CLI understands --tool-access, which selects the
	// agent's tool set (doc-only agents lose bash/edit/write in favor of
	// write_doc/edit_doc).
	if target.CLI == "simplest" && cfg != nil && cfg.ToolAccess != "" {
		args = append(args, "--tool-access", cfg.ToolAccess)
	}
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
func CommandForAgent(cfg *agentspec.AgentConfig, agentPath string, target agentspec.CLITarget, prompt string, session optional.Option[string], runDir string, sockDir string, chatID string, langRules string, configPath string, proxyOpts ...ProxySandboxConfig) (*exec.Cmd, error) {
	bwrapArgs, err := buildArgsForAgent(cfg, agentPath, target, prompt, session, runDir, sockDir, chatID, langRules, configPath, proxyOpts...)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("bwrap", bwrapArgs...)
	return cmd, nil
}

// CommandForCommandExec creates an exec.Cmd initialized to run fakebashd inside a bubblewrap sandbox.
func CommandForCommandExec(runDir string, sockDir string, chatID string, configPath string, allowCrossSession bool, proxyOpts ...ProxySandboxConfig) (*exec.Cmd, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting user home directory: %w", err)
	}

	var args []string
	args, err = appendBaseSandboxArgs(args, home, chatID)
	if err != nil {
		return nil, err
	}

	// Bind runDir (when outside HOME) BEFORE the sensitive-mask block so that
	// a non-HOME runDir subtree can never shadow the proxy/config masks.
	if runDir != "" {
		if _, err := os.Stat(runDir); err == nil {
			// Skip the extra bind when runDir is inside HOME: HOME is already
			// bind-mounted as a single mount, and a nested separate bind would
			// give runDir a different st_dev, breaking hard links (pnpm would
			// then relocate its store onto the project's "filesystem"). This
			// requires runDir to be part of the HOME mount (not a separate
			// container volume mounted under HOME).
			if !strings.HasPrefix(runDir, home+string(filepath.Separator)) {
				args = append(args, "--bind", runDir, runDir)
			}
			args = append(args, "--chdir", runDir)
		} else {
			args = append(args, "--chdir", home)
		}
	} else {
		args = append(args, "--chdir", home)
	}

	// Bind HOME
	args = append(args, "--bind", home, home)

	// Ignore auth dir for all registered CLIs, and ssh dir to prevent key leak
	for _, cli := range agentwrapper.GetRegisteredCLIs() {
		if spec := agentwrapper.GetSandboxSpec(cli); spec != nil {
			if authDir := spec.AuthDirectory(home); authDir != "" {
				if _, err := os.Stat(authDir); err == nil {
					args = append(args, "--tmpfs", authDir)
				}
			}
		}
	}
	// Append unified SSH sandbox mounts and environment variables
	args = appendSSHSandboxArgs(args, home)

	// Authoritative late masking of config file and proxy sensitive files
	// (ca.key and proxy config) after all directory binds (D1/R1/N2)
	args = appendConfigMaskArgs(args, configPath)
	var caKey, proxyConfigPath string
	if len(proxyOpts) > 0 {
		caKey = proxyOpts[0].CAKey
		proxyConfigPath = proxyOpts[0].ProxyConfigPath
	}
	args = appendProxySensitiveMaskArgs(args, caKey, proxyConfigPath)

	// Filter out the host runtime-data root (~/asgard/data) unless cross-session debugging is allowed
	if !allowCrossSession {
		args = appendCrossSessionMaskArgs(args)
	}

	// If proxy is enabled for this sandbox, mount merged CA bundle and inject proxy env vars
	if len(proxyOpts) > 0 && proxyOpts[0].Enabled {
		proxyCfg := proxyOpts[0]
		cID := chatID
		if cID == "" {
			cID = "default"
		}
		mergedBundlePath := filepath.Join(paths.CABundleDir(cID), "merged-ca-certificates.crt")
		if err := proxy.MergeCACert("/etc/ssl/certs/ca-certificates.crt", proxyCfg.CACert, mergedBundlePath); err != nil {
			return nil, fmt.Errorf("merging CA cert bundle: %w", err)
		}

		args = append(args, "--ro-bind", mergedBundlePath, "/etc/ssl/certs/ca-certificates.crt")
		if _, err := os.Stat("/etc/ssl/cert.pem"); err == nil {
			args = append(args, "--ro-bind", mergedBundlePath, "/etc/ssl/cert.pem")
		}

		// Inject protected proxy and CA bundle environment variables
		for _, k := range fakebash.ProtectedProxyEnvKeys {
			switch k {
			case "HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy":
				if proxyCfg.ProxyAddr != "" {
					args = append(args, "--setenv", k, proxyCfg.ProxyAddr)
				}
			case "NO_PROXY", "no_proxy":
				args = append(args, "--setenv", k, "localhost,127.0.0.1")
			case "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "CURL_CA_BUNDLE":
				args = append(args, "--setenv", k, "/etc/ssl/certs/ca-certificates.crt")
			}
		}

		// Inject custom proxy env vars (dummy secrets exposed to command sandbox)
		for envKey, dummyVal := range proxyCfg.EnvVars {
			if strings.TrimSpace(envKey) != "" {
				args = append(args, "--setenv", envKey, dummyVal)
			}
		}
	}

	if sockDir != "" {
		args = append(args, "--dir", "/fakebash")
		args = append(args, "--bind", sockDir, "/fakebash")
	}

	args = append(args, "--")
	args = append(args, "/bin/fakebashd")

	cmd := exec.Command("bwrap", args...)
	return cmd, nil
}
