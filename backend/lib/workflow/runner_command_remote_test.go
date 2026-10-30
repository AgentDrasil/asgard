package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// fakeCommandFS is a chat FS whose commands run on a (fake) runner.
type fakeCommandFS struct {
	runnerfs.FS
	got []runnerfs.CommandSpec
	do  func(ctx context.Context, spec runnerfs.CommandSpec) (runnerfs.CommandResult, error)
}

func (f *fakeCommandFS) RunCommand(ctx context.Context, spec runnerfs.CommandSpec) (runnerfs.CommandResult, error) {
	f.got = append(f.got, spec)
	return f.do(ctx, spec)
}

func runRemoteCommand(t *testing.T, ctx context.Context, fs *fakeCommandFS, runner NodeRunner, node *workflowspec.NodeSpec, mod func(*NodeContext)) *workflowspec.NodeResult {
	t.Helper()
	const sid = "sess-remote"
	nctx := &NodeContext{
		SessionID:  sid,
		RunDir:     "/runner/ws",
		TmpDir:     DefaultTmpDir(sid),
		SessionDir: DefaultSessionDir(sid),
		Input:      "in",
		Node:       node,
		Defn:       &workflowspec.WorkflowDefinition{Name: "remote", Nodes: []*workflowspec.NodeSpec{node}},
	}
	if mod != nil {
		mod(nctx)
	}
	res, err := runner.Run(withFS(ctx, fs), nctx)
	require.NoError(t, err)
	require.NotNil(t, res)
	return res
}

func okCommand(stdout, stderr string, code int) func(context.Context, runnerfs.CommandSpec) (runnerfs.CommandResult, error) {
	return func(context.Context, runnerfs.CommandSpec) (runnerfs.CommandResult, error) {
		return runnerfs.CommandResult{ExitCode: code, Stdout: []byte(stdout), Stderr: []byte(stderr)}, nil
	}
}

