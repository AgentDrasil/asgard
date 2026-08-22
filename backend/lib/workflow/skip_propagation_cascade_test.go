package workflow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// NewEngineWithRunner builds an engine whose command nodes are served by the
// given test runner.
func NewEngineWithRunner(runner NodeRunner) *Engine {
	registry := NewNodeRunnerRegistry()
	registry.Register(runner)
	return NewEngine(registry)
}

// stubRunner is a deterministic NodeRunner used to drive the engine without
// executing real commands.
type stubRunner struct {
	mu       sync.Mutex
	outcomes map[string]stubOutcome
	runned   []string
}

type stubOutcome struct {
	exitCode int
	output   string
	err      error
}

func newStubRunner(outcomes map[string]stubOutcome) *stubRunner {
	return &stubRunner{outcomes: outcomes}
}

func (s *stubRunner) Supports(t workflowspec.NodeType) bool { return t == workflowspec.NodeTypeCommand }

func (s *stubRunner) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	s.mu.Lock()
	s.runned = append(s.runned, nctx.Node.ID)
	out, ok := s.outcomes[nctx.Node.ID]
	s.mu.Unlock()
	if !ok {
		out = stubOutcome{}
	}
	if out.err != nil {
		return nil, out.err
	}
	status := workflowspec.StatusSucceeded
	if out.exitCode != 0 {
		status = workflowspec.StatusFailed
	}
	return &workflowspec.NodeResult{Status: status, ExitCode: out.exitCode, Output: out.output}, nil
}

func (s *stubRunner) hasRun(nodeID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.runned {
		if id == nodeID {
			return true
		}
	}
	return false
}

// buildAndFixYAML is the canonical build-and-fix DAG from the design doc.
const buildAndFixYAML = `
name: build-and-fix-workflow
nodes:
  - id: build_cmd
    type: command
    command: "go build ./..."
  - id: fix_build_agent
    type: command
    command: "fix"
    depends:
      - node: build_cmd
        when: "nodes.build_cmd.exit_code != 0"
  - id: post_test
    type: command
    command: "test"
    depends:
      - node: fix_build_agent
`

func runEngine(t *testing.T, yamlDef string, outcomes map[string]stubOutcome) (*WorkflowRunResult, *stubRunner) {
	t.Helper()
	defn, err := workflowspec.ParseDefinition([]byte(yamlDef))
	require.NoError(t, err)

	stub := newStubRunner(outcomes)
	engine := NewEngineWithRunner(stub)
	res, err := engine.Execute(context.Background(), defn, RunContext{SessionID: "test-session"})
	require.NoError(t, err)
	return res, stub
}

