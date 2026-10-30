// Package cmdexec runs a shell command either directly with bash (process
// group kill on cancel) or inside the bubblewrap sandbox that hosts the
// fakebashd gRPC daemon, the dual-sandbox model of agent runs. It is shared by
// the backend's runner-less command nodes and by the runner's RunCommand.
package cmdexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	"uuid"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/AgentDrasil/asgard/backend/lib/bwrap"
	"github.com/AgentDrasil/asgard/fakebash/pb"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

// SandboxSpec describes one sandboxed command.
type SandboxSpec struct {
	Command string
	// Dir is the working directory on the host (bound into the sandbox by
	// bwrap when it lies outside HOME).
	Dir string
	// Cwd is the directory as the sandbox sees it; empty means Dir. A chat's
	// tmp/session dir is mounted at /tmp and /session (see bwrap.SandboxCwd).
	Cwd string
	// ReadonlyDirs are the operator's readonly mount roots, bound read-only.
	ReadonlyDirs []string
	ChatID       string
	// ConfigPath and Proxy mask the asgard config and credentials.
	ConfigPath        string
	Proxy             bwrap.ProxySandboxConfig
	AllowCrossSession bool
	// Env is the environment of the sandboxed command; nil means the process
	// environment.
	Env []string
}

// RunDirect runs command with bash in dir. The command gets its own process
// group which is killed as a whole on cancel: killing only bash would leave
// its children (e.g. `sleep 60`) holding the output pipes. A nil env means
// the process environment.
func RunDirect(ctx context.Context, command, dir string, env []string, stdout, stderr io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return -1, fmt.Errorf("running command: %w", err)
	}
	return 0, nil
}

// RunSandboxed runs the command inside a bubblewrap sandbox hosting a
// fakebashd gRPC daemon: the host dials the socket directory bind-mounted at
// /fakebash.
func RunSandboxed(ctx context.Context, spec SandboxSpec, stdout, stderr io.Writer) (int, error) {
	sockDir := runnerpaths.SockDir("fakebash-sock-" + uuid.NewV7().String())
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		return -1, fmt.Errorf("creating sock directory %q: %w", sockDir, err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()

	sandboxCmd, err := bwrap.CommandForCommandExecAt(spec.Dir, spec.Cwd, sockDir, spec.ChatID, spec.ConfigPath, spec.AllowCrossSession, spec.ReadonlyDirs, spec.Proxy)
	if err != nil {
		return -1, fmt.Errorf("creating command exec sandbox: %w", err)
	}
	env := spec.Env
	if env == nil {
		env = os.Environ()
	}
	cwd := spec.Cwd
	if cwd == "" {
		cwd = spec.Dir
	}
	sandboxCmd.Env = append(append([]string(nil), env...), "ASGARD_CHAT_ID="+spec.ChatID)
	sandboxCmd.Stdout = os.Stdout
	sandboxCmd.Stderr = os.Stderr
	sandboxCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := sandboxCmd.Start(); err != nil {
		return -1, fmt.Errorf("starting command exec sandbox: %w", err)
	}
	defer func() {
		// Kill the sandbox's whole process group: bwrap's pid namespace dies
		// with it, taking the command along.
		_ = syscall.Kill(-sandboxCmd.Process.Pid, syscall.SIGKILL)
		_ = sandboxCmd.Process.Kill()
		_, _ = sandboxCmd.Process.Wait()
	}()

	conn, err := dialSocket(ctx, filepath.Join(sockDir, "fakebash.sock"))
	if err != nil {
		return -1, err
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewFakebashServiceClient(conn)
	stream, err := client.RunCommand(ctx, &pb.CommandRequest{
		Args: []string{"-c", spec.Command},
		Cwd:  cwd,
		Env:  env,
	})
	if err != nil {
		return -1, fmt.Errorf("run command stream error: %w", err)
	}

	exitCode := 0
	for {
		resp, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return -1, fmt.Errorf("stream recv error: %w", err)
		}
		switch resp.Type {
		case pb.CommandResponse_STDOUT:
			if _, err := stdout.Write(resp.Payload); err != nil {
				return -1, fmt.Errorf("writing stdout: %w", err)
			}
		case pb.CommandResponse_STDERR:
			if _, err := stderr.Write(resp.Payload); err != nil {
				return -1, fmt.Errorf("writing stderr: %w", err)
			}
		case pb.CommandResponse_EXIT:
			if len(resp.Payload) > 0 {
				exitCode, err = strconv.Atoi(string(resp.Payload))
				if err != nil {
					return -1, fmt.Errorf("malformed exit code payload %q: %w", string(resp.Payload), err)
				}
			}
		}
	}
	return exitCode, nil
}

// dialSocket waits for the sandbox daemon's unix socket to accept connections
// (it may still be booting) and returns a gRPC client connection.
func dialSocket(ctx context.Context, socketPath string) (*grpc.ClientConn, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	for {
		if conn, err := net.DialTimeout("unix", socketPath, time.Second); err == nil {
			_ = conn.Close()
			break
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("fakebash socket %q not ready: %w", socketPath, waitCtx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}

	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("grpc dial %q: %w", socketPath, err)
	}
	return conn, nil
}
