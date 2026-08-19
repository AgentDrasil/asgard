package workflow

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanReviewLoopExecution(t *testing.T) {
	yamlSpec := `
name: test-plan-review-loop
nodes:
  - id: intend_agent
    type: command
    command: "echo intend"

  - id: plan_agent
    type: command
    depends:
      - node: intend_agent
      - node: plan_approval
        when: "nodes.plan_approval.output == 'Request Changes'"
    join: always
    command: "echo plan"

  - id: plan_review_agent
    type: command
    depends:
      - node: plan_agent
    command: "echo plan review"

  - id: plan_approval
    type: human
    depends:
      - node: plan_review_agent
    prompt: "Approve or Request Changes?"
    options: ["Approve", "Request Changes"]

  - id: coding_agent
    type: command
    depends:
      - node: plan_approval
        when: "nodes.plan_approval.output == 'Approve'"
    command: "echo coding"
`
	defn, err := ParseDefinition([]byte(yamlSpec))
	require.NoError(t, err)

	engine, _, suspender := newTestEngine(t)

	var runsMu sync.Mutex
	planRuns := 0
	codingRuns := 0

	runner := &funcRunner{fn: func(ctx context.Context, nctx *NodeContext) (*NodeResult, error) {
		runsMu.Lock()
		defer runsMu.Unlock()
		if nctx.Node.ID == "plan_agent" {
			planRuns++
		}
		if nctx.Node.ID == "coding_agent" {
			codingRuns++
		}
		return &NodeResult{Status: StatusSucceeded, ExitCode: 0, Output: "ok"}, nil
	}}
	engine.registry.Register(runner)

	// Background thread to simulate human replies
	repliedCount := 0
	stopCh := make(chan struct{})
	defer close(stopCh)

	go func() {
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(10 * time.Millisecond):
				suspender.mu.Lock()
				n := len(suspender.requests)
				var req SuspendRequest
				if n > repliedCount {
					req = suspender.requests[n-1]
				}
				suspender.mu.Unlock()

				if req.RunID != "" {
					repliedCount++
					switch repliedCount {
					case 1:
						// First round: request changes
						_, _ = engine.Resume(context.Background(), req.RunID, "Request Changes")
					case 2:
						// Second round: approve
						_, _ = engine.Resume(context.Background(), req.RunID, "Approve")
						return
					}
				}
			}
		}
	}()

	res, err := engine.Execute(context.Background(), defn, RunContext{SessionID: "loop-session"})
	require.NoError(t, err)
	assert.Equal(t, RunStatusCompleted, res.Status)

	runsMu.Lock()
	defer runsMu.Unlock()
	assert.Equal(t, 2, planRuns, "plan_agent should have run twice due to loop")
	assert.Equal(t, 1, codingRuns, "coding_agent should have run once after approval")
}

