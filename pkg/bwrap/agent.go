package bwrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

// CommandForAgent creates an exec.Cmd initialized to run the target CLI inside bubblewrap sandbox.
func CommandForAgent(cfg *agentspec.AgentConfig, agentPath string, target agentspec.CLITarget, prompt string, session optional.Option[string], runDir string, sockDir string, chatID string, langRules string, configPath string, proxyOpts ...ProxySandboxConfig) (*exec.Cmd, error) {
	bwrapArgs, err := buildArgsForAgent(cfg, agentPath, target, prompt, session, runDir, sockDir, chatID, langRules, configPath, proxyOpts...)
	if err != nil {
		return nil, err
	}
	return newBwrapCmd(context.Background(), bwrapArgs)
}

func buildArgsForAgent(cfg *agentspec.AgentConfig, agentPath string, target agentspec.CLITarget, prompt string, session optional.Option[string], runDir string, sockDir string, chatID string, langRules string, configPath string, proxyOpts ...ProxySandboxConfig) ([]string, error) {
	sess := ""
	if session.IsSome() {
		sess = session.Unwrap()
	}
	return SandboxSpec{
		Cfg: cfg, AgentPath: agentPath, Target: target, Session: sess,
		RunDir: runDir, SockDir: sockDir, ChatID: chatID, LangRules: langRules,
		ConfigPath: configPath, Proxy: firstProxy(proxyOpts),
	}.agentArgv(prompt, session)
}

func buildSandboxArgs(cfg *agentspec.AgentConfig, agentPath string, target agentspec.CLITarget, allCLIs bool, session string, runDir string, sockDir string, chatID string, langRules string, configPath string, proxyOpts ...ProxySandboxConfig) ([]string, error) {
	return buildSandboxArgsPinned(nil, cfg, agentPath, target, allCLIs, session, runDir, sockDir, chatID, langRules, configPath, proxyOpts...)
}

func buildSandboxArgsPinned(pins map[string]string, cfg *agentspec.AgentConfig, agentPath string, target agentspec.CLITarget, allCLIs bool, session string, runDir string, sockDir string, chatID string, langRules string, configPath string, proxyOpts ...ProxySandboxConfig) ([]string, error) {
	return SandboxSpec{
		Cfg: cfg, AgentPath: agentPath, Target: target, AllCLIs: allCLIs,
		Pins: pins, Session: session, RunDir: runDir, SockDir: sockDir,
		ChatID: chatID, LangRules: langRules, ConfigPath: configPath,
		Proxy: firstProxy(proxyOpts),
	}.agentArgs()
}

