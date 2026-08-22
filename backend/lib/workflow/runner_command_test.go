package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

func runCommandNode(t *testing.T, node *workflowspec.NodeSpec) *workflowspec.NodeResult {
	t.Helper()
	runner := NewCommandRunner(false)
	nctx := &NodeContext{
		SessionID: "sess-cmd-test",
		RunDir:    t.TempDir(),
		TmpDir:    t.TempDir(),
		Node:      node,
		Defn:      &workflowspec.WorkflowDefinition{Name: "cmd-test", Nodes: []*workflowspec.NodeSpec{node}},
	}
	result, err := runner.Run(t.Context(), nctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	return result
}

func TestCommandRunner_DefaultExitCodeSemantics(t *testing.T) {
	t.Parallel()

	t.Run("exit 0 succeeds", func(t *testing.T) {
		result := runCommandNode(t, &workflowspec.NodeSpec{ID: "ok", Type: workflowspec.NodeTypeCommand, Command: "true"})
		assert.Equal(t, workflowspec.StatusSucceeded, result.Status)
		assert.Equal(t, 0, result.ExitCode)
		assert.NoError(t, result.Error)
	})

	t.Run("exit 1 fails by default", func(t *testing.T) {
		result := runCommandNode(t, &workflowspec.NodeSpec{ID: "fail", Type: workflowspec.NodeTypeCommand, Command: "false"})
		assert.Equal(t, workflowspec.StatusFailed, result.Status)
		assert.Equal(t, 1, result.ExitCode)
		assert.Error(t, result.Error)
	})
}

func TestCommandRunner_AllowedExitCodes(t *testing.T) {
	t.Parallel()

	t.Run("whitelisted exit 1 succeeds and keeps real exit code", func(t *testing.T) {
		result := runCommandNode(t, &workflowspec.NodeSpec{
			ID:               "grepish",
			Type:             workflowspec.NodeTypeCommand,
			Command:          "false",
			AllowedExitCodes: []int{0, 1},
		})
		assert.Equal(t, workflowspec.StatusSucceeded, result.Status)
		assert.Equal(t, 1, result.ExitCode, "success must preserve the real exit code for when-edges")
		assert.NoError(t, result.Error)
	})

	t.Run("non-whitelisted exit 2 fails and keeps real exit code", func(t *testing.T) {
		result := runCommandNode(t, &workflowspec.NodeSpec{
			ID:               "bad",
			Type:             workflowspec.NodeTypeCommand,
			Command:          "exit 2",
			AllowedExitCodes: []int{0, 1},
		})
		assert.Equal(t, workflowspec.StatusFailed, result.Status)
		assert.Equal(t, 2, result.ExitCode, "failure must preserve the real exit code")
		assert.Error(t, result.Error)
	})
}

func TestCommandRunner_AllowedExitCodes_DownstreamEdgeSemantics(t *testing.T) {
	t.Parallel()

	defn := &workflowspec.WorkflowDefinition{Name: "edges", Nodes: []*workflowspec.NodeSpec{
		{ID: "check", Type: workflowspec.NodeTypeCommand, Command: "exit 2", AllowedExitCodes: []int{0, 1}},
	}}
	upstreams := map[string]*workflowspec.NodeResult{
		"check": runCommandNode(t, defn.Nodes[0]),
	}

	// A FAILED node with exit code 2 must not satisfy a `== 0` (nor `== 1`)
	// condition edge, so no false branch is activated downstream.
	ok, err := workflowspec.EvaluateSimpleExpr("nodes.check.exit_code == 0", upstreams, defn)
	require.NoError(t, err)
	assert.False(t, ok, "failed exit 2 must not match exit_code == 0 edge")

	ok, err = workflowspec.EvaluateSimpleExpr("nodes.check.exit_code == 1", upstreams, defn)
	require.NoError(t, err)
	assert.False(t, ok, "failed exit 2 must not match exit_code == 1 edge")

	ok, err = workflowspec.EvaluateSimpleExpr("nodes.check.exit_code != 0", upstreams, defn)
	require.NoError(t, err)
	assert.True(t, ok, "failure branch edge must still match")

	// A SUCCEEDED whitelisted node with exit code 1 must match `== 1` edges.
	defnOK := &workflowspec.WorkflowDefinition{Name: "edges-ok", Nodes: []*workflowspec.NodeSpec{
		{ID: "check", Type: workflowspec.NodeTypeCommand, Command: "false", AllowedExitCodes: []int{0, 1}},
	}}
	upstreamsOK := map[string]*workflowspec.NodeResult{
		"check": runCommandNode(t, defnOK.Nodes[0]),
	}
	ok, err = workflowspec.EvaluateSimpleExpr("nodes.check.exit_code == 1", upstreamsOK, defnOK)
	require.NoError(t, err)
	assert.True(t, ok, "whitelisted exit 1 must match exit_code == 1 edge")
}

func TestValidate_AllowedExitCodesOnlyOnCommandNodes(t *testing.T) {
	t.Parallel()

	t.Run("command node with whitelist is valid", func(t *testing.T) {
		_, err := workflowspec.ParseDefinition([]byte(`
name: ok-wf
nodes:
  - id: check
    type: command
    command: "grep -q x file"
    allowed_exit_codes: [0, 1]
  - id: consumer
    type: command
    command: "true"
    depends:
      - node: check
`))
		require.NoError(t, err)
	})

	t.Run("agent node with whitelist is rejected", func(t *testing.T) {
		_, err := workflowspec.ParseDefinition([]byte(`
name: bad-wf
nodes:
  - id: entry_agent
    type: agent
    agent_id: some-agent
    entry: true
    allowed_exit_codes: [0, 1]
`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "allowed_exit_codes is only allowed on command nodes")
	})
}