func TestReviewAndFixLoopExecution(t *testing.T) {
	yamlSpec := `
name: test-review-fix-loop
nodes:
  - id: coding_agent
    type: command
    command: "echo code"

  - id: commit_agent
    type: command
    depends:
      - node: coding_agent
    command: "echo commit"

  - id: code_review_agent
    type: command
    depends:
      - node: commit_agent
      - node: fix_agent
    join: always
    command: "echo review"

  - id: review_approval
    type: human
    depends:
      - node: code_review_agent
    prompt: "Choose Pass & Push or Fix Required"
    options: ["Pass & Push", "Fix Required"]

  - id: fix_agent
    type: command
    depends:
      - node: review_approval
        when: "nodes.review_approval.output == 'Fix Required'"
    command: "echo fix and amend"

  - id: git_push_cmd
    type: command
    depends:
      - node: review_approval
        when: "nodes.review_approval.output == 'Pass & Push'"
    command: "echo push"
`
	defn, err := ParseDefinition([]byte(yamlSpec))
	require.NoError(t, err)

	engine, _, suspender := newTestEngine(t)

	var countsMu sync.Mutex
	executionCounts := make(map[string]int)

	runner := &funcRunner{fn: func(ctx context.Context, nctx *NodeContext) (*NodeResult, error) {
		countsMu.Lock()
		executionCounts[nctx.Node.ID]++
		countsMu.Unlock()
		return &NodeResult{Status: StatusSucceeded, ExitCode: 0, Output: fmt.Sprintf("%s ok", nctx.Node.ID)}, nil
	}}
	engine.registry.Register(runner)

	repliedCount := 0
	stopCh := make(chan struct{})
	defer close(stopCh)

	go func() {
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(10 * time.Millisecond):
				suspender.mu.Lock()
				n := len(suspender.requests)
				var req SuspendRequest
				if n > repliedCount {
					req = suspender.requests[n-1]
				}
				suspender.mu.Unlock()

				if req.RunID != "" {
					repliedCount++
					switch repliedCount {
					case 1:
						// Round 1: Fix Required
						_, _ = engine.Resume(context.Background(), req.RunID, "Fix Required")
					case 2:
						// Round 2: Pass & Push
						_, _ = engine.Resume(context.Background(), req.RunID, "Pass & Push")
						return
					}
				}
			}
		}
	}()

	res, err := engine.Execute(context.Background(), defn, RunContext{SessionID: "fix-loop-session"})
	require.NoError(t, err)
	assert.Equal(t, RunStatusCompleted, res.Status)

	countsMu.Lock()
	defer countsMu.Unlock()
	assert.Equal(t, 1, executionCounts["coding_agent"])
	assert.Equal(t, 1, executionCounts["commit_agent"])
	assert.Equal(t, 2, executionCounts["code_review_agent"])
	assert.Equal(t, 1, executionCounts["fix_agent"])
	assert.Equal(t, 1, executionCounts["git_push_cmd"])

	suspender.mu.Lock()
	defer suspender.mu.Unlock()
	require.Len(t, suspender.requests, 2)
	assert.NotEqual(t, suspender.requests[0].MessageID, suspender.requests[1].MessageID, "each iteration must have unique message ID")
	assert.Contains(t, suspender.requests[1].MessageID, "-2")
}

// loopCountingRunner counts executions per node and lets tests derive each
// invocation's exit code statefully. Returned results are always
// StatusSucceeded (mirroring allowed_exit_codes semantics) so loops are driven
// purely by exit-code routing.
type loopCountingRunner struct {
	mu     sync.Mutex
	counts map[string]int
	// exitFor returns the exit code for a node's nth invocation (1-based).
	exitFor func(nodeID string, n int) int
}

func newLoopCountingRunner(exitFor func(nodeID string, n int) int) *loopCountingRunner {
	return &loopCountingRunner{counts: make(map[string]int), exitFor: exitFor}
}

func (r *loopCountingRunner) Supports(t NodeType) bool { return t == NodeTypeCommand }

func (r *loopCountingRunner) Run(ctx context.Context, nctx *NodeContext) (*NodeResult, error) {
	r.mu.Lock()
	r.counts[nctx.Node.ID]++
	n := r.counts[nctx.Node.ID]
	r.mu.Unlock()
	code := 0
	if r.exitFor != nil {
		code = r.exitFor(nctx.Node.ID, n)
	}
	return &NodeResult{Status: StatusSucceeded, ExitCode: code, Output: nctx.Node.ID}, nil
}

func (r *loopCountingRunner) count(nodeID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[nodeID]
}

// fixLoopYAML is the canonical self-healing loop topology: review -> verdict
// routes back to fixer via a counting edge until the loop quota trips the
// on_exhausted orphan.
const fixLoopYAML = `
name: fix-loop-circuit
loops:
  - id: fix_loop
    nodes: [review, verdict, fixer]
    max_iterations: 2
    on_exhausted: fallback
nodes:
  - id: coding
    type: command
    command: "echo code"
  - id: review
    type: command
    command: "echo review"
    depends:
      - node: coding
      - node: fixer
    join: always
  - id: verdict
    type: command
    command: "echo verdict"
    depends:
      - node: review
  - id: fixer
    type: command
    command: "echo fix"
    depends:
      - node: verdict
        when: "nodes.verdict.exit_code == 0"
        counts_loop: fix_loop
  - id: fallback
    type: command
    command: "echo fallback"
`

