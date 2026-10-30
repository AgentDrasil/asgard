package bwrap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/common"
	awconfig "github.com/AgentDrasil/asgard/agentwrapper/config"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
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
	TypesafeAPIKey  string            // TypeSafe API key for Jev System One evaluation
	GeminiAPIKey    string            // Gemini API key for Level 2 GenAI summarizer
	CompassModel    string            // Gemini model for command result compass
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
	args, err := buildSandboxArgs(cfg, agentPath, target, false, runDir, sockDir, chatID, langRules, configPath, proxyOpts...)
	if err != nil {
		return nil, err
	}

	// Target executable and its arguments
	args = append(args, "aw")
	args = append(args, target.CLI)
	// Agent identity: the sandbox-side adapter uses it to materialize
	// CLI-native agent definitions from the AW_AGENTS.md contract.
	if cfg != nil && strings.TrimSpace(cfg.ID) != "" {
		args = append(args, "--agent", cfg.ID)
	}
	args = append(args, "--model", target.Model)
	if spec := agentwrapper.GetSandboxSpec(target.CLI); spec != nil {
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

// buildSandboxArgs constructs the bubblewrap arguments up to and including the
// "--" separator, i.e. everything except the command run inside the sandbox.
func buildSandboxArgs(cfg *agentspec.AgentConfig, agentPath string, target agentspec.CLITarget, allCLIs bool, runDir string, sockDir string, chatID string, langRules string, configPath string, proxyOpts ...ProxySandboxConfig) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting user home directory: %w", err)
	}

	var args []string
	args, err = appendBaseSandboxArgs(args, home, chatID)
	if err != nil {
		return nil, err
	}

	// Every read-write bind below is demoted to read-only when its path is at
	// or beneath a readonly mount root (e.g. through a symlink alias).
	var readonlyDirs []string
	if cfg != nil {
		readonlyDirs = cfg.MountDirs.ReadOnly
	}

	if err := validateReadonlyRoots(readonlyDirs, []string{filepath.Join(home, ".ssh"), paths.ConfigDir()}); err != nil {
		return nil, err
	}

	// Mount roles config run_dirs as read-write
	if cfg != nil {
		for _, dir := range cfg.RunDirs {
			if _, err := os.Stat(dir); err != nil {
				return nil, fmt.Errorf("run directory %q does not exist: %w", dir, err)
			}
			args = appendBind(args, readonlyDirs, dir)
		}

		// Mount additional directories from MountDirs. Readonly roots are
		// bound below, after every read-write bind (a later bind of an
		// ancestor would otherwise bury them); a missing one is skipped with
		// a warning by appendReadonlyBinds, as in command sandboxes.
		for _, dir := range cfg.MountDirs.ReadWrite {
			if _, err := os.Stat(dir); err != nil {
				return nil, fmt.Errorf("mount readwrite directory %q does not exist: %w", dir, err)
			}
			args = appendBind(args, readonlyDirs, dir)
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
			args = appendBind(args, readonlyDirs, runDir)
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
	args = append(args, "--ro-bind", "/bin/fakebash", "/bin/sh")
	args = append(args, "--ro-bind", "/bin/fakebash", "/usr/bin/sh")

	// Mask fish so it is not usable inside the sandbox (only if present on host).
	for _, p := range []string{"/usr/bin/fish", "/bin/fish"} {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", "/dev/null", p)
		}
	}

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

	// Inject TypeSafe Jev API key, Gemini API key, and compass model if available
	typesafeKey := os.Getenv("TYPESAFE_API_KEY")
	geminiKey := os.Getenv("GEMINI_API_KEY")
	compassModel := os.Getenv("GEMINI_MODEL_FOR_COMMAND_RESULT_COMPASS")
	if len(proxyOpts) > 0 {
		if proxyOpts[0].TypesafeAPIKey != "" {
			typesafeKey = proxyOpts[0].TypesafeAPIKey
		}
		if proxyOpts[0].GeminiAPIKey != "" {
			geminiKey = proxyOpts[0].GeminiAPIKey
		}
		if proxyOpts[0].CompassModel != "" {
			compassModel = proxyOpts[0].CompassModel
		}
	}
	if typesafeKey != "" {
		args = append(args, "--setenv", "TYPESAFE_API_KEY", typesafeKey)
	}
	if geminiKey != "" {
		args = append(args, "--setenv", "GEMINI_API_KEY", geminiKey)
	}
	if compassModel != "" {
		args = append(args, "--setenv", "GEMINI_MODEL_FOR_COMMAND_RESULT_COMPASS", compassModel)
	}

	spec := agentwrapper.GetSandboxSpec(target.CLI)

	// Target-specific mounts. The metadata-query sandbox (allCLIs) needs the
	// auth of every registered CLI.
	specs := []types.SandboxSpec{}
	if allCLIs {
		clis := agentwrapper.GetRegisteredCLIs()
		sort.Strings(clis)
		for _, cli := range clis {
			if sp := agentwrapper.GetSandboxSpec(cli); sp != nil {
				specs = append(specs, sp)
			}
		}
	} else if spec != nil {
		specs = append(specs, spec)
	}
	envs := map[string]string{}
	for _, sp := range specs {
		for _, dir := range sp.MountDirectories(home) {
			if _, err := os.Stat(dir); err == nil {
				args = appendBind(args, readonlyDirs, dir)
			}
		}
		for k, v := range sp.ExtraEnv(home) {
			envs[k] = v
		}
	}
	// Readonly mounts last among the directory binds, so they win over any
	// read-write bind of an ancestor (run_dirs, mount_dirs.readwrite, CLI dirs).
	if args, err = appendReadonlyBinds(args, readonlyDirs); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(envs))
	for k := range envs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--setenv", k, envs[k])
	}

	// The query sandbox runs `aw models|usage`, which filters by the aw config
	// allow-list. Mount the resolved file read-only at a fixed path and point
	// aw at it, so it does not depend on how (or whether) the host path, which
	// may be a chain of symlinks, resolves inside the sandbox.
	if allCLIs {
		if awConfig, err := awconfig.Path(); err == nil {
			if resolved, err := filepath.EvalSymlinks(awConfig); err == nil {
				if fi, err := os.Stat(resolved); err == nil && !fi.IsDir() {
					args = append(args, "--ro-bind", resolved, queryConfigPath, "--setenv", awconfig.EnvPath, queryConfigPath)
				}
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

// queryConfigPath is where the query sandbox mounts the aw config. It lives
// under the writable /session bind because /etc is read-only, so bwrap could
// not create a mount point there.
const queryConfigPath = "/session/aw-config.yaml"

// CommandForQuery creates an exec.Cmd that runs `aw <args...>` inside a
// dedicated, ephemeral sandbox. It is used for read-only metadata queries
// (models, usage) that invoke the vendor CLIs non-interactively, which would
// otherwise run on the host and could trigger the CLIs' hooks. The sandbox
// mounts the credentials of every registered CLI, since one `aw` call queries
// them all. The returned cleanup removes the query's throwaway directories.
func CommandForQuery(ctx context.Context, args ...string) (*exec.Cmd, func(), error) {
	chatID := fmt.Sprintf("aw-query-%d-%d", os.Getpid(), time.Now().UnixNano())
	cleanup := func() {
		_ = os.RemoveAll(paths.SessionDir(chatID))
		_ = os.RemoveAll(paths.SessionTmpDir(chatID))
	}
	runDir, err := setupSessionDir(chatID)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	bwrapArgs, err := buildSandboxArgs(nil, "", agentspec.CLITarget{}, true, runDir, "", chatID, "", "")
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	bwrapArgs = append(bwrapArgs, "aw")
	bwrapArgs = append(bwrapArgs, args...)
	return exec.CommandContext(ctx, "bwrap", bwrapArgs...), cleanup, nil
}

// RunQuery runs `aw <args...>` in a query sandbox and returns its stdout. It
// satisfies agentwrapper.QueryRunner.
func RunQuery(ctx context.Context, args ...string) ([]byte, error) {
	cmd, cleanup, err := CommandForQuery(ctx, args...)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running sandboxed aw %v: %w: %s", args, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// appendProxyArgs mounts the merged CA bundle and injects the protected proxy
// environment (and the custom proxy env vars) into a sandbox.
func appendProxyArgs(args []string, chatID string, proxyCfg ProxySandboxConfig) ([]string, error) {
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
			log.Debug().Str("env_key", envKey).Str("masked_value", proxy.MaskSecret(dummyVal)).Msg("injecting proxy env into command sandbox")
			args = append(args, "--setenv", envKey, dummyVal)
		}
	}
	return args, nil
}

// CommandForCommandExec creates an exec.Cmd initialized to run fakebashd inside a bubblewrap sandbox.
func CommandForCommandExec(runDir string, sockDir string, chatID string, configPath string, allowCrossSession bool, proxyOpts ...ProxySandboxConfig) (*exec.Cmd, error) {
	return CommandForCommandExecAt(runDir, "", sockDir, chatID, configPath, allowCrossSession, nil, proxyOpts...)
}

// SandboxCwd returns the path at which the host directory dir is visible
// inside a command sandbox of chatID: the chat's tmp and session dirs are
// mounted at /tmp and /session, anything else under the host runtime-data root
// (~/asgard/data) is masked unless allowCrossSession (an operator readonly
// mount beneath it is re-mounted after the mask), and every other
// directory keeps its path (CommandForCommandExecAt binds it there).
func SandboxCwd(chatID, dir string, allowCrossSession bool, readonlyDirs []string) (string, error) {
	dir = filepath.Clean(dir)
	for _, m := range []struct{ host, mount string }{
		{paths.SessionTmpDir(chatID), "/tmp"},
		{paths.SessionDir(chatID), "/session"},
	} {
		for _, base := range realAndLexical(m.host) {
			if rel, err := filepath.Rel(base, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
				return filepath.Join(m.mount, rel), nil
			}
		}
	}
	if !allowCrossSession && !beneathAny(readonlyDirs, dir) {
		for _, base := range realAndLexical(paths.DataDir()) {
			if rel, err := filepath.Rel(base, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
				return "", fmt.Errorf("directory %q is not visible in the sandbox (the runtime data directory is masked)", dir)
			}
		}
	}
	return dir, nil
}

// validateReadonlyRoots rejects operator readonly roots that cannot work in a
// sandbox: a root at or beneath a path the sandbox masks with a later tmpfs
// (it would be silently shadowed), or at or beneath the reserved /session or
// at /tmp itself (it would replace the chat's own bind).
func validateReadonlyRoots(readonlyDirs, masked []string) error {
	for _, d := range readonlyDirs {
		if !filepath.IsAbs(d) {
			continue // reported by appendReadonlyBinds
		}
		for _, c := range realAndLexical(d) {
			if c == "/tmp" || c == "/session" || strings.HasPrefix(c, "/session/") {
				return fmt.Errorf("mount readonly directory %q collides with the reserved sandbox mount point /tmp or /session", d)
			}
		}
		for _, m := range masked {
			if m != "" && beneathAny([]string{m}, d) {
				return fmt.Errorf("mount readonly directory %q lies inside %q, which the sandbox masks; it would be invisible", d, m)
			}
		}
	}
	return nil
}

// appendReadonlyBinds binds each dir read-only at its own path and,
// when it is a symlink, at its resolved path too: a process may reach the tree
// through either name and both must be read-only. A relative entry is an error
// (fail closed): it could not be mounted, leaving the tree writable.
func appendReadonlyBinds(args []string, dirs []string) ([]string, error) {
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("mount readonly directory must be an absolute path: %q", dir)
		}
		// A missing root has nothing to protect: skip it (loudly), the same
		// as agent sandboxes do.
		if _, err := os.Stat(dir); err != nil {
			log.Warn().Str("dir", dir).Msg("skipping missing readonly mount directory")
			continue
		}
		args = append(args, "--ro-bind", dir, dir)
		if ev, err := filepath.EvalSymlinks(dir); err == nil && ev != filepath.Clean(dir) {
			args = append(args, "--ro-bind", ev, ev)
		}
	}
	return args, nil
}

