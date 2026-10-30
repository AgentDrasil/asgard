package workflow

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/AgentDrasil/asgard/backend/lib/cmdexec"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// commandRunner executes command nodes, optionally sandboxed via bubblewrap +
// the fakebashd gRPC socketpair protocol.
type commandRunner struct {
	// sandboxEnabled is the default sandbox setting; a node-level `sandbox`
	// flag overrides it.
	sandboxEnabled bool
	conf           *config.Config
}

// NewCommandRunner creates the runner for `command` nodes.
func NewCommandRunner(sandboxEnabled bool) NodeRunner {
	return &commandRunner{sandboxEnabled: sandboxEnabled}
}

// NewCommandRunnerWithConfig creates the runner for `command` nodes with a Config instance.
func NewCommandRunnerWithConfig(sandboxEnabled bool, conf *config.Config) NodeRunner {
	return &commandRunner{sandboxEnabled: sandboxEnabled, conf: conf}
}

func (r *commandRunner) Supports(t workflowspec.NodeType) bool {
	return t == workflowspec.NodeTypeCommand
}

func (r *commandRunner) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	sandbox := r.sandboxEnabled
	if nctx.Node.Sandbox != nil {
		sandbox = *nctx.Node.Sandbox
	}
	// A chat bound to a runner runs its commands there; only the host-local
	// layout (development and tests without a runner) executes them here.
	if cr, ok := ctx.Value(fsCtxKey{}).(runnerfs.CommandRunner); ok {
		return r.runOnRunner(ctx, nctx, cr, sandbox)
	}
	return r.runLocal(ctx, nctx, sandbox)
}

// runLocal executes the command on this host.
func (r *commandRunner) runLocal(ctx context.Context, nctx *NodeContext, sandbox bool) (*workflowspec.NodeResult, error) {
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

	ctx, cancel := withNodeTimeout(ctx, node)
	defer cancel()

	var stdout, stderr bytes.Buffer
	var exitCode int
	var err error
	if sandbox {
		spec := cmdexec.SandboxSpec{
			Command: command, Dir: workingDir, ChatID: nctx.SessionID, AllowCrossSession: nctx.AllowCrossSession,
		}
		if r.conf != nil {
			spec.ConfigPath = r.conf.GetConfigPath()
			spec.Proxy = r.conf.SandboxProxyOptions()
		}
		exitCode, err = cmdexec.RunSandboxed(ctx, spec, &stdout, &stderr)
	} else {
		exitCode, err = cmdexec.RunDirect(ctx, command, workingDir, nil, &stdout, &stderr)
	}

	result := &workflowspec.NodeResult{ExitCode: exitCode, Output: stdout.String()}
	collectArtifact(nctx, node, result)
	return finishCommand(node, result, err, stderr.String()), nil
}

// finishCommand sets the status of a command that ended with exitCode (or an
// execution error).
func finishCommand(node *workflowspec.NodeSpec, result *workflowspec.NodeResult, err error, stderr string) *workflowspec.NodeResult {
	if err != nil {
		result.Status = workflowspec.StatusFailed
		result.Error = err
		return result
	}
	// ExitCode always keeps the real process exit code (even on success) so
	// downstream `when: "nodes.<id>.exit_code == N"` edges match precisely.
	if !exitCodeAllowed(node, result.ExitCode) {
		result.Status = workflowspec.StatusFailed
		result.Error = fmt.Errorf("command exited with code %d: %s", result.ExitCode, truncate(stderr, 2000))
		return result
	}
	result.Status = workflowspec.StatusSucceeded
	return result
}

// commandDirs are the directories as the command sees them, which for a
// runner-bound chat are never backend host paths. The chat's default tmp and
// session dirs live at the runner's own location: a sandboxed command sees
// them as /tmp and /session; an unsandboxed one gets the runner's paths in
// $ASGARD_TMP_DIR and $ASGARD_SESSION_DIR. Dirs the workflow chose itself
// are kept.
func commandDirs(nctx *NodeContext, sandbox bool) (tmp, session string) {
	tmp, session = nctx.TmpDir, nctx.SessionDir
	if nctx.SessionID == "" {
		return
	}
	if tmp == DefaultTmpDir(nctx.SessionID) {
		tmp = "${ASGARD_TMP_DIR}"
		if sandbox {
			tmp = "/tmp"
		}
	}
	if session == DefaultSessionDir(nctx.SessionID) {
		session = "${ASGARD_SESSION_DIR}"
		if sandbox {
			session = "/session"
		}
	}
	return
}