func TestLoopCountingCircuitBreakActivatesOnExhausted(t *testing.T) {
	defn, err := ParseDefinition([]byte(fixLoopYAML))
	require.NoError(t, err)

	runner := newLoopCountingRunner(func(nodeID string, n int) int { return 0 })
	engine := NewEngineWithRunner(runner)

	res, err := engine.Execute(context.Background(), defn, RunContext{SessionID: "circuit-session"})
	require.NoError(t, err)

	assert.Equal(t, RunStatusCompleted, res.Status)
	assert.Equal(t, 1, runner.count("coding"))
	assert.Equal(t, 3, runner.count("review"), "review re-runs after every fix attempt")
	assert.Equal(t, 3, runner.count("verdict"))
	assert.Equal(t, 2, runner.count("fixer"), "fixer admitted exactly max_iterations times")
	assert.Equal(t, 1, runner.count("fallback"), "on_exhausted orphan activated once")
	assert.Equal(t, StatusSucceeded, res.Nodes["fallback"].Status)
}

func TestHappyPathSweepsDormantOrphanAsCompleted(t *testing.T) {
	defn, err := ParseDefinition([]byte(fixLoopYAML))
	require.NoError(t, err)

	// verdict exits 1 (PASS): no fix needed, loop branch never fires.
	runner := newLoopCountingRunner(func(nodeID string, n int) int {
		if nodeID == "verdict" {
			return 1
		}
		return 0
	})
	engine := NewEngineWithRunner(runner)

	res, err := engine.Execute(context.Background(), defn, RunContext{SessionID: "happy-session"})
	require.NoError(t, err)

	assert.Equal(t, RunStatusCompleted, res.Status, "dormant orphan must not fail the run")
	assert.Equal(t, 0, runner.count("fixer"))
	assert.Equal(t, 0, runner.count("fallback"), "on_exhausted orphan must not run as a root node")
	require.NotNil(t, res.Nodes["fallback"])
	assert.Equal(t, StatusSkipped, res.Nodes["fallback"].Status)
	assert.Equal(t, SkipReasonNeverActivated, res.Nodes["fallback"].SkipReason)
	assert.Equal(t, StatusSkipped, res.Nodes["fixer"].Status)
	assert.Equal(t, SkipReasonConditionFalse, res.Nodes["fixer"].SkipReason)
}

func TestExhaustedHumanAbortSettlesCanceled(t *testing.T) {
	yamlSpec := `
name: abort-workflow
loops:
  - id: fix_loop
    nodes: [review, verdict, fixer]
    max_iterations: 1
    on_exhausted: fix_fallback
nodes:
  - id: coding
    type: command
    command: "echo code"
  - id: review
    type: command
    command: "echo review"
    depends:
      - node: coding
      - node: fixer
    join: always
  - id: verdict
    type: command
    command: "exit 0"
    depends:
      - node: review
  - id: fixer
    type: command
    command: "echo fix"
    depends:
      - node: verdict
        when: "nodes.verdict.exit_code == 0"
        counts_loop: fix_loop
  - id: fix_fallback
    type: human
    prompt: "Auto-fix exhausted. Retry, skip or abort?"
    options: ["Retry (reset counter)", "Skip This Step", "Abort Workflow"]
`
	defn, err := ParseDefinition([]byte(yamlSpec))
	require.NoError(t, err)

	runner := newLoopCountingRunner(nil)
	engine := NewEngineWithRunner(runner)
	rec := &suspendRecorder{}
	engine.SetHumanSuspender(func(req SuspendRequest) error {
		rec.record(req)
		return nil
	})

	// Background responder: answer the fallback suspension with an abort.
	replied := false
	stopCh := make(chan struct{})
	defer close(stopCh)
	go func() {
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(10 * time.Millisecond):
				rec.mu.Lock()
				pending := len(rec.requests)
				rec.mu.Unlock()
				if pending > 0 && !replied {
					replied = true
					req := rec.all()[0]
					_, _ = engine.Resume(context.Background(), req.RunID, "Abort Workflow")
					return
				}
			}
		}
	}()

	res, err := engine.Execute(context.Background(), defn, RunContext{SessionID: "abort-session", RunID: "run-abort"})
	require.NoError(t, err)

	assert.Equal(t, RunStatusCanceled, res.Status, "Abort Workflow reply must settle CANCELLED, never COMPLETED")
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Error(), "aborted by user")
	assert.Equal(t, 1, runner.count("fixer"))
	require.NotNil(t, res.Nodes["fix_fallback"])
	assert.Equal(t, StatusSucceeded, res.Nodes["fix_fallback"].Status)
	assert.Equal(t, "Abort Workflow", res.Nodes["fix_fallback"].Output)
}