// TestSkipPropagationMatrix validates the status propagation matrix from the
// design doc (§2.3).
func TestSkipPropagationMatrix(t *testing.T) {
	t.Run("build succeeded: conditional fix skipped as ConditionFalse, global COMPLETED", func(t *testing.T) {
		res, stub := runEngine(t, buildAndFixYAML, map[string]stubOutcome{
			"build_cmd": {exitCode: 0},
		})

		assert.Equal(t, RunStatusCompleted, res.Status)
		assert.Equal(t, workflowspec.StatusSucceeded, res.Nodes["build_cmd"].Status)
		require.NotNil(t, res.Nodes["fix_build_agent"])
		assert.Equal(t, workflowspec.StatusSkipped, res.Nodes["fix_build_agent"].Status)
		assert.Equal(t, workflowspec.SkipReasonConditionFalse, res.Nodes["fix_build_agent"].SkipReason)
		assert.Equal(t, workflowspec.StatusSkipped, res.Nodes["post_test"].Status)
		assert.Equal(t, workflowspec.SkipReasonConditionFalse, res.Nodes["post_test"].SkipReason)
		assert.False(t, stub.hasRun("fix_build_agent"))
		assert.False(t, stub.hasRun("post_test"))
	})

	t.Run("build failed, fix repaired: when-branch absorbs failure, global COMPLETED", func(t *testing.T) {
		res, stub := runEngine(t, buildAndFixYAML, map[string]stubOutcome{
			"build_cmd":       {exitCode: 1},
			"fix_build_agent": {exitCode: 0},
			"post_test":       {exitCode: 0},
		})

		assert.Equal(t, RunStatusCompleted, res.Status)
		assert.Equal(t, workflowspec.StatusFailed, res.Nodes["build_cmd"].Status)
		assert.Equal(t, workflowspec.StatusSucceeded, res.Nodes["fix_build_agent"].Status)
		assert.Equal(t, workflowspec.StatusSucceeded, res.Nodes["post_test"].Status)
		assert.True(t, stub.hasRun("fix_build_agent"))
		assert.True(t, stub.hasRun("post_test"))
	})

	t.Run("build failed, unprotected downstream cascades, global FAILED", func(t *testing.T) {
		yamlDef := `
name: cascade
nodes:
  - id: build_cmd
    type: command
    command: "go build ./..."
  - id: verify
    type: command
    command: "verify"
    depends:
      - node: build_cmd
  - id: post_test
    type: command
    command: "test"
    depends:
      - node: verify
`
		res, stub := runEngine(t, yamlDef, map[string]stubOutcome{
			"build_cmd": {exitCode: 1},
		})

		assert.Equal(t, RunStatusFailed, res.Status)
		assert.Equal(t, workflowspec.StatusFailed, res.Nodes["build_cmd"].Status)
		assert.Equal(t, workflowspec.StatusSkipped, res.Nodes["verify"].Status)
		assert.Equal(t, workflowspec.SkipReasonCascadedFailure, res.Nodes["verify"].SkipReason)
		assert.Equal(t, workflowspec.StatusSkipped, res.Nodes["post_test"].Status)
		assert.Equal(t, workflowspec.SkipReasonCascadedFailure, res.Nodes["post_test"].SkipReason)
		assert.False(t, stub.hasRun("verify"))
		assert.False(t, stub.hasRun("post_test"))
	})
}

// TestJoinAlways verifies that join: always (== on_skip: run + on_fail: run)
// lets a join node run even when upstreams skipped or failed.
func TestJoinAlways(t *testing.T) {
	yamlDef := `
name: join
nodes:
  - id: build_cmd
    type: command
    command: "go build ./..."
  - id: fix_build_agent
    type: command
    command: "fix"
    depends:
      - node: build_cmd
        when: "nodes.build_cmd.exit_code != 0"
  - id: build_summary
    type: command
    command: "summarize"
    depends:
      - node: build_cmd
      - node: fix_build_agent
    join: always
`
	res, stub := runEngine(t, yamlDef, map[string]stubOutcome{
		"build_cmd":     {exitCode: 0},
		"build_summary": {exitCode: 0},
	})

	assert.Equal(t, RunStatusCompleted, res.Status)
	assert.Equal(t, workflowspec.StatusSkipped, res.Nodes["fix_build_agent"].Status)
	assert.Equal(t, workflowspec.SkipReasonConditionFalse, res.Nodes["fix_build_agent"].SkipReason)
	assert.Equal(t, workflowspec.StatusSucceeded, res.Nodes["build_summary"].Status)
	assert.True(t, stub.hasRun("build_summary"))
}