// agentArgv is the full bwrap argument list of an agent run: the sandbox
// arguments followed by the `aw <cli> ...` command. The session option keeps
// the distinction between "no session" and an empty one (only a non-empty
// session is passed on).
func (s SandboxSpec) agentArgv(prompt string, session optional.Option[string]) ([]string, error) {
	args, err := s.agentArgs()
	if err != nil {
		return nil, err
	}

	// Target executable and its arguments
	args = append(args, "aw")
	args = append(args, s.Target.CLI)
	args = append(args, "--model", s.Target.Model)
	if spec := agentwrapper.GetSandboxSpec(s.Target.CLI); spec != nil {
		args = append(args, spec.ExtraArgs()...)
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

// agentArgs constructs the bubblewrap arguments of the agent sandbox (and,
// with AllCLIs, the metadata-query sandbox) up to and including the "--"
// separator, i.e. everything except the command run inside the sandbox.
func (s SandboxSpec) agentArgs() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting user home directory: %w", err)
	}

	// Start from an empty environment; see env.go for what is set afterwards.
	args := []string{"--clearenv"}
	args, err = appendBaseSandboxArgs(args, home, s.ChatID)
	if err != nil {
		return nil, err
	}
	args = appendInheritedEnvArgs(args)
	args = appendExtraEnvArgs(args, s.Proxy.ExtraEnv)
	if args, err = appendSharedArgs(args, s.Cfg); err != nil {
		return nil, err
	}

	// Every read-write bind below is demoted to read-only when its path is at
	// or beneath a readonly mount root (e.g. through a symlink alias).
	planner := newMountPlanner(s.Cfg, nil, true, nil)
	if err := validateReadonlyRoots(planner.readonly, []string{filepath.Join(home, ".ssh"), paths.ConfigDir()}); err != nil {
		return nil, err
	}

	// Verify run directory exists
	if _, err := os.Stat(s.RunDir); err != nil {
		return nil, fmt.Errorf("run directory %q does not exist: %w", s.RunDir, err)
	}

	if s.Cfg != nil && s.Cfg.IsReadOnly() {
		var roDirs []string
		args, _, roDirs, err = planner.bindReadonlyRunDirs(args)
		if err != nil {
			return nil, err
		}
		sessionTmp := runnerpaths.SessionTmpDir(s.ChatID)
		sessionDir := runnerpaths.SessionDir(s.ChatID)
		if !beneathAny([]string{sessionTmp, sessionDir}, s.RunDir) && !beneathAny(roDirs, s.RunDir) {
			args = append(args, "--ro-bind", s.RunDir, s.RunDir)
		}
	} else {
		var mountedRWDirs []string
		planner.planReadWrite()
		if s.Cfg != nil {
			args, mountedRWDirs, err = planner.bindReadWrite(args)
			if err != nil {
				return nil, err
			}
		}
		if !beneathAny(mountedRWDirs, s.RunDir) {
			args = appendBind(args, planner.readonly, s.RunDir)
		}
		// Apply subdir mask rules after all RW mounts (including runDir) are bound
		if args, err = planner.applyMaskRules(args, s.RunDir); err != nil {
			return nil, err
		}
	}

	// Bind logs directory
	logDir := runnerpaths.LogsDir()
	if err := os.MkdirAll(logDir, 0755); err == nil {
		args = append(args, "--bind", logDir, logDir)
	}

	// Mount the socket directory to /fakebash
	args = appendSockDirArgs(args, s.SockDir)

	// bwrap cannot mount onto a symlink destination (e.g. /usr/bin/sh -> dash
	// on Debian), so bind fakebash onto the resolved targets instead.
	for _, dest := range resolveBindDests("/bin/bash", "/usr/bin/bash", "/bin/sh", "/usr/bin/sh") {
		args = append(args, "--ro-bind", "/bin/fakebash", dest)
	}

	// Mask fish so it is not usable inside the sandbox (only if present on host).
	for _, p := range resolveBindDests("/usr/bin/fish", "/bin/fish") {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", "/dev/null", p)
		}
	}

	// Change working directory to runDir in the sandbox
	args = append(args, "--chdir", s.RunDir)

	args = append(args, "--setenv", SandboxedEnv, "1")
	if s.ChatID != "" {
		args = append(args, "--setenv", "ASGARD_CHAT_ID", s.ChatID)
	}
	// ASGARD_MODEL identifies the selected CLI target's model inside the
	// sandbox so aw status reports and ask-user questions can be attributed
	// to it by the host.
	if s.Target.Model != "" {
		args = append(args, "--setenv", "ASGARD_MODEL", s.Target.Model)
	}

	// The jev and Gemini keys of fakebash's output compression are never
	// passed in: the runner makes those calls over the per-run socket.

	cliSpec := agentwrapper.GetSandboxSpec(s.Target.CLI)

	var envs map[string]string
	if args, envs, err = s.appendCLIMounts(args, home, cliSpec, planner.readonly); err != nil {
		return nil, err
	}
	// Readonly mounts last among the directory binds, so they win over any
	// read-write bind of an ancestor (run_dirs, mount_dirs.readwrite, CLI dirs).
	if args, err = planner.bindReadonlyLate(args); err != nil {
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

	if s.AllCLIs {
		args = appendQueryConfigArgs(args)
	}

	// Credential masks after all directory mounts, so subsequent binds cannot
	// shadow the /dev/null masking (D1/R1/N2).
	args = appendCredentialMasks(args, home, s.ConfigPath, s.Proxy.CAKey)

	// Inject proxy environment variables into agent sandbox
	if len(s.Proxy.EnvVars) > 0 {
		keys := make([]string, 0, len(s.Proxy.EnvVars))
		for k := range s.Proxy.EnvVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.TrimSpace(k) != "" {
				args = append(args, "--setenv", k, s.Proxy.EnvVars[k])
			}
		}
	}

	if args, err = s.appendContractArgs(args, home, cliSpec); err != nil {
		return nil, err
	}

	// End of bubblewrap arguments
	args = append(args, "--")

	return args, nil
}