func TestExhaustedRetryResetsLoopCounter(t *testing.T) {
	yamlSpec := `
name: retry-reset
loops:
  - id: fix_loop
    nodes: [review, verdict, fixer]
    max_iterations: 2
    on_exhausted: manual_retry
nodes:
  - id: coding
    type: command
    command: "echo code"
  - id: review
    type: command
    command: "echo review"
    depends:
      - node: coding
      - node: fixer
    join: always
  - id: verdict
    type: command
    command: "echo verdict"
    allowed_exit_codes: [0, 1]
    depends:
      - node: review
  - id: fixer
    type: command
    command: "echo fix"
    depends:
      - node: verdict
        when: "nodes.verdict.exit_code == 0"
        counts_loop: fix_loop
      - node: manual_retry
        when: "nodes.manual_retry.exit_code == 0"
        resets_loop: fix_loop
    join: always
  - id: manual_retry
    type: command
    command: "echo retry"
`
	defn, err := ParseDefinition([]byte(yamlSpec))
	require.NoError(t, err)

	// verdict demands a fix three times, then passes.
	runner := newLoopCountingRunner(func(nodeID string, n int) int {
		if nodeID == "verdict" && n > 3 {
			return 1
		}
		return 0
	})
	engine := NewEngineWithRunner(runner)

	res, err := engine.Execute(context.Background(), defn, RunContext{SessionID: "retry-session"})
	require.NoError(t, err)

	assert.Equal(t, RunStatusCompleted, res.Status)
	assert.Equal(t, 3, runner.count("fixer"), "resets_loop edge must re-admit fixer after exhaustion")
	assert.Equal(t, 1, runner.count("manual_retry"))
	assert.Equal(t, 4, runner.count("verdict"))
}

func TestNestedLoopInnerCounterResetsOnOuterAdvance(t *testing.T) {
	yamlSpec := `
name: nested-loops
loops:
  - id: outer
    nodes: [step, check, fixer, pend]
    max_iterations: 2
  - id: inner
    parent: outer
    nodes: [check, fixer]
    max_iterations: 2
    on_exhausted: inner_fb
nodes:
  - id: kickoff
    type: command
    command: "echo kickoff"
  - id: step
    type: command
    command: "echo step"
    depends:
      - node: kickoff
      - node: pend
        when: "nodes.pend.exit_code == 0"
        counts_loop: outer
    join: always
  - id: check
    type: command
    command: "echo check"
    depends:
      - node: step
      - node: fixer
    join: always
  - id: fixer
    type: command
    command: "echo fix"
    depends:
      - node: check
        when: "nodes.check.exit_code == 0"
        counts_loop: inner
  - id: pend
    type: command
    command: "echo pend"
    depends:
      - node: check
        when: "nodes.check.exit_code == 1"
      - node: inner_fb
    join: always
  - id: inner_fb
    type: command
    command: "echo inner fallback"
`
	defn, err := ParseDefinition([]byte(yamlSpec))
	require.NoError(t, err)

	// check always demands an inner fix; inner exhausts after 2 attempts and
	// inner_fb advances the outer loop, which must reset the inner counter.
	// The outer loop itself has no on_exhausted: its quota breach must fail
	// the re-entry target (step) and settle the run FAILED (fail-closed)
	// without re-driving the join: always downstream (no livelock).
	runner := newLoopCountingRunner(func(nodeID string, n int) int { return 0 })
	engine := NewEngineWithRunner(runner)

	res, err := engine.Execute(context.Background(), defn, RunContext{SessionID: "nested-session"})
	require.NoError(t, err)

	assert.Equal(t, RunStatusFailed, res.Status, "outer loop exhaustion without on_exhausted must settle FAILED")
	require.NotNil(t, res.Nodes["step"])
	assert.Equal(t, StatusFailed, res.Nodes["step"].Status)
	require.NotNil(t, res.Nodes["step"].Error)
	assert.Contains(t, res.Nodes["step"].Error.Error(), "loop \"outer\" exhausted")
	assert.Equal(t, 6, runner.count("fixer"), "inner loop must run 2 fixes per outer iteration (3 outer steps)")
	assert.Equal(t, 3, runner.count("inner_fb"), "inner loop exhausts once per outer iteration")
	assert.Equal(t, 3, runner.count("step"))
	assert.Equal(t, 9, runner.count("check"))
}

