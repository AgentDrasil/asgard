package workflow

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// crossSessionRecordingRunner records the AllowCrossSession flag every node
// context carried, so tests can assert the engine propagated the run-level
// flag into each node execution.
type crossSessionRecordingRunner struct {
	mu   sync.Mutex
	seen []bool
}

func (r *crossSessionRecordingRunner) Supports(t workflowspec.NodeType) bool {
	return t == workflowspec.NodeTypeCommand
}

func (r *crossSessionRecordingRunner) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	r.mu.Lock()
	r.seen = append(r.seen, nctx.AllowCrossSession)
	r.mu.Unlock()
	return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded, ExitCode: 0}, nil
}

func (r *crossSessionRecordingRunner) flags() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.seen...)
}

const crossSessionPropagationYAML = `
name: cross-session-propagation
nodes:
  - id: first
    type: command
    command: "true"
  - id: second
    type: command
    command: "true"
    depends:
      - node: first
`

// TestExecute_AllowCrossSession_PropagatesToNodes pins the run-level flag
// reaching every NodeContext: agent and command runners derive their sandbox
// masking policy from it, so a session that enabled cross-session access must
// not have its workflow nodes masked.
func TestExecute_AllowCrossSession_PropagatesToNodes(t *testing.T) {
	t.Parallel()

	defn, err := workflowspec.ParseDefinition([]byte(crossSessionPropagationYAML))
	require.NoError(t, err)

	for _, tt := range []struct {
		name string
		flag bool
	}{
		{name: "cross-session enabled", flag: true},
		{name: "cross-session disabled (zero value)", flag: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runner := &crossSessionRecordingRunner{}
			engine := NewEngineWithRunner(runner)

			res, err := engine.Execute(t.Context(), defn, RunContext{
				SessionID:         "sess-cross-" + tt.name,
				RunDir:            t.TempDir(),
				AllowCrossSession: tt.flag,
			})
			require.NoError(t, err)
			assert.Equal(t, RunStatusCompleted, res.Status)

			flags := runner.flags()
			require.Len(t, flags, 2, "both nodes must have executed")
			assert.Equal(t, tt.flag, flags[0])
			assert.Equal(t, tt.flag, flags[1])
		})
	}
}

// TestExecute_AllowCrossSession_PersistedInSnapshot verifies the flag is
// recorded on WAITING_HUMAN snapshots so resumes rebuild sandboxes with the
// same cross-session access policy.
func TestExecute_AllowCrossSession_PersistedInSnapshot(t *testing.T) {
	t.Parallel()

	suspensionYAML := `
name: cross-session-suspend
nodes:
  - id: ask
    type: human
    prompt: "approve?"
    options: ["ok"]
`

	defn, err := workflowspec.ParseDefinition([]byte(suspensionYAML))
	require.NoError(t, err)

	store := newMemStore()
	engine := NewEngine(NewNodeRunnerRegistry())
	engine.SetRunStore(store)
	engine.SetHumanSuspender(func(req SuspendRequest) error { return nil })

	go func() {
		_, _ = engine.Execute(context.Background(), defn, RunContext{
			SessionID:         "sess-cross-suspend",
			RunID:             "run-cross-suspend",
			RunDir:            t.TempDir(),
			AllowCrossSession: true,
		})
	}()

	require.Eventually(t, func() bool {
		snap, err := store.GetRun("run-cross-suspend")
		return err == nil && snap != nil && snap.Status == PersistStatusWaitingHuman
	}, 5_000_000_000, 10_000_000, "run must suspend and persist a WAITING_HUMAN snapshot")

	snap, err := store.GetRun("run-cross-suspend")
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.True(t, snap.AllowCrossSession, "snapshot must carry the session's AllowCrossSession flag")
}

// TestCommandRunner_AllowCrossSession_SandboxMasking drives the real
// commandRunner in sandbox mode against a mock bwrap that records its argv
// and serves the fakebash unix socket (accepting connections without
// speaking gRPC, so the RunCommand stream fails fast instead of waiting out
// the full 10s socket timeout). The argv-level masking semantics themselves
// are pinned by TestCommandForCommandExec_CrossSessionMasking in package
// bwrap; this test pins the runner's propagation of the flag.
func TestCommandRunner_AllowCrossSession_SandboxMasking(t *testing.T) {
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		t.Setenv("GOPATH", gopath)
	}
	if gocache := os.Getenv("GOCACHE"); gocache != "" {
		t.Setenv("GOCACHE", gocache)
	}
	// Keep HOME short: sockDir lives under $HOME/tmp/fakebash-sock-<uuid>
	// and the AF_UNIX socket path must stay under the 108-byte sun_path limit.
	home, err := os.MkdirTemp("", "xsshm")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)

	mockDir := t.TempDir()
	argsFile := filepath.Join(mockDir, "bwrap-args.txt")
	mockBwrap := filepath.Join(mockDir, "bwrap")
	// The mock records its argv and then serves the fakebash unix socket it
	// was asked to bind (parsed from "--bind <sockDir> /fakebash"): the
	// socket accepts connections but never speaks gRPC, so dialFakebashSocket
	// succeeds immediately and the RunCommand stream fails fast instead of
	// waiting out the full 10s socket timeout. Output is redirected to
	// /dev/null so the orphaned helper cannot hold the test's stdout pipe.
	script := `#!/bin/bash
printf '%s\n' "$@" >> ` + argsFile + `
sockdir=""
args=("$@")
for ((i = 0; i < ${#args[@]} - 2; i++)); do
  if [ "${args[$i]}" = "--bind" ] && [ "${args[$((i + 2))]}" = "/fakebash" ]; then
    sockdir="${args[$((i + 1))]}"
  fi
done
if [ -n "$sockdir" ]; then
  timeout 15 python3 -c 'import socket, sys
s = socket.socket(socket.AF_UNIX)
s.bind(sys.argv[1])
s.listen(64)
while True:
    c, _ = s.accept()
    c.close()' "$sockdir/fakebash.sock" >/dev/null 2>&1 &
fi
exit 0
`
	require.NoError(t, os.WriteFile(mockBwrap, []byte(script), 0o755))
	t.Setenv("PATH", mockDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runNode := func(t *testing.T, allow bool) string {
		t.Helper()
		require.NoError(t, os.RemoveAll(argsFile))

		runner := NewCommandRunnerWithConfig(true, &config.Config{})
		nctx := &NodeContext{
			SessionID:         "sess-cmd-cross",
			RunDir:            t.TempDir(),
			TmpDir:            t.TempDir(),
			AllowCrossSession: allow,
			Node:              &workflowspec.NodeSpec{ID: "probe", Type: workflowspec.NodeTypeCommand, Command: "true"},
			Defn:              &workflowspec.WorkflowDefinition{Name: "cmd-cross"},
		}
		_, err := runner.Run(t.Context(), nctx)
		require.NoError(t, err)

		data, rerr := os.ReadFile(argsFile)
		require.NoError(t, rerr, "mock bwrap must have captured its argv")
		return string(data)
	}

	hostData := filepath.Join(home, "asgard", "data")

	t.Run("masked when flag is false", func(t *testing.T) {
		args := runNode(t, false)
		assert.Contains(t, args, "--tmpfs\n"+hostData)
	})

	t.Run("unmasked when flag is true", func(t *testing.T) {
		args := runNode(t, true)
		assert.NotContains(t, args, "--tmpfs\n"+hostData)
	})
}