// appendSockDirArgs mounts the per-run socket directory at /fakebash.
func appendSockDirArgs(args []string, sockDir string) []string {
	if sockDir != "" {
		args = append(args, "--dir", "/fakebash")
		args = append(args, "--bind", sockDir, "/fakebash")
	}
	return args
}

// appendCLIMounts adds the target-specific mounts: the auth/config
// directories of the selected CLI (the metadata-query sandbox needs those of
// every registered CLI) and the selected agy/claude account overlays. It
// returns the extra environment of the CLIs, to be set by the caller.
func (s SandboxSpec) appendCLIMounts(args []string, home string, cliSpec types.SandboxSpec, readonlyDirs []string) ([]string, map[string]string, error) {
	specs := []types.SandboxSpec{}
	if s.AllCLIs {
		clis := agentwrapper.GetRegisteredCLIs()
		sort.Strings(clis)
		for _, cli := range clis {
			if sp := agentwrapper.GetSandboxSpec(cli); sp != nil {
				specs = append(specs, sp)
			}
		}
	} else if cliSpec != nil {
		specs = append(specs, cliSpec)
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
	// Overlay the selected agy account's token over the host ~/.gemini token.
	// agy refreshes the token in place, so a file bind persists refreshes.
	if s.AllCLIs || s.Target.CLI == "agy" {
		sel, err := accountForSandbox("agy", s.Target.Model, s.AllCLIs, s.Pins)
		if err != nil {
			return nil, nil, err
		}
		setProfileEnv(envs, "agy", sel, s.AllCLIs)
		if args, err = appendAgyAccountToken(args, home, sel); err != nil {
			return nil, nil, err
		}
	}
	// Overlay the selected claude account dir over the host ~/.claude. It must
	// follow the host bind above; later binds under ~/.claude (skills) come after.
	if s.AllCLIs || s.Target.CLI == "claude" {
		sel, err := accountForSandbox("claude", s.Target.Model, s.AllCLIs, s.Pins)
		if err != nil {
			return nil, nil, err
		}
		setProfileEnv(envs, "claude", sel, s.AllCLIs)
		if args, err = appendClaudeAccountDir(args, home, sel, s.Session); err != nil {
			return nil, nil, err
		}
	}
	return args, envs, nil
}

// appendContractArgs builds and mounts the AW_AGENTS.md contract, and mounts
// skills/ if present in AgentPath. The contract is written to the chat tmpDir
// on the host and bind-mounted read-only at the single canonical sandbox path
// /session/AW_AGENTS.md (also exported as $AW_AGENTS_PATH) regardless of the
// target CLI. The aw dispatcher inside the sandbox translates the contract for
// each CLI, so this layer never addresses CLI-specific configuration paths.
func (s SandboxSpec) appendContractArgs(args []string, home string, cliSpec types.SandboxSpec) ([]string, error) {
	var agentsMDPath string
	var skillsPath string
	if s.AgentPath != "" {
		candidate := filepath.Join(s.AgentPath, "AGENTS.md")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			agentsMDPath = candidate
		}
		skillsPath = filepath.Join(s.AgentPath, "skills")
		if st, err := os.Stat(skillsPath); err != nil || !st.IsDir() {
			skillsPath = ""
		}
	}

	// Determine the host dir where we can write the contract file (already mounted as /tmp).
	promptHostDir, err := setupTmpDir(s.ChatID)
	if err != nil {
		return nil, err
	}

	contractFile, err := writeContractFile(promptHostDir, s.Cfg, agentsMDPath, s.LangRules)
	if err != nil {
		return nil, err
	}
	args = append(args, "--setenv", common.EnvVar, common.DefaultPath)
	if contractFile != "" {
		args = append(args, "--ro-bind", contractFile, common.DefaultPath)
	}
	if cliSpec != nil && skillsPath != "" {
		if dest := cliSpec.SkillsMountPath(home); dest != "" {
			args = append(args, "--ro-bind", skillsPath, dest)
		}
	}
	return args, nil
}
