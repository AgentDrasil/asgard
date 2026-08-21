package workflow

import (
	"fmt"
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

const sampleDevWorkflowYAML = `
name: dev-workflow
max_node_executions: 500

loops:
  - id: step_loop
    nodes:
      - coding_agent
      - commit_agent
      - code_review_agent
      - check_review_verdict
      - fix_agent
      - check_pending_steps
      - mark_step_skipped
    max_iterations: 50

  - id: fix_loop
    parent: step_loop
    nodes:
      - code_review_agent
      - check_review_verdict
      - fix_agent
    max_iterations: 5
    on_exhausted: fix_fallback

  - id: final_review_loop
    nodes:
      - final_cleaner
      - final_commit
      - final_review_agent
      - check_final_verdict
    max_iterations: 3
    on_exhausted: final_fallback

nodes:
  - id: check_justfile
    type: command
    sandbox: true
    working_dir: "${run_dir}"
    command: "mkdir -p ${tmp_dir}/plan && git rev-parse HEAD > ${tmp_dir}/plan/base_commit.txt && just --summary | grep -w build && just --summary | grep -w test && just --summary | grep -w fmt && just --summary | grep -w lint"
    allowed_exit_codes: [0, 1]

  - id: init_justfile_agent
    type: agent
    depends:
      - node: check_justfile
        when: "nodes.check_justfile.exit_code != 0"
    agent_id: justfile-init
    session_policy: fresh

  - id: intend_agent
    type: agent
    depends:
      - node: check_justfile
      - node: init_justfile_agent
    join: always
    agent_id: intend
    session_policy: fresh
    entry: true
    required_outputs:
      - "${tmp_dir}/intend.md"

  - id: plan_agent
    type: agent
    depends:
      - node: intend_agent
      - node: plan_approval
        when: "nodes.plan_approval.output != 'Approve'"
    join: always
    agent_id: planner
    session_policy: inherit
    required_outputs:
      - "${tmp_dir}/plan/plan.md"
      - "${tmp_dir}/plan/todo.yaml"

  - id: plan_review_agent
    type: agent
    depends:
      - node: plan_agent
    agent_id: plan-reviewer
    session_policy: fresh

  - id: plan_approval
    type: human
    depends:
      - node: plan_review_agent
    prompt: "Please review Plan (${tmp_dir}/plan/plan.md) and Review Feedback (${tmp_dir}/plan/review_feedback.md). Choose Approve or Request Changes."
    options: ["Approve", "Request Changes"]
    output_file: "plan_user_decision.txt"

  - id: coding_agent
    type: agent
    depends:
      - node: plan_approval
        when: "nodes.plan_approval.output == 'Approve'"
      - node: check_pending_steps
        when: "nodes.check_pending_steps.exit_code == 0"
        counts_loop: step_loop
    join: always
    agent_id: coder
    session_policy: fresh

  - id: commit_agent
    type: agent
    depends:
      - node: coding_agent
    agent_id: commit-agent
    session_policy: fresh

  - id: code_review_agent
    type: agent
    depends:
      - node: commit_agent
      - node: fix_agent
    join: always
    agent_id: code-reviewer
    session_policy: fresh
    required_outputs:
      - "${tmp_dir}/review_verdict.txt"

  - id: check_review_verdict
    type: command
    sandbox: false
    command: >
      if grep -qx 'FIX' ${tmp_dir}/review_verdict.txt; then exit 0;
      elif grep -qx 'PASS' ${tmp_dir}/review_verdict.txt; then exit 1;
      else exit 2; fi
    allowed_exit_codes: [0, 1]
    depends:
      - node: code_review_agent

  - id: fix_agent
    type: agent
    depends:
      - node: check_review_verdict
        when: "nodes.check_review_verdict.exit_code == 0"
        counts_loop: fix_loop
      - node: fix_fallback
        when: "nodes.fix_fallback.output == 'Retry (reset counter)'"
        resets_loop: fix_loop
    join: always
    agent_id: fix-agent
    session_policy: fresh

  - id: check_pending_steps
    type: command
    sandbox: false
    command: "grep -q 'status: pending' ${tmp_dir}/plan/todo.yaml"
    allowed_exit_codes: [0, 1]
    depends:
      - node: check_review_verdict
        when: "nodes.check_review_verdict.exit_code == 1"
      - node: mark_step_skipped
        when: "nodes.mark_step_skipped.exit_code == 0"
    join: always

  - id: fix_fallback
    type: human
    prompt: "Auto-fix exhausted after 5 attempts for the current step. Please review Code Review Report (${tmp_dir}/code_review.md) and Fix Attempts Log (${tmp_dir}/plan/fix_attempts.md)."
    options: ["Retry (reset counter)", "Skip This Step", "Abort Workflow"]
    output_file: "fix_fallback_decision.txt"

  - id: mark_step_skipped
    type: command
    sandbox: false
    command: "sed -i '0,/status: in_review/s//status: skipped (known-broken)/' ${tmp_dir}/plan/todo.yaml && rm -f ${tmp_dir}/review_verdict.txt"
    allowed_exit_codes: [0]
    depends:
      - node: fix_fallback
        when: "nodes.fix_fallback.output == 'Skip This Step'"

  - id: final_cleaner
    type: agent
    depends:
      - node: check_pending_steps
        when: "nodes.check_pending_steps.exit_code == 1"
      - node: check_final_verdict
        when: "nodes.check_final_verdict.exit_code == 0"
        counts_loop: final_review_loop
      - node: final_approval
        when: "nodes.final_approval.output == 'Request Changes'"
      - node: final_fallback
        when: "nodes.final_fallback.output == 'Retry (reset counter)'"
        resets_loop: final_review_loop
    join: always
    agent_id: final-cleaner
    session_policy: fresh

  - id: final_commit
    type: agent
    depends:
      - node: final_cleaner
    agent_id: commit-agent
    session_policy: fresh

  - id: final_review_agent
    type: agent
    depends:
      - node: final_commit
    agent_id: final-reviewer
    session_policy: fresh
    required_outputs:
      - "${tmp_dir}/final_verdict.txt"

  - id: check_final_verdict
    type: command
    sandbox: false
    command: >
      if grep -qx 'FIX' ${tmp_dir}/final_verdict.txt; then exit 0;
      elif grep -qx 'PASS' ${tmp_dir}/final_verdict.txt; then exit 1;
      else exit 2; fi
    allowed_exit_codes: [0, 1]
    depends:
      - node: final_review_agent

  - id: final_fallback
    type: human
    prompt: "Final review auto-fix exhausted after 3 attempts. Please inspect Final Audit Report (${tmp_dir}/final_review.md)."
    options: ["Retry (reset counter)", "Proceed to Approval", "Abort Workflow"]
    output_file: "final_fallback_decision.txt"

  - id: final_approval
    type: human
    depends:
      - node: check_final_verdict
        when: "nodes.check_final_verdict.exit_code == 1"
      - node: final_fallback
        when: "nodes.final_fallback.output == 'Proceed to Approval'"
    join: always
    prompt: "All steps completed, minor issues cleaned, and final audit passed. Please review Final Audit Report (${tmp_dir}/final_review.md)."
    options: ["Accept & Deliver", "Request Changes"]
    output_file: "final_decision.txt"
`

func TestValidateFunctionNodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		spec      string
		wantErr   string
		checkFunc func(t *testing.T, defn *WorkflowDefinition)
	}{
		{
			name: "valid function node",
			spec: `
name: test-function-node
nodes:
  - id: fn1
    type: function
    function: my_transform
    prompt: "transform ${input}"
    timeout: 30s
`,
			checkFunc: func(t *testing.T, defn *WorkflowDefinition) {
				require.Len(t, defn.Nodes, 1)
				assert.Equal(t, NodeTypeFunction, defn.Nodes[0].Type)
				assert.Equal(t, "my_transform", defn.Nodes[0].Function)
				assert.Equal(t, "30s", defn.Nodes[0].Timeout)
				assert.Equal(t, "transform ${input}", defn.Nodes[0].Prompt)
			},
		},
		{
			name: "missing function field",
			spec: `
name: test-function-node
nodes:
  - id: fn1
    type: function
`,
			wantErr: "node fn1: function is required for function nodes",
		},
		{
			name: "invalid type message includes function",
			spec: `
name: test-function-node
nodes:
  - id: weird
    type: quantum
`,
			wantErr: `node weird: invalid type "quantum" (must be agent, llm, command, human, function or workflow)`,
		},
		{
			name: "allowed_exit_codes still rejected on function nodes",
			spec: `
name: test-function-node
nodes:
  - id: fn1
    type: function
    function: my_transform
    allowed_exit_codes: [0, 1]
`,
			wantErr: "allowed_exit_codes is only allowed on command nodes",
		},
		{
			name: "entry rejected on function nodes",
			spec: `
name: test-function-node
nodes:
  - id: fn1
    type: function
    function: my_transform
    entry: true
`,
			wantErr: "entry is only allowed on agent nodes",
		},
		{
			name: "function field rejected on non-function node",
			spec: `
name: test-function-node
nodes:
  - id: cmd1
    type: command
    command: "echo hello"
    function: my_transform
`,
			wantErr: "function is only allowed on function nodes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defn, err := ParseDefinition([]byte(tt.spec))
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.checkFunc != nil {
				tt.checkFunc(t, defn)
			}
		})
	}
}