func TestCascadeSkipGuardBlocksConditionalEdge(t *testing.T) {
	yamlSpec := `
name: cascade-guard
nodes:
  - id: reviewer
    type: command
    command: "echo review"
  - id: verdict
    type: command
    command: "echo verdict"
    depends:
      - node: reviewer
  - id: fixer
    type: command
    command: "echo fix"
    depends:
      - node: verdict
        when: "nodes.verdict.exit_code == 0"
`
	// reviewer crashes: verdict is cascade-skipped; its zero-valued exit code
	// must not satisfy fixer's `exit_code == 0` condition.
	res, stub := runEngine(t, yamlSpec, map[string]stubOutcome{
		"reviewer": {exitCode: 1},
	})

	assert.Equal(t, RunStatusFailed, res.Status)
	require.NotNil(t, res.Nodes["verdict"])
	assert.Equal(t, StatusSkipped, res.Nodes["verdict"].Status)
	assert.Equal(t, SkipReasonCascadedFailure, res.Nodes["verdict"].SkipReason)
	assert.False(t, stub.hasRun("verdict"))
	assert.False(t, stub.hasRun("fixer"), "cascade-skipped parent must never trigger a conditional edge")
	require.NotNil(t, res.Nodes["fixer"])
	assert.Equal(t, StatusSkipped, res.Nodes["fixer"].Status)
}

func TestSeedReplaySuppressesReenqueueAndCounting(t *testing.T) {
	defn, err := ParseDefinition([]byte(fixLoopYAML))
	require.NoError(t, err)

	runner := newLoopCountingRunner(func(nodeID string, n int) int { return 0 })
	engine := NewEngineWithRunner(runner)

	// Simulate a restored snapshot: the loop already executed one fix round
	// and everything settled except the on_exhausted orphan.
	res, err := engine.Execute(context.Background(), defn, RunContext{
		SessionID: "replay-session",
		SeedNodes: map[string]*NodeResult{
			"coding":  {Status: StatusSucceeded, ExitCode: 0},
			"review":  {Status: StatusSucceeded, ExitCode: 0},
			"verdict": {Status: StatusSucceeded, ExitCode: 0},
			"fixer":   {Status: StatusSucceeded, ExitCode: 0},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, RunStatusCompleted, res.Status)
	assert.Equal(t, 0, runner.count("coding"), "seeded nodes must not re-execute on replay")
	assert.Equal(t, 0, runner.count("review"))
	assert.Equal(t, 0, runner.count("verdict"))
	assert.Equal(t, 0, runner.count("fixer"))
	assert.Equal(t, 0, runner.count("fallback"))
	require.NotNil(t, res.Nodes["fallback"])
	assert.Equal(t, SkipReasonNeverActivated, res.Nodes["fallback"].SkipReason)
}

func TestMaxNodeExecutionsDefinitionCap(t *testing.T) {
	yamlSpec := `
name: max-exec-cap
max_node_executions: 2
nodes:
  - id: kickoff
    type: command
    command: "echo start"
  - id: spin
    type: command
    command: "echo spin"
    depends:
      - node: kickoff
      - node: spin
        when: "nodes.spin.exit_code == 0"
    join: always
`
	defn, err := ParseDefinition([]byte(yamlSpec))
	require.NoError(t, err)

	runner := newLoopCountingRunner(nil)
	engine := NewEngineWithRunner(runner)

	res, err := engine.Execute(context.Background(), defn, RunContext{SessionID: "cap-session"})
	require.NoError(t, err)

	assert.Equal(t, RunStatusCompleted, res.Status)
	assert.Equal(t, 2, runner.count("spin"), "definition-level max_node_executions caps re-entry")
}
