package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loopTestWorkflow assembles a workflow spec from node and loop bodies.
func loopTestWorkflow(nodesBody, loopsBody string) string {
	spec := "name: loop-wf\n" + loopsBody + nodesBody
	return spec
}

// baseLoopNodes is a minimal valid node set: check -> fix backedge carrying
// counts_loop, plus an on_exhausted orphan human and an unrelated exit node.
const baseLoopNodes = `
nodes:
  - id: start
    type: command
    command: "true"
  - id: check
    type: command
    command: "true"
    depends:
      - node: start
  - id: fix
    type: command
    command: "true"
    depends:
      - node: check
        when: "nodes.check.exit_code == 0"
        counts_loop: fix_loop
  - id: fix_fallback
    type: human
    prompt: "Auto-fix exhausted."
`

const baseLoops = `
loops:
  - id: fix_loop
    nodes: [check, fix]
    max_iterations: 5
    on_exhausted: fix_fallback
`

func TestValidateLoops(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		nodesBody  string
		loopsBody  string
		wantErr    string
		checkParse func(t *testing.T, defn *WorkflowDefinition)
	}{
		{
			name:      "valid single loop with counts edge and on_exhausted orphan",
			nodesBody: baseLoopNodes,
			loopsBody: baseLoops,
		},
		{
			name: "valid nested loops with parent relation",
			nodesBody: `
nodes:
  - id: start
    type: command
    command: "true"
  - id: check
    type: command
    command: "true"
    depends:
      - node: start
        counts_loop: step_loop
  - id: fix
    type: command
    command: "true"
    depends:
      - node: check
        when: "nodes.check.exit_code == 0"
        counts_loop: fix_loop
  - id: fix_fallback
    type: human
    prompt: "Auto-fix exhausted."
`,
			loopsBody: `
loops:
  - id: step_loop
    nodes: [start, check, fix]
    max_iterations: 50
  - id: fix_loop
    parent: step_loop
    nodes: [check, fix]
    max_iterations: 5
    on_exhausted: fix_fallback
`,
		},
		{
			name:      "empty loop id",
			nodesBody: baseLoopNodes,
			loopsBody: `
loops:
  - id: ""
    nodes: [check, fix]
    max_iterations: 5
`,
			wantErr: "loop id cannot be empty",
		},
		{
			name:      "duplicate loop id",
			nodesBody: baseLoopNodes,
			loopsBody: baseLoops + `
  - id: fix_loop
    nodes: [check, fix]
    max_iterations: 3
`,
			wantErr: "duplicate loop id: fix_loop",
		},
		{
			name:      "empty nodes list",
			nodesBody: baseLoopNodes,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: []
    max_iterations: 5
`,
			wantErr: "loop fix_loop: nodes list cannot be empty",
		},
		{
			name:      "unknown node in loop nodes",
			nodesBody: baseLoopNodes,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [check, ghost]
    max_iterations: 5
`,
			wantErr: `loop fix_loop: references unknown node "ghost"`,
		},
		{
			name:      "unknown parent loop",
			nodesBody: baseLoopNodes,
			loopsBody: `
loops:
  - id: fix_loop
    parent: outer_loop
    nodes: [check, fix]
    max_iterations: 5
`,
			wantErr: `loop fix_loop: unknown parent loop "outer_loop"`,
		},
		{
			name:      "parent cycle",
			nodesBody: baseLoopNodes,
			loopsBody: `
loops:
  - id: loop_a
    parent: loop_b
    nodes: [check, fix]
    max_iterations: 5
  - id: loop_b
    parent: loop_a
    nodes: [start, check, fix]
    max_iterations: 50
`,
			wantErr: "loop loop_a: parent cycle detected at loop_a",
		},
		{
			name:      "child node outside parent scope",
			nodesBody: baseLoopNodes,
			loopsBody: `
loops:
  - id: step_loop
    nodes: [start, check]
    max_iterations: 50
  - id: fix_loop
    parent: step_loop
    nodes: [check, fix]
    max_iterations: 5
`,
			wantErr: `loop fix_loop: node "fix" is not part of parent loop step_loop`,
		},
		{
			name:      "unrelated loops share a node",
			nodesBody: baseLoopNodes,
			loopsBody: baseLoops + `
  - id: other_loop
    nodes: [start, fix]
    max_iterations: 10
`,
			wantErr: "loops fix_loop and other_loop share node",
		},
		{
			name: "loop without any counts_loop edge",
			nodesBody: `
nodes:
  - id: start
    type: command
    command: "true"
  - id: fix
    type: command
    command: "true"
    depends:
      - node: start
        when: "nodes.start.exit_code == 0"
`,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [start, fix]
    max_iterations: 5
`,
			wantErr: "loop fix_loop: no dependency edge declares counts_loop: fix_loop",
		},
		{
			name: "counts_loop references unknown loop",
			nodesBody: `
nodes:
  - id: start
    type: command
    command: "true"
  - id: fix
    type: command
    command: "true"
    depends:
      - node: start
        when: "nodes.start.exit_code == 0"
        counts_loop: ghost_loop
`,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [fix]
    max_iterations: 5
`,
			wantErr: `node fix: counts_loop references unknown loop "ghost_loop"`,
		},
		{
			name: "counts_loop edge source outside loop",
			nodesBody: `
nodes:
  - id: start
    type: command
    command: "true"
  - id: fix
    type: command
    command: "true"
    depends:
      - node: start
        when: "nodes.start.exit_code == 0"
        counts_loop: fix_loop
`,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [fix]
    max_iterations: 5
`,
			wantErr: "counts_loop \"fix_loop\" requires both edge source start and target fix inside loop fix_loop",
		},
		{
			name: "counts_loop edge target outside loop",
			nodesBody: `
nodes:
  - id: start
    type: command
    command: "true"
  - id: check
    type: command
    command: "true"
    depends:
      - node: start
  - id: fix
    type: command
    command: "true"
    depends:
      - node: check
        when: "nodes.check.exit_code == 0"
        counts_loop: fix_loop
`,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [check]
    max_iterations: 5
`,
			wantErr: "counts_loop \"fix_loop\" requires both edge source check and target fix inside loop fix_loop",
		},
		{
			name: "resets_loop references unknown loop",
			nodesBody: `
nodes:
  - id: fix
    type: command
    command: "true"
    depends:
      - node: fix_fallback
        when: "nodes.fix_fallback.output == 'Retry'"
        resets_loop: ghost_loop
  - id: fix_fallback
    type: human
    prompt: "exhausted"
`,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [fix]
    max_iterations: 5
`,
			wantErr: `node fix: resets_loop references unknown loop "ghost_loop"`,
		},
		{
			name: "resets_loop edge target outside loop",
			nodesBody: `
nodes:
  - id: check
    type: command
    command: "true"
  - id: fix
    type: command
    command: "true"
    depends:
      - node: check
        when: "nodes.check.exit_code == 0"
        counts_loop: fix_loop
  - id: outside
    type: command
    command: "true"
    depends:
      - node: fix_fallback
        when: "nodes.fix_fallback.output == 'Retry'"
        resets_loop: fix_loop
  - id: fix_fallback
    type: human
    prompt: "exhausted"
`,
			loopsBody: baseLoops,
			wantErr:   `node outside: resets_loop "fix_loop" requires edge target outside inside loop fix_loop`,
		},
		{
			name: "edge declares both counts_loop and resets_loop",
			nodesBody: `
nodes:
  - id: check
    type: command
    command: "true"
  - id: fix
    type: command
    command: "true"
    depends:
      - node: check
        when: "nodes.check.exit_code == 0"
        counts_loop: fix_loop
        resets_loop: fix_loop
`,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [check, fix]
    max_iterations: 5
`,
			wantErr: "node fix: dependency on check cannot declare both counts_loop and resets_loop",
		},
		{
			name:      "on_exhausted references unknown node",
			nodesBody: baseLoopNodes,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [check, fix]
    max_iterations: 5
    on_exhausted: ghost_fallback
`,
			wantErr: `loop fix_loop: on_exhausted references unknown node "ghost_fallback"`,
		},
		{
			name:      "duplicate node in loop nodes list",
			nodesBody: baseLoopNodes,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [check, fix, check]
    max_iterations: 5
    on_exhausted: fix_fallback
`,
			wantErr: `loop fix_loop: duplicate node "check" in nodes list`,
		},
		{
			name:      "max_iterations 0 with on_exhausted is dead config",
			nodesBody: baseLoopNodes,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [check, fix]
    max_iterations: 0
    on_exhausted: fix_fallback
`,
			wantErr: "loop fix_loop: max_iterations: 0 (unlimited) cannot declare on_exhausted fallback",
		},
		{
			name: "on_exhausted node belongs to another loop",
			nodesBody: `
nodes:
  - id: start
    type: command
    command: "true"
  - id: check
    type: command
    command: "true"
    depends:
      - node: start
  - id: fix
    type: command
    command: "true"
    depends:
      - node: check
        when: "nodes.check.exit_code == 0"
        counts_loop: fix_loop
  - id: step_start
    type: command
    command: "true"
  - id: step_check
    type: command
    command: "true"
    depends:
      - node: step_start
        when: "nodes.step_start.exit_code == 0"
        counts_loop: step_loop
`,
			loopsBody: `
loops:
  - id: step_loop
    nodes: [step_start, step_check]
    max_iterations: 3
  - id: fix_loop
    nodes: [check, fix]
    max_iterations: 5
    on_exhausted: step_check
`,
			wantErr: "loop fix_loop: on_exhausted node step_check must not belong to any loop (found in step_loop)",
		},
		{
			name:      "on_exhausted node inside the loop",
			nodesBody: baseLoopNodes,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [check, fix, fix_fallback]
    max_iterations: 5
    on_exhausted: fix_fallback
`,
			wantErr: "loop fix_loop: on_exhausted node fix_fallback must not belong to any loop (found in fix_loop)",
		},
		{
			name:      "negative max_iterations",
			nodesBody: baseLoopNodes,
			loopsBody: `
loops:
  - id: fix_loop
    nodes: [check, fix]
    max_iterations: -1
    on_exhausted: fix_fallback
`,
			wantErr: "loop fix_loop: max_iterations cannot be negative",
		},
		{
			name:      "negative max_node_executions",
			nodesBody: baseLoopNodes,
			loopsBody: "max_node_executions: -5\n" + baseLoops,
			wantErr:   "max_node_executions cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defn, err := ParseDefinition([]byte(loopTestWorkflow(tt.nodesBody, tt.loopsBody)))
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.checkParse != nil {
				tt.checkParse(t, defn)
			}
		})
	}
}