// interpolateAs expands nctx's placeholders with the given tmp and session dir.
func interpolateAs(nctx *NodeContext, text, tmp, session string) string {
	c := *nctx
	c.TmpDir, c.SessionDir = tmp, session
	return c.Interpolate(text)
}

// runOnRunner executes the command on the chat's runner and maps the outcome
// onto a NodeResult exactly like a local run.
func (r *commandRunner) runOnRunner(ctx context.Context, nctx *NodeContext, cr runnerfs.CommandRunner, sandbox bool) (*workflowspec.NodeResult, error) {
	node := nctx.Node
	tmp, session := commandDirs(nctx, sandbox)
	command := interpolateAs(nctx, node.Command, tmp, session)
	// The working dir is resolved by the runner, which understands the chat's
	// namespaced /tmp and /session forms but not shell variables.
	wdTmp, wdSession := nctx.TmpDir, nctx.SessionDir
	if nctx.TmpDir == DefaultTmpDir(nctx.SessionID) {
		wdTmp = "/tmp"
	}
	if nctx.SessionDir == DefaultSessionDir(nctx.SessionID) {
		wdSession = "/session"
	}
	workingDir := interpolateAs(nctx, node.WorkingDir, wdTmp, wdSession)

	// The runner enforces the node timeout itself so that a timeout looks
	// like a killed process (exit -1), as it does locally; this context only
	// carries the cancellation and a safety margin against a hung runner.
	callCtx, cancel := ctx, context.CancelFunc(func() {})
	if d := node.TimeoutDuration(); d > 0 {
		callCtx, cancel = context.WithTimeout(ctx, d+remoteTimeoutMargin)
	}
	defer cancel()

	res, err := cr.RunCommand(callCtx, runnerfs.CommandSpec{
		Command: command, WorkingDir: workingDir, RunDir: nctx.RunDir, TmpDir: nctx.TmpDir,
		OutputFile: node.OutputFile, Sandbox: sandbox, AllowCrossSession: nctx.AllowCrossSession,
		Timeout: node.TimeoutDuration(),
	})
	result := &workflowspec.NodeResult{ExitCode: res.ExitCode, Output: string(res.Stdout)}
	if err != nil {
		result.ExitCode = -1
	}
	if res.StdoutTruncated {
		result.Output += StdoutTruncatedMarker
	}
	for _, a := range res.Artifacts {
		if result.Artifacts == nil {
			result.Artifacts = make(map[string]string)
		}
		// The viewer path is the key the file API accepts; a runner path
		// would mean nothing on the backend.
		result.Artifacts[a.Name] = a.ViewerPath
	}
	return finishCommand(node, result, err, string(res.Stderr)), nil
}

// StdoutTruncatedMarker is appended to the output of a runner command whose
// stdout hit the runner's cap.
const StdoutTruncatedMarker = "\n[asgard: stdout truncated]\n"

// remoteTimeoutMargin is how long past the node timeout the backend waits for
// the runner to report a timed out command.
const remoteTimeoutMargin = 30 * time.Second

// exitCodeAllowed reports whether the exit code counts as success: zero or a
// member of the node's allowed_exit_codes whitelist.
func exitCodeAllowed(node *workflowspec.NodeSpec, exitCode int) bool {
	if exitCode == 0 {
		return true
	}
	for _, code := range node.AllowedExitCodes {
		if exitCode == code {
			return true
		}
	}
	return false
}

// collectArtifact registers the node's declared output_file (relative to the
// artifacts dir) in the result if it was produced.
func collectArtifact(nctx *NodeContext, node *workflowspec.NodeSpec, result *workflowspec.NodeResult) {
	if node.OutputFile == "" {
		return
	}
	path := node.OutputFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(nctx.TmpDir, path)
	}
	if _, err := os.Stat(path); err != nil {
		return
	}
	if result.Artifacts == nil {
		result.Artifacts = make(map[string]string)
	}
	result.Artifacts[node.OutputFile] = path
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
