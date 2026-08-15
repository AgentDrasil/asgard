package workflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/AgentDrasil/asgard/lib/bwrap"
	"github.com/AgentDrasil/asgard/lib/fakebash/pb"
)

// commandRunner executes command nodes, optionally sandboxed via bubblewrap +
// the fakebashd gRPC socketpair protocol.
type commandRunner struct {
	// sandboxEnabled is the default sandbox setting; a node-level `sandbox`
	// flag overrides it.
	sandboxEnabled bool
}

// NewCommandRunner creates the runner for `command` nodes.
func NewCommandRunner(sandboxEnabled bool) NodeRunner {
	return &commandRunner{sandboxEnabled: sandboxEnabled}
}

func (r *commandRunner) Supports(t NodeType) bool {
	return t == NodeTypeCommand
}

func (r *commandRunner) Run(ctx context.Context, nctx *NodeContext) (*NodeResult, error) {
	node := nctx.Node
	command := nctx.Interpolate(node.Command)

	workingDir := nctx.Interpolate(node.WorkingDir)
	if workingDir == "" {
		workingDir = nctx.RunDir
	} else if !filepath.IsAbs(workingDir) {
		workingDir = filepath.Join(nctx.RunDir, workingDir)
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, fmt.Errorf("node %s: creating working dir: %w", node.ID, err)
	}

	sandbox := r.sandboxEnabled
	if node.Sandbox != nil {
		sandbox = *node.Sandbox
	}

	ctx, cancel := withNodeTimeout(ctx, node)
	defer cancel()

	var stdout, stderr bytes.Buffer
	var exitCode int
	var err error
	if sandbox {
		exitCode, err = runSandboxedCommand(ctx, command, workingDir, nctx.SessionID, &stdout, &stderr)
	} else {
		exitCode, err = runDirectCommand(ctx, command, workingDir, &stdout, &stderr)
	}

	result := &NodeResult{ExitCode: exitCode, Output: stdout.String()}
	collectArtifact(nctx, node, result)
	if err != nil {
		result.Status = StatusFailed
		result.Error = err
		return result, nil
	}
	if exitCode != 0 {
		result.Status = StatusFailed
		result.Error = fmt.Errorf("command exited with code %d: %s", exitCode, truncate(stderr.String(), 2000))
		return result, nil
	}
	result.Status = StatusSucceeded
	return result, nil
}

// collectArtifact registers the node's declared output_file (relative to the
// artifacts dir) in the result if it was produced.
func collectArtifact(nctx *NodeContext, node *NodeSpec, result *NodeResult) {
	if node.OutputFile == "" {
		return
	}
	path := node.OutputFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(nctx.ArtifactsDir, path)
	}
	if _, err := os.Stat(path); err != nil {
		return
	}
	if result.Artifacts == nil {
		result.Artifacts = make(map[string]string)
	}
	result.Artifacts[node.OutputFile] = path
}

// runDirectCommand runs the command with bash on the host.
func runDirectCommand(ctx context.Context, command, workingDir string, stdout, stderr *bytes.Buffer) (int, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = workingDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, fmt.Errorf("running command: %w", err)
	}
	return 0, nil
}

func asExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if ok {
		*target = exitErr
	}
	return ok
}

// runSandboxedCommand runs the command inside a bubblewrap sandbox hosting a
// fakebashd gRPC daemon, mirroring the dual-sandbox execution model used by
// agent runs: the host dials the socket directory bind-mounted at /fakebash.
func runSandboxedCommand(ctx context.Context, command, runDir, chatID string, stdout, stderr *bytes.Buffer) (int, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return -1, fmt.Errorf("getting user home directory: %w", err)
	}
	sockDir := filepath.Join(home, "tmp", "fakebash-sock-"+uuid.Must(uuid.NewV7()).String())
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		return -1, fmt.Errorf("creating sock directory %q: %w", sockDir, err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()

	sandboxCmd, err := bwrap.CommandForCommandExec(runDir, sockDir, chatID)
	if err != nil {
		return -1, fmt.Errorf("creating command exec sandbox: %w", err)
	}
	sandboxCmd.Env = append(os.Environ(), "ASGARD_CHAT_ID="+chatID)
	sandboxCmd.Stdout = os.Stdout
	sandboxCmd.Stderr = os.Stderr

	if err := sandboxCmd.Start(); err != nil {
		return -1, fmt.Errorf("starting command exec sandbox: %w", err)
	}
	defer func() {
		if sandboxCmd.Process != nil {
			_ = sandboxCmd.Process.Kill()
			_, _ = sandboxCmd.Process.Wait()
		}
	}()

	conn, err := dialFakebashSocket(ctx, filepath.Join(sockDir, "fakebash.sock"))
	if err != nil {
		return -1, err
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewFakebashServiceClient(conn)
	stream, err := client.RunCommand(ctx, &pb.CommandRequest{
		Args: []string{"-c", command},
		Cwd:  runDir,
		Env:  os.Environ(),
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
			stdout.Write(resp.Payload)
		case pb.CommandResponse_STDERR:
			stderr.Write(resp.Payload)
		case pb.CommandResponse_EXIT:
			if len(resp.Payload) > 0 {
				exitCode, _ = strconv.Atoi(string(resp.Payload))
			}
		}
	}
	return exitCode, nil
}

// dialFakebashSocket waits for the sandbox daemon's unix socket to accept
// connections (it may still be booting) and returns a gRPC client connection.
func dialFakebashSocket(ctx context.Context, socketPath string) (*grpc.ClientConn, error) {
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