// appendBind binds dir read-write, or read-only when it is at or beneath one
// of readonlyDirs (compared by lexical and resolved path, so a symlink alias
// of a readonly tree cannot be mounted writable).
func appendBind(args []string, readonlyDirs []string, dir string) []string {
	if beneathAny(readonlyDirs, dir) {
		return append(args, "--ro-bind", dir, dir)
	}
	return append(args, "--bind", dir, dir)
}

// splitUnderDataRoot separates the dirs at or beneath the runtime-data root.
func splitUnderDataRoot(dirs []string) (other, under []string) {
	for _, d := range dirs {
		if filepath.IsAbs(d) && beneathAny([]string{paths.DataDir()}, d) {
			under = append(under, d)
		} else {
			other = append(other, d)
		}
	}
	return other, under
}

// beneathAny reports whether p is one of dirs or lies beneath one.
func beneathAny(dirs []string, p string) bool {
	p = filepath.Clean(p)
	cands := realAndLexical(p)
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			continue
		}
		for _, base := range realAndLexical(d) {
			for _, c := range cands {
				if rel, err := filepath.Rel(base, c); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
					return true
				}
			}
		}
	}
	return false
}

func realAndLexical(p string) []string {
	out := []string{filepath.Clean(p)}
	if ev, err := filepath.EvalSymlinks(p); err == nil && ev != out[0] {
		out = append(out, ev)
	}
	return out
}