func TestCommandRunnerOnRunnerMapsResults(t *testing.T) {
	node := &workflowspec.NodeSpec{ID: "n", Type: workflowspec.NodeTypeCommand, Command: "c"}

	t.Run("success", func(t *testing.T) {
		fs := &fakeCommandFS{do: okCommand("out\n", "", 0)}
		res := runRemoteCommand(t, t.Context(), fs, NewCommandRunner(true), node, nil)
		assert.Equal(t, workflowspec.StatusSucceeded, res.Status)
		assert.Equal(t, 0, res.ExitCode)
		assert.Equal(t, "out\n", res.Output)
		assert.NoError(t, res.Error)
	})

	t.Run("non-zero exit fails with the stderr tail and keeps the code", func(t *testing.T) {
		fs := &fakeCommandFS{do: okCommand("o", "boom", 4)}
		res := runRemoteCommand(t, t.Context(), fs, NewCommandRunner(true), node, nil)
		assert.Equal(t, workflowspec.StatusFailed, res.Status)
		assert.Equal(t, 4, res.ExitCode)
		assert.Equal(t, "o", res.Output)
		require.Error(t, res.Error)
		assert.Equal(t, "command exited with code 4: boom", res.Error.Error())
	})

	t.Run("allowed_exit_codes", func(t *testing.T) {
		allowed := &workflowspec.NodeSpec{ID: "n", Type: workflowspec.NodeTypeCommand, Command: "c", AllowedExitCodes: []int{0, 1}}
		res := runRemoteCommand(t, t.Context(), &fakeCommandFS{do: okCommand("", "", 1)}, NewCommandRunner(true), allowed, nil)
		assert.Equal(t, workflowspec.StatusSucceeded, res.Status)
		assert.Equal(t, 1, res.ExitCode, "success keeps the real exit code")
		res = runRemoteCommand(t, t.Context(), &fakeCommandFS{do: okCommand("", "x", 2)}, NewCommandRunner(true), allowed, nil)
		assert.Equal(t, workflowspec.StatusFailed, res.Status)
		assert.Equal(t, 2, res.ExitCode)
	})

	t.Run("artifacts map name to the viewer path", func(t *testing.T) {
		withOut := &workflowspec.NodeSpec{ID: "n", Type: workflowspec.NodeTypeCommand, Command: "c", OutputFile: "r/o.txt"}
		fs := &fakeCommandFS{do: func(context.Context, runnerfs.CommandSpec) (runnerfs.CommandResult, error) {
			return runnerfs.CommandResult{Artifacts: []runnerfs.CommandArtifact{{Name: "r/o.txt", ViewerPath: "/tmp/r/o.txt", AbsPath: "/runner/home/tmp/r/o.txt"}}}, nil
		}}
		res := runRemoteCommand(t, t.Context(), fs, NewCommandRunner(true), withOut, nil)
		assert.Equal(t, map[string]string{"r/o.txt": "/tmp/r/o.txt"}, res.Artifacts)
		assert.Equal(t, []string{"/tmp/r/o.txt"}, ArtifactViewerPathsInSession(res.Artifacts, DefaultTmpDir("sess-remote"), DefaultSessionDir("sess-remote")))
		assert.Equal(t, "r/o.txt", fs.got[0].OutputFile)

		fs = &fakeCommandFS{do: okCommand("", "", 0)}
		res = runRemoteCommand(t, t.Context(), fs, NewCommandRunner(true), withOut, nil)
		assert.Empty(t, res.Artifacts)
	})

	t.Run("an execution error fails the node", func(t *testing.T) {
		boom := errors.New("sandbox not ready: proxy is not listening")
		fs := &fakeCommandFS{do: func(context.Context, runnerfs.CommandSpec) (runnerfs.CommandResult, error) {
			return runnerfs.CommandResult{Stdout: []byte("partial")}, boom
		}}
		res := runRemoteCommand(t, t.Context(), fs, NewCommandRunner(true), node, nil)
		assert.Equal(t, workflowspec.StatusFailed, res.Status)
		assert.Equal(t, -1, res.ExitCode)
		assert.Equal(t, "partial", res.Output)
		assert.ErrorIs(t, res.Error, boom)
	})

	t.Run("cancellation fails the node with the context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		fs := &fakeCommandFS{do: func(ctx context.Context, _ runnerfs.CommandSpec) (runnerfs.CommandResult, error) {
			cancel()
			<-ctx.Done()
			return runnerfs.CommandResult{}, ctx.Err()
		}}
		res := runRemoteCommand(t, ctx, fs, NewCommandRunner(true), node, nil)
		assert.Equal(t, workflowspec.StatusFailed, res.Status)
		assert.ErrorIs(t, res.Error, context.Canceled)
	})
}

