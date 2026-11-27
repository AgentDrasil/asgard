package bwrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

// CommandForCommandExec creates an exec.Cmd initialized to run fakebashd inside a bubblewrap sandbox.
func CommandForCommandExec(runDir string, sockDir string, chatID string, configPath string, allowCrossSession bool, proxyOpts ...ProxySandboxConfig) (*exec.Cmd, error) {
	return CommandForCommandExecAt(runDir, "", sockDir, chatID, configPath, allowCrossSession, nil, proxyOpts...)
}

// CommandForAgentCommandExec creates an exec.Cmd initialized to run fakebashd inside a bubblewrap sandbox
// inheriting mounts, mask rules, and lock mode from the agent configuration.
func CommandForAgentCommandExec(cfg *agentspec.AgentConfig, runDir string, cwd string, sockDir string, chatID string, configPath string, allowCrossSession bool, proxyOpts ...ProxySandboxConfig) (*exec.Cmd, error) {
	spec := SandboxSpec{
		Cfg: cfg, RunDir: runDir, Cwd: cwd, SockDir: sockDir, ChatID: chatID,
		ConfigPath: configPath, AllowCrossSession: allowCrossSession,
		Proxy: firstProxy(proxyOpts),
	}
	return spec.cmdExecCmd()
}

// CommandForCommandExecAt is CommandForCommandExec with the sandbox working
// directory cwd given separately from the host directory runDir (empty cwd:
// runDir). A chat's tmp/session dir is visible as /tmp/<rel> or /session/<rel>
// (see SandboxCwd), not at its host path. readonlyDirs are the operator's
// readonly mount roots: like in agent sandboxes each existing one is bound
// read-only after the broader read-write binds (HOME, runDir), so they win, and
// a runDir beneath one is never bound read-write.
func CommandForCommandExecAt(runDir string, cwd string, sockDir string, chatID string, configPath string, allowCrossSession bool, readonlyDirs []string, proxyOpts ...ProxySandboxConfig) (*exec.Cmd, error) {
	spec := SandboxSpec{
		RunDir: runDir, Cwd: cwd, SockDir: sockDir, ChatID: chatID,
		ConfigPath: configPath, AllowCrossSession: allowCrossSession,
		ReadonlyDirs: readonlyDirs, Proxy: firstProxy(proxyOpts),
	}
	return spec.cmdExecCmd()
}

func commandForCommandExec(cfg *agentspec.AgentConfig, runDir string, cwd string, sockDir string, chatID string, configPath string, allowCrossSession bool, readonlyDirs []string, proxyOpts ...ProxySandboxConfig) (*exec.Cmd, error) {
	return SandboxSpec{
		Cfg: cfg, RunDir: runDir, Cwd: cwd, SockDir: sockDir, ChatID: chatID,
		ConfigPath: configPath, AllowCrossSession: allowCrossSession,
		ReadonlyDirs: readonlyDirs, Proxy: firstProxy(proxyOpts),
	}.cmdExecCmd()
}

func (s SandboxSpec) cmdExecCmd() (*exec.Cmd, error) {
	args, err := s.cmdExecArgs()
	if err != nil {
		return nil, err
	}
	return newBwrapCmd(context.Background(), args)
}

func (s SandboxSpec) cmdExecArgs() ([]string, error) {
	cwd := s.Cwd
	if cwd == "" {
		cwd = s.RunDir
	}
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

	sessionTmp := runnerpaths.SessionTmpDir(s.ChatID)
	sessionDir := runnerpaths.SessionDir(s.ChatID)
	provided := []string{home, sessionTmp, sessionDir}

	planner := newMountPlanner(s.Cfg, provided, false, s.ReadonlyDirs)

	masked := []string{filepath.Join(home, ".ssh"), paths.ConfigDir(), filepath.Join(home, ".config", "gcp-sa.json")}
	for _, cli := range agentwrapper.GetRegisteredCLIs() {
		if spec := agentwrapper.GetSandboxSpec(cli); spec != nil {
			masked = append(masked, spec.AuthDirectory(home))
		}
	}
	if err := validateReadonlyRoots(planner.readonly, masked); err != nil {
		return nil, err
	}

	if s.Cfg != nil && s.Cfg.IsReadOnly() {
		// Readonly mode: HOME is mounted read-only
		args = append(args, "--ro-bind", home, home)

		var seenRO map[string]bool
		args, seenRO, _, err = planner.bindReadonlyRunDirs(args)
		if err != nil {
			return nil, err
		}

		args = appendRunDirAndChdir(args, s.RunDir, cwd, home, func(dir string) []string {
			if !beneathAny(provided, dir) && !seenRO[filepath.Clean(dir)] {
				return append(args, "--ro-bind", dir, dir)
			}
			return args
		})
	} else if s.Cfg != nil {
		planner.planReadWrite()
		var mountedRWDirs []string
		args, mountedRWDirs, err = planner.bindReadWrite(args)
		if err != nil {
			return nil, err
		}

		args = appendRunDirAndChdir(args, s.RunDir, cwd, home, func(dir string) []string {
			if !beneathAny(mountedRWDirs, dir) {
				return appendBind(args, planner.readonly, dir)
			}
			return args
		})

		// Bind HOME
		args = append(args, "--bind", home, home)
	} else {
		// cfg == nil
		args = appendRunDirAndChdir(args, s.RunDir, cwd, home, func(dir string) []string {
			if !strings.HasPrefix(dir, home+string(filepath.Separator)) {
				return appendBind(args, planner.readonly, dir)
			}
			return args
		})

		// Bind HOME
		args = append(args, "--bind", home, home)
	}

	// If cross-session access is enabled and agent is not readonly, bind the cross-session data root.
	// In readonly mode, never bind the cross-session data root read-write.
	if s.AllowCrossSession && (s.Cfg == nil || !s.Cfg.IsReadOnly()) {
		args = appendCrossSessionBindArgs(args)
	} else {
		args = appendCrossSessionMaskArgs(args, home)
	}

	// Apply subdir mask rules after all RW mounts (including HOME and cross-session root)
	if s.Cfg != nil && !s.Cfg.IsReadOnly() {
		if args, err = planner.applyMaskRules(args, s.RunDir); err != nil {
			return nil, err
		}
	}

	// Readonly mounts come after the read-write binds above so they win.
	if args, err = planner.bindReadonlyLate(args); err != nil {
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

	// Credential masks after all directory mounts, so subsequent binds cannot
	// shadow the /dev/null masking (D1/R1/N2).
	args = appendCredentialMasks(args, home, s.ConfigPath, s.Proxy.CAKey)

	// If proxy is enabled for this sandbox, mount merged CA bundle and inject proxy env vars
	if s.Proxy.Enabled {
		args, err = appendProxyArgs(args, s.ChatID, s.Proxy)
		if err != nil {
			return nil, err
		}
	}

	args = appendSockDirArgs(args, s.SockDir)
	args = append(args, "--setenv", SandboxedEnv, "1", "--", "/bin/fakebashd")

	return args, nil
}

// appendRunDirAndChdir binds runDir (via bindFn) and sets --chdir to cwd when
// runDir exists on the host; otherwise it falls back to --chdir home.
func appendRunDirAndChdir(args []string, runDir, cwd, home string, bindFn func(dir string) []string) []string {
	if runDir != "" {
		if _, err := os.Stat(runDir); err == nil {
			args = bindFn(runDir)
			return append(args, "--chdir", cwd)
		}
	}
	return append(args, "--chdir", home)
}