func TestValidateLoops_ParsedFields(t *testing.T) {
	t.Parallel()

	spec := loopTestWorkflow(`
nodes:
  - id: start
    type: command
    command: "true"
  - id: check
    type: command
    command: "true"
    depends:
      - node: start
        counts_loop: step_loop
  - id: fix
    type: command
    command: "true"
    depends:
      - node: check
        when: "nodes.check.exit_code == 0"
        counts_loop: fix_loop
      - node: fix_fallback
        when: "nodes.fix_fallback.output == 'Retry (reset counter)'"
        resets_loop: fix_loop
    join: always
  - id: fix_fallback
    type: human
    prompt: "exhausted"
`, `
max_node_executions: 500
loops:
  - id: step_loop
    nodes: [start, check, fix]
    max_iterations: 50
  - id: fix_loop
    parent: step_loop
    nodes: [check, fix]
    max_iterations: 5
    on_exhausted: fix_fallback
`)

	defn, err := ParseDefinition([]byte(spec))
	require.NoError(t, err)

	assert.Equal(t, 500, defn.MaxNodeExecutions)
	require.Len(t, defn.Loops, 2)

	byID := map[string]*LoopSpec{}
	for _, loop := range defn.Loops {
		byID[loop.ID] = loop
	}
	stepLoop := byID["step_loop"]
	fixLoop := byID["fix_loop"]
	require.NotNil(t, stepLoop)
	require.NotNil(t, fixLoop)
	assert.Equal(t, 50, stepLoop.MaxIterations)
	assert.Empty(t, stepLoop.Parent)
	assert.Equal(t, "step_loop", fixLoop.Parent)
	assert.Equal(t, 5, fixLoop.MaxIterations)
	assert.Equal(t, "fix_fallback", fixLoop.OnExhausted)

	var fixNode *NodeSpec
	for _, node := range defn.Nodes {
		if node.ID == "fix" {
			fixNode = node
		}
	}
	require.NotNil(t, fixNode)
	require.Len(t, fixNode.Depends, 2)
	assert.Equal(t, "fix_loop", fixNode.Depends[0].CountsLoop)
	assert.Equal(t, "fix_loop", fixNode.Depends[1].ResetsLoop)
}