func TestParseDevWorkflowYAML(t *testing.T) {
	t.Parallel()
	defn, err := ParseDefinition([]byte(sampleDevWorkflowYAML))
	require.NoError(t, err)
	assert.Equal(t, "dev-workflow", defn.Name)
}

func TestValidateRequiredOutputs_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    string
		wantErr string
	}{
		{
			name: "valid required_outputs and max_retries",
			spec: `
name: test-required-outputs
nodes:
  - id: agent1
    type: agent
    agent_id: agy
    entry: true
    required_outputs:
      - "${tmp_dir}/out.md"
    max_retries: 3
`,
		},
		{
			name: "negative max_retries rejected",
			spec: `
name: test-required-outputs
nodes:
  - id: agent1
    type: agent
    agent_id: agy
    entry: true
    max_retries: -1
`,
			wantErr: "max_retries cannot be negative",
		},
		{
			name: "empty required_outputs entry rejected",
			spec: `
name: test-required-outputs
nodes:
  - id: agent1
    type: agent
    agent_id: agy
    entry: true
    required_outputs:
      - "  "
`,
			wantErr: "required_outputs entry cannot be empty",
		},
		{
			name: "required_outputs on command node rejected",
			spec: `
name: test-required-outputs
nodes:
  - id: cmd1
    type: command
    command: "ls"
    required_outputs:
      - "${tmp_dir}/out.md"
`,
			wantErr: "required_outputs is only allowed on agent nodes",
		},
		{
			name: "max_retries on human node rejected",
			spec: `
name: test-required-outputs
nodes:
  - id: ask1
    type: human
    prompt: "Ready?"
    max_retries: 2
`,
			wantErr: "max_retries is only allowed on agent nodes",
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

// workflowNodeSpec is a minimal valid workflow node body used by the
// NodeTypeWorkflow validation tests.
func workflowNodeSpec(extra string) string {
	return `
name: wf-node-test
nodes:
  - id: sub
    type: command
    command: "true"
  - id: fan
    type: workflow
    workflow: sub-wf
` + extra + `
  - id: done
    type: command
    command: "true"
    depends:
      - node: fan
`
}

func TestValidate_WorkflowNodeType_Valid(t *testing.T) {
	t.Parallel()

	specs := []string{
		workflowNodeSpec(""),
		workflowNodeSpec("    fanout:\n      items_file: ${tmp_dir}/items.txt\n"),
		workflowNodeSpec("    fanout:\n      items_file: ${tmp_dir}/items.txt\n      max_parallel: 5\n      output_file: ${tmp_dir}/results.txt\n"),
	}
	for _, spec := range specs {
		defn, err := ParseDefinition([]byte(spec))
		require.NoError(t, err)
		node := defn.Nodes[1]
		assert.Equal(t, NodeTypeWorkflow, node.Type)
		if node.Fanout != nil && node.Fanout.MaxParallel != nil {
			assert.Equal(t, 5, *node.Fanout.MaxParallel)
		}
	}
}

func TestValidate_WorkflowNodeType_MissingWorkflow(t *testing.T) {
	t.Parallel()

	spec := `
name: wf-missing
nodes:
  - id: fan
    type: workflow
`
	_, err := ParseDefinition([]byte(spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow is required for workflow nodes")
}

func TestValidate_WorkflowNodeType_PromptForbidden(t *testing.T) {
	t.Parallel()

	spec := `
name: wf-prompt
nodes:
  - id: fan
    type: workflow
    workflow: sub-wf
    prompt: "do things"
`
	_, err := ParseDefinition([]byte(spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt is not allowed on workflow nodes")
}

func TestValidate_WorkflowNodeType_NodeOutputFileForbidden(t *testing.T) {
	t.Parallel()

	spec := `
name: wf-outputfile
nodes:
  - id: fan
    type: workflow
    workflow: sub-wf
    output_file: ${tmp_dir}/out.txt
`
	_, err := ParseDefinition([]byte(spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output_file is not allowed on workflow nodes")
}

func TestValidate_WorkflowNodeType_InvalidFanout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		extra   string
		wantErr string
	}{
		{
			name:    "missing items_file",
			extra:   "    fanout:\n      max_parallel: 2\n",
			wantErr: "fanout.items_file is required",
		},
		{
			name:    "max_parallel zero",
			extra:   "    fanout:\n      items_file: items.txt\n      max_parallel: 0\n",
			wantErr: "fanout.max_parallel must be positive",
		},
		{
			name:    "max_parallel negative",
			extra:   "    fanout:\n      items_file: items.txt\n      max_parallel: -1\n",
			wantErr: "fanout.max_parallel must be positive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseDefinition([]byte(workflowNodeSpec(tt.extra)))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidate_WorkflowNodeType_InvalidFieldsOnOtherTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    string
		wantErr string
	}{
		{
			name: "workflow field on command node",
			spec: `
name: wf-on-cmd
nodes:
  - id: cmd1
    type: command
    command: "true"
    workflow: sub-wf
`,
			wantErr: "workflow is only allowed on workflow nodes",
		},
		{
			name: "fanout on agent node",
			spec: `
name: fanout-on-agent
agents:
  - id: a1
nodes:
  - id: ag1
    type: agent
    agent_id: a1
    entry: true
    fanout:
      items_file: items.txt
`,
			wantErr: "fanout is only allowed on workflow nodes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseDefinition([]byte(tt.spec))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidate_NoHuman_RejectsHumanNode(t *testing.T) {
	t.Parallel()

	spec := `
name: nohuman-wf
no_human: true
nodes:
  - id: cmd1
    type: command
    command: "true"
  - id: ask1
    type: human
    prompt: "Proceed?"
`
	_, err := ParseDefinition([]byte(spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow declared no_human: true cannot contain human nodes")
}

func TestValidate_Schedule_RequiresNoHuman(t *testing.T) {
	t.Parallel()

	spec := `
name: sched-wf
schedule: "0 2 * * *"
nodes:
  - id: cmd1
    type: command
    command: "true"
`
	_, err := ParseDefinition([]byte(spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow with schedule must declare no_human: true")
}

func TestValidate_Schedule_Format(t *testing.T) {
	t.Parallel()

	base := func(schedule string) string {
		return fmt.Sprintf(`
name: sched-wf
no_human: true
schedule: %q
nodes:
  - id: cmd1
    type: command
    command: "true"
`, schedule)
	}

	for _, valid := range []string{"0 2 * * *", "*/5 * * * *"} {
		_, err := ParseDefinition([]byte(base(valid)))
		require.NoError(t, err, "schedule %q should be valid", valid)
	}

	for _, invalid := range []string{"invalid-cron", "* * *"} {
		_, err := ParseDefinition([]byte(base(invalid)))
		require.Error(t, err, "schedule %q should be rejected", invalid)
		assert.Contains(t, err.Error(), "invalid schedule expression")
	}
}

func TestValidate_Schedule_NeverFires(t *testing.T) {
	t.Parallel()

	spec := `
name: never-wf
no_human: true
schedule: "0 0 31 2 *"
nodes:
  - id: cmd1
    type: command
    command: "true"
`
	_, err := ParseDefinition([]byte(spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schedule expression will never fire")
}