// TestEvaluateNodeReadinessMultiEdge exercises the multi-dependency edge
// arbitration algorithm directly.
func TestEvaluateNodeReadinessMultiEdge(t *testing.T) {
	node := &workflowspec.NodeSpec{
		ID:   "joiner",
		Type: workflowspec.NodeTypeCommand,
		Depends: []workflowspec.NodeDependency{
			{NodeID: "guarded", When: "nodes.guarded.exit_code != 0"},
			{NodeID: "plain"},
		},
	}

	t.Run("when false on guarded edge skips with ConditionFalse", func(t *testing.T) {
		action, reason := EvaluateNodeReadiness(node, map[string]*workflowspec.NodeResult{
			"guarded": {Status: workflowspec.StatusSucceeded, ExitCode: 0},
			"plain":   {Status: workflowspec.StatusSucceeded},
		})
		assert.Equal(t, ActionSkip, action)
		assert.Equal(t, workflowspec.SkipReasonConditionFalse, reason)
	})

	t.Run("when true bypasses parent failure", func(t *testing.T) {
		action, reason := EvaluateNodeReadiness(node, map[string]*workflowspec.NodeResult{
			"guarded": {Status: workflowspec.StatusFailed, ExitCode: 1},
			"plain":   {Status: workflowspec.StatusSucceeded},
		})
		assert.Equal(t, ActionRun, action)
		assert.Empty(t, reason)
	})

	t.Run("plain failed edge cascades", func(t *testing.T) {
		action, reason := EvaluateNodeReadiness(node, map[string]*workflowspec.NodeResult{
			"guarded": {Status: workflowspec.StatusSucceeded, ExitCode: 0},
			"plain":   {Status: workflowspec.StatusFailed},
		})
		assert.Equal(t, ActionSkip, action)
		assert.Equal(t, workflowspec.SkipReasonCascadedFailure, reason)
	})

	t.Run("upstream condition-false skip propagates as ConditionFalse", func(t *testing.T) {
		action, reason := EvaluateNodeReadiness(node, map[string]*workflowspec.NodeResult{
			"guarded": {Status: workflowspec.StatusSucceeded, ExitCode: 0},
			"plain":   {Status: workflowspec.StatusSkipped, SkipReason: workflowspec.SkipReasonConditionFalse},
		})
		assert.Equal(t, ActionSkip, action)
		assert.Equal(t, workflowspec.SkipReasonConditionFalse, reason)
	})

	t.Run("upstream cascaded skip propagates as CascadedFailure", func(t *testing.T) {
		action, reason := EvaluateNodeReadiness(node, map[string]*workflowspec.NodeResult{
			"guarded": {Status: workflowspec.StatusSucceeded, ExitCode: 0},
			"plain":   {Status: workflowspec.StatusSkipped, SkipReason: workflowspec.SkipReasonCascadedFailure},
		})
		assert.Equal(t, ActionSkip, action)
		assert.Equal(t, workflowspec.SkipReasonCascadedFailure, reason)
	})
}

// TestForkJoinParallelism asserts that independent nodes actually run
// concurrently (fork-join semantics).
func TestForkJoinParallelism(t *testing.T) {
	yamlDef := `
name: forkjoin
nodes:
  - id: a
    type: command
    command: "sleep"
  - id: b
    type: command
    command: "sleep"
  - id: c
    type: command
    command: "join"
    depends:
      - node: a
      - node: b
`
	defn, err := workflowspec.ParseDefinition([]byte(yamlDef))
	require.NoError(t, err)

	var mu sync.Mutex
	running := 0
	peak := 0
	var wg sync.WaitGroup
	wg.Add(2)

	parallelRunner := &funcRunner{fn: func(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
		if nctx.Node.ID == "c" {
			return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded}, nil
		}
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		mu.Unlock()
		wg.Done()
		wg.Wait()
		return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded}, nil
	}}

	engine := NewEngineWithRunner(parallelRunner)
	res, err := engine.Execute(context.Background(), defn, RunContext{SessionID: "par"})
	require.NoError(t, err)
	assert.Equal(t, RunStatusCompleted, res.Status)
	assert.GreaterOrEqual(t, peak, 2, "independent nodes should run in parallel")
}

// funcRunner adapts a function into a NodeRunner for tests.
type funcRunner struct {
	fn func(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error)
}

func (f *funcRunner) Supports(t workflowspec.NodeType) bool { return t == workflowspec.NodeTypeCommand }

func (f *funcRunner) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	return f.fn(ctx, nctx)
}
