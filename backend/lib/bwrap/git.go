package bwrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// GitSandboxSpec describes one sandboxed git invocation (see GitSandboxArgs).
type GitSandboxSpec struct {
	ChatID string
	// WorkTreeSrc is the bind source of the repository: the runner passes
	// /proc/self/fd/4 for an inherited directory fd opened beneath an allowed
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
	if spec.Network {
		args = appendUpstreamProxyEnvArgs(args)
	}
	args = appendExtraEnvArgs(args, spec.Env)
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
	args = appendProxySensitiveMaskArgs(args, spec.Proxy.CAKey)
	if spec.Network && spec.Proxy.Enabled {
		args, err = appendProxyArgs(args, spec.ChatID, spec.Proxy)
		if err != nil {
			return nil, err
		}
	}
	args = append(args, "--", "git")
	return append(args, spec.Git...), nil
}

// CommandForGit returns the bwrap command running git per spec. The caller
// appends the repository directory to cmd.ExtraFiles (fd 4 of bwrap; fd 3 is
// the --args file, see newBwrapCmd).
func CommandForGit(ctx context.Context, spec GitSandboxSpec) (*exec.Cmd, error) {
	spec.WorkTreeSrc = "/proc/self/fd/4"
	args, err := GitSandboxArgs(spec)
	if err != nil {
		return nil, err
	}
	return newBwrapCmd(ctx, args)
}