func TestCommandRunnerOnRunnerSpec(t *testing.T) {
	sandboxOff := false
	node := &workflowspec.NodeSpec{
		ID: "n", Type: workflowspec.NodeTypeCommand, Timeout: "90s", WorkingDir: "${tmp_dir}/work",
		Command: "cp ${tmp_dir}/a ${session_dir}/b && cd ${run_dir} && echo ${input}",
	}

	t.Run("sandboxed commands see the sandbox mounts, never backend host paths", func(t *testing.T) {
		fs := &fakeCommandFS{do: okCommand("", "", 0)}
		runRemoteCommand(t, t.Context(), fs, NewCommandRunner(true), node, func(n *NodeContext) { n.AllowCrossSession = true })
		require.Len(t, fs.got, 1)
		spec := fs.got[0]
		assert.Equal(t, "cp /tmp/a /session/b && cd /runner/ws && echo in", spec.Command)
		assert.Equal(t, "/tmp/work", spec.WorkingDir)
		assert.True(t, spec.Sandbox)
		assert.True(t, spec.AllowCrossSession)
		assert.Equal(t, 90*time.Second, spec.Timeout)
		assert.Equal(t, "/runner/ws", spec.RunDir)
		assert.NotContains(t, spec.Command, runnerpaths.SessionTmpDir("sess-remote"))
		assert.NotContains(t, spec.Command, runnerpaths.SessionDir("sess-remote"))
	})

	t.Run("unsandboxed commands get the runner's own paths from the environment", func(t *testing.T) {
		n := *node
		n.Sandbox = &sandboxOff
		fs := &fakeCommandFS{do: okCommand("", "", 0)}
		runRemoteCommand(t, t.Context(), fs, NewCommandRunner(true), &n, nil)
		spec := fs.got[0]
		assert.Equal(t, "cp ${ASGARD_TMP_DIR}/a ${ASGARD_SESSION_DIR}/b && cd /runner/ws && echo in", spec.Command)
		assert.Equal(t, "/tmp/work", spec.WorkingDir, "the runner resolves the namespaced form itself")
		assert.False(t, spec.Sandbox)
	})

	t.Run("the runner-less default sandbox setting applies without a node override", func(t *testing.T) {
		fs := &fakeCommandFS{do: okCommand("", "", 0)}
		runRemoteCommand(t, t.Context(), fs, NewCommandRunner(false), node, nil)
		assert.False(t, fs.got[0].Sandbox)
	})

	t.Run("dirs the workflow chose are kept", func(t *testing.T) {
		fs := &fakeCommandFS{do: okCommand("", "", 0)}
		runRemoteCommand(t, t.Context(), fs, NewCommandRunner(true), node, func(n *NodeContext) {
			n.TmpDir = "/runner/ws/mytmp"
			n.SessionDir = "/runner/ws/mysess"
		})
		spec := fs.got[0]
		assert.Equal(t, "cp /runner/ws/mytmp/a /runner/ws/mysess/b && cd /runner/ws && echo in", spec.Command)
		assert.Equal(t, "/runner/ws/mytmp/work", spec.WorkingDir)
		assert.Equal(t, "/runner/ws/mytmp", spec.TmpDir)
	})
}

func TestCommandRunnerOnRunnerTimeoutIsEnforcedByTheRunner(t *testing.T) {
	node := &workflowspec.NodeSpec{ID: "n", Type: workflowspec.NodeTypeCommand, Command: "c", Timeout: "1s"}
	fs := &fakeCommandFS{do: func(ctx context.Context, _ runnerfs.CommandSpec) (runnerfs.CommandResult, error) {
		dl, ok := ctx.Deadline()
		require.True(t, ok)
		assert.Greater(t, time.Until(dl), 5*time.Second, "the backend waits past the node timeout for the runner's verdict")
		return runnerfs.CommandResult{ExitCode: -1}, nil
	}}
	res := runRemoteCommand(t, t.Context(), fs, NewCommandRunner(true), node, nil)
	assert.Equal(t, workflowspec.StatusFailed, res.Status)
	assert.Equal(t, -1, res.ExitCode)
	assert.Equal(t, time.Second, fs.got[0].Timeout)
}

func TestCommandRunnerWithoutRunnerFSRunsLocally(t *testing.T) {
	// A host-local FS is not a CommandRunner: the node executes here.
	res := runCommandNode(t, &workflowspec.NodeSpec{ID: "l", Type: workflowspec.NodeTypeCommand, Command: "echo local"})
	assert.Equal(t, "local\n", res.Output)
	require.NotNil(t, runnerfs.NewLocal("x"))
	_, isRunner := any(runnerfs.NewLocal("x")).(runnerfs.CommandRunner)
	assert.False(t, isRunner)
}

func TestCommandRunnerOnRunnerMarksTruncatedStdout(t *testing.T) {
	node := &workflowspec.NodeSpec{ID: "n", Type: workflowspec.NodeTypeCommand, Command: "c"}
	fs := &fakeCommandFS{do: func(context.Context, runnerfs.CommandSpec) (runnerfs.CommandResult, error) {
		return runnerfs.CommandResult{Stdout: []byte("big"), StdoutTruncated: true}, nil
	}}
	res := runRemoteCommand(t, t.Context(), fs, NewCommandRunner(true), node, nil)
	assert.Equal(t, "big"+StdoutTruncatedMarker, res.Output)
}