// TestValidateHumanNodes_OnExhaustedExemption verifies that a human node
// activated only via on_exhausted routing does not participate in the pairwise
// total-order reachability check (it has no static in-edges by design).
func TestValidateHumanNodes_OnExhaustedExemption(t *testing.T) {
	t.Parallel()

	spec := loopTestWorkflow(`
nodes:
  - id: start
    type: command
    command: "true"
  - id: check
    type: command
    command: "true"
    depends:
      - node: start
  - id: fix
    type: command
    command: "true"
    depends:
      - node: check
        when: "nodes.check.exit_code == 0"
        counts_loop: fix_loop
  - id: fix_fallback
    type: human
    prompt: "Auto-fix exhausted."
  - id: other_approval
    type: human
    prompt: "Approve?"
    depends:
      - node: start
`, baseLoops)

	// fix_fallback and other_approval are statically unordered, but
	// fix_fallback is an on_exhausted orphan and therefore exempt.
	_, err := ParseDefinition([]byte(spec))
	require.NoError(t, err)

	// Without the exemption the same pair must be rejected: declare the loop
	// without on_exhausted so fix_fallback becomes a regular human node.
	specNoExemption := loopTestWorkflow(`
nodes:
  - id: start
    type: command
    command: "true"
  - id: check
    type: command
    command: "true"
    depends:
      - node: start
  - id: fix
    type: command
    command: "true"
    depends:
      - node: check
        when: "nodes.check.exit_code == 0"
        counts_loop: fix_loop
  - id: fix_fallback
    type: human
    prompt: "Auto-fix exhausted."
  - id: other_approval
    type: human
    prompt: "Approve?"
    depends:
      - node: start
`, `
loops:
  - id: fix_loop
    nodes: [check, fix]
    max_iterations: 5
`)

	_, err = ParseDefinition([]byte(specNoExemption))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parallel human nodes are not supported")

	// An on_exhausted human node with static in-edges must be rejected.
	specWithStaticDeps := loopTestWorkflow(`
nodes:
  - id: start
    type: command
    command: "true"
  - id: check
    type: command
    command: "true"
    depends:
      - node: start
  - id: fix
    type: command
    command: "true"
    depends:
      - node: check
        when: "nodes.check.exit_code == 0"
        counts_loop: fix_loop
  - id: fix_fallback
    type: human
    prompt: "Auto-fix exhausted."
    depends:
      - node: start
`, baseLoops)
	_, err = ParseDefinition([]byte(specWithStaticDeps))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "on_exhausted human node must have no incoming dependency edges (must be an orphan)")
}

