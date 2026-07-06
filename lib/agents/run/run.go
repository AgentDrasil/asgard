package run

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/agentwrapper"
	"github.com/AgentDrasil/asgard/lib/bwrap"
)

func isAllowedDir(path string, allowedDirs []string) bool {
	path = filepath.Clean(path)
	for _, dir := range allowedDirs {
		dir = filepath.Clean(dir)
		if path == dir {
			return true
		}
		// Check if it is a subdirectory
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Run checks the remaining quota for each CLI target configured on the agent.
// It runs the bubblewrap command for the first target that has more than 20% quota remaining.
// If no targets have more than 20% quota remaining, it returns an error.
func Run(ctx context.Context, agent *agents.Agent, prompt string, session optional.Option[string], runDirOpt optional.Option[string]) ([]byte, error) {
	if len(agent.Config.CLI) == 0 {
		return nil, fmt.Errorf("no CLI targets configured for agent %s", agent.Config.ID)
	}

	var selectedTarget *agents.CLITarget
	for _, target := range agent.Config.CLI {
		quota := agentwrapper.CheckQuota(target.CLI, target.Model)
		if quota > 0.20 {
			selectedTarget = &target
			break
		}
	}

	if selectedTarget == nil {
		return nil, fmt.Errorf("no CLI target with more than 20%% quota remaining is available for agent %s", agent.Config.ID)
	}

	var runDir string
	if runDirOpt.IsSome() && runDirOpt.Unwrap() != "" {
		rd := runDirOpt.Unwrap()
		if !isAllowedDir(rd, agent.Config.RunDirs) {
			return nil, fmt.Errorf("run directory %q is not allowed by agent configuration", rd)
		}
		runDir = rd
	} else if len(agent.Config.RunDirs) > 0 && agent.Config.RunDirs[0] != "" {
		runDir = agent.Config.RunDirs[0]
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("getting user home directory: %w", err)
		}
		tmpDir := filepath.Join(home, "tmp")
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			return nil, fmt.Errorf("creating tmp directory %q: %w", tmpDir, err)
		}
		uuidDir := filepath.Join(tmpDir, uuid.NewString())
		if err := os.MkdirAll(uuidDir, 0755); err != nil {
			return nil, fmt.Errorf("creating uuid run directory %q: %w", uuidDir, err)
		}
		runDir = uuidDir
	}

	// Ensure the resolved runDir exists (e.g. if it was a subdirectory under config run_dirs that was not created yet)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("creating run directory %q: %w", runDir, err)
	}

	// Create Socketpair for communicating between agent sandbox and command sandbox
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("creating socketpair: %w", err)
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	agentSocket := os.NewFile(uintptr(fds[0]), "agent-socket")
	commandSocket := os.NewFile(uintptr(fds[1]), "command-socket")
	defer func() { _ = agentSocket.Close() }()
	defer func() { _ = commandSocket.Close() }()

	agentSandboxCmd, err := bwrap.CommandForAgent(&agent.Config, *selectedTarget, prompt, session, runDir, agentSocket)
	if err != nil {
		return nil, fmt.Errorf("creating command for agent: %w", err)
	}

	// Start the command execution sandbox
	cmdSandboxCmd, err := bwrap.CommandForCommandExec(runDir, commandSocket)
	if err != nil {
		return nil, fmt.Errorf("creating command for command exec: %w", err)
	}

	cmdSandboxCmd.Stdout = os.Stdout
	cmdSandboxCmd.Stderr = os.Stderr

	if err := cmdSandboxCmd.Start(); err != nil {
		return nil, fmt.Errorf("starting command execution sandbox: %w", err)
	}

	var stdoutBuf bytes.Buffer
	agentSandboxCmd.Stdout = &stdoutBuf
	agentSandboxCmd.Stderr = os.Stderr

	if err := agentSandboxCmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agent sandbox command: %w", err)
	}

	// Close parent's references to the sockets so they are only held by the child sandboxes.
	// This ensures fakebashd gets io.EOF and exits cleanly when the agent wrapper terminates.
	_ = agentSocket.Close()
	_ = commandSocket.Close()

	defer func() {
		if cmdSandboxCmd.Process != nil {
			_, _ = cmdSandboxCmd.Process.Wait()
		}
	}()

	if ctx.Done() != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				if agentSandboxCmd.Process != nil {
					_ = agentSandboxCmd.Process.Kill()
				}
			case <-done:
			}
		}()
	}

	err = agentSandboxCmd.Wait()
	out := stdoutBuf.Bytes()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return out, fmt.Errorf("running agent sandbox command: %w (output: %q)", err, string(out))
	}

	return out, nil
}