// CommandForCommandExecAt is CommandForCommandExec with the sandbox working
// directory cwd given separately from the host directory runDir (empty cwd:
// runDir). A chat's tmp/session dir is visible as /tmp/<rel> or /session/<rel>
// (see SandboxCwd), not at its host path. readonlyDirs are the operator's
// readonly mount roots: like in agent sandboxes each existing one is bound
// read-only after the broader read-write binds (HOME, runDir), so they win, and
// a runDir beneath one is never bound read-write.
func CommandForCommandExecAt(runDir string, cwd string, sockDir string, chatID string, configPath string, allowCrossSession bool, readonlyDirs []string, proxyOpts ...ProxySandboxConfig) (*exec.Cmd, error) {
	if cwd == "" {
		cwd = runDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting user home directory: %w", err)
	}

	var args []string
	args, err = appendBaseSandboxArgs(args, home, chatID)
	if err != nil {
		return nil, err
	}

	masked := []string{filepath.Join(home, ".ssh"), paths.ConfigDir()}
	for _, cli := range agentwrapper.GetRegisteredCLIs() {
		if spec := agentwrapper.GetSandboxSpec(cli); spec != nil {
			masked = append(masked, spec.AuthDirectory(home))
		}
	}
	if err := validateReadonlyRoots(readonlyDirs, masked); err != nil {
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
				// Beneath a readonly root (also through a symlink alias) the
				// path is mounted read-only so it exists and cannot be written.
				args = appendBind(args, readonlyDirs, runDir)
			}
			args = append(args, "--chdir", cwd)
		} else {
			args = append(args, "--chdir", home)
		}
	} else {
		args = append(args, "--chdir", home)
	}

	// Bind HOME
	args = append(args, "--bind", home, home)

	// Readonly mounts come after the read-write binds above so they win.
	// Roots under the runtime-data root are re-mounted after the mask below
	// (unless the mask is off), so the operator's explicit mount stays
	// visible, read-only.
	var maskedRO []string
	normalRO := readonlyDirs
	if !allowCrossSession {
		normalRO, maskedRO = splitUnderDataRoot(readonlyDirs)
	}
	if args, err = appendReadonlyBinds(args, normalRO); err != nil {
		return nil, err
	}

	// Ignore auth dir for all registered CLIs, and ssh dir to prevent key leak
	for _, cli := range agentwrapper.GetRegisteredCLIs() {
		if spec := agentwrapper.GetSandboxSpec(cli); spec != nil {
			if authDir := spec.AuthDirectory(home); authDir != "" {
				if _, err := os.Stat(authDir); err == nil {
					args = append(args, "--tmpfs", authDir)
				}
			}
			for _, file := range spec.AuthFiles(home) {
				if fi, err := os.Stat(file); err == nil && !fi.IsDir() {
					args = append(args, "--ro-bind", "/dev/null", file)
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
		if args, err = appendReadonlyBinds(args, maskedRO); err != nil {
			return nil, err
		}
	}

	// If proxy is enabled for this sandbox, mount merged CA bundle and inject proxy env vars
	if len(proxyOpts) > 0 && proxyOpts[0].Enabled {
		args, err = appendProxyArgs(args, chatID, proxyOpts[0])
		if err != nil {
			return nil, err
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

// GitSandboxSpec describes one sandboxed git invocation (see GitSandboxArgs).
type GitSandboxSpec struct {
	ChatID string
	// WorkTreeSrc is the bind source of the repository: the runner passes
	// /proc/self/fd/3 for an inherited directory fd opened beneath an allowed
	// root, so nothing but that directory is reachable.
	WorkTreeSrc string
	// ReadWrite mounts the repository writable (pull); otherwise read-only.
	ReadWrite bool
	// Network keeps the network (push/pull) with the ssh-agent passthrough and
	// the masking proxy; otherwise the network namespace is unshared.
	Network bool
	// Env are extra KEY=VALUE variables; the environment is otherwise cleared.
	Env        []string
	ConfigPath string
	Proxy      ProxySandboxConfig
	// Git are the git arguments (after the git binary).
	Git []string
}

// GitRepoMount is where the repository appears inside the git sandbox.
const GitRepoMount = "/repo"

// GitSandboxArgs builds the bubblewrap arguments that run git on a single
// repository directory: fresh pid/ipc/uts/cgroup namespaces, die-with-parent,
// a cleared environment, no HOME contents besides the git config, the same
// credential and proxy-config masks as agent command sandboxes, and the
// network only when asked for.
func GitSandboxArgs(spec GitSandboxSpec) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting user home directory: %w", err)
	}
	args := []string{"--clearenv", "--new-session"}
	args, err = appendBaseSandboxArgs(args, home, spec.ChatID)
	if err != nil {
		return nil, err
	}
	if !spec.Network {
		args = append(args, "--unshare-net")
	}

	// HOME is an empty tmpfs with only the git configuration visible.
	args = append(args, "--tmpfs", home)
	for _, p := range []string{filepath.Join(home, ".gitconfig"), filepath.Join(home, ".config", "git")} {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		}
	}
	if spec.Network {
		args = appendSSHSandboxArgs(args, home)
	}

	bind := "--ro-bind"
	if spec.ReadWrite {
		bind = "--bind"
	}
	args = append(args, bind, spec.WorkTreeSrc, GitRepoMount, "--chdir", GitRepoMount)

	args = appendConfigMaskArgs(args, spec.ConfigPath)
	args = appendProxySensitiveMaskArgs(args, spec.Proxy.CAKey, spec.Proxy.ProxyConfigPath)
	args = appendCrossSessionMaskArgs(args)
	if spec.Network && spec.Proxy.Enabled {
		args, err = appendProxyArgs(args, spec.ChatID, spec.Proxy)
		if err != nil {
			return nil, err
		}
	}
	for _, kv := range spec.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			args = append(args, "--setenv", k, v)
		}
	}
	args = append(args, "--", "git")
	return append(args, spec.Git...), nil
}

// CommandForGit returns the bwrap command running git per spec. The caller
// passes the repository directory as the first extra file (fd 3 of bwrap).
func CommandForGit(ctx context.Context, spec GitSandboxSpec) (*exec.Cmd, error) {
	spec.WorkTreeSrc = "/proc/self/fd/3"
	args, err := GitSandboxArgs(spec)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, "bwrap", args...), nil
}