func TestValidateCommand_AllowedExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    string
		wantErr string
	}{
		{
			name: "valid allowed_exit_codes",
			spec: `
name: test-exit-codes
nodes:
  - id: check
    type: command
    command: "grep foo bar"
    allowed_exit_codes: [0, 1]
`,
		},
		{
			name: "negative allowed_exit_code",
			spec: `
name: test-exit-codes
nodes:
  - id: check
    type: command
    command: "grep foo bar"
    allowed_exit_codes: [-1]
`,
			wantErr: "allowed_exit_codes entry -1 out of valid exit code range (0-255)",
		},
		{
			name: "allowed_exit_code out of range",
			spec: `
name: test-exit-codes
nodes:
  - id: check
    type: command
    command: "grep foo bar"
    allowed_exit_codes: [256]
`,
			wantErr: "allowed_exit_codes entry 256 out of valid exit code range (0-255)",
		},
		{
			name: "duplicate allowed_exit_code",
			spec: `
name: test-exit-codes
nodes:
  - id: check
    type: command
    command: "grep foo bar"
    allowed_exit_codes: [1, 1]
`,
			wantErr: "duplicate entry 1 in allowed_exit_codes",
		},
		{
			name: "allowed_exit_codes on non-command node",
			spec: `
name: test-exit-codes
nodes:
  - id: ask
    type: human
    prompt: "Ready?"
    allowed_exit_codes: [0]
`,
			wantErr: "allowed_exit_codes is only allowed on command nodes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseDefinition([]byte(tt.spec))
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
