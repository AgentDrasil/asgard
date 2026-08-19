package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// devWorkflowE2EYAML mirrors the production tmp/agents/dev-workflow/workflow.yaml
// Phase 3/4/5 topology: the per-step loop with its inner self-healing fix loop,
// the fix_fallback human orphan, mark_step_skipped and the Phase 5 final chain.
// Agent nodes are stubbed; command nodes run real shell commands against the
// run tmp dir so verdict routing, todo advancement and the sed skip flip are
// exercised end-to-end.
const devWorkflowE2EYAML = `
name: dev-workflow-e2e
tmp_dir: "tmp/${session_id}"
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
nodes:
  - id: planner_stub
    type: agent
    agent_id: planner_stub
    entry: true
  - id: plan_approval
    type: human
    depends:
      - node: planner_stub
    prompt: "Approve the plan?"
    options: ["Approve", "Request Changes"]
  - id: coding_agent
    type: agent
    agent_id: coding_agent
    depends:
      - node: plan_approval
        when: "nodes.plan_approval.output == 'Approve'"
      - node: check_pending_steps
        when: "nodes.check_pending_steps.exit_code == 0"
        counts_loop: step_loop
    join: always
  - id: commit_agent
    type: agent
    agent_id: commit_agent
    depends:
      - node: coding_agent
  - id: code_review_agent
    type: agent
    agent_id: code_review_agent
    depends:
      - node: commit_agent
      - node: fix_agent
    join: always
  - id: check_review_verdict
    type: command
    sandbox: false
    command: >
      if grep -qx 'FIX_REQUIRED' ${tmp_dir}/review_verdict.txt; then exit 0;
      elif grep -qx 'PASS' ${tmp_dir}/review_verdict.txt; then exit 1;
      else exit 2; fi
    allowed_exit_codes: [0, 1]
    depends:
      - node: code_review_agent
  - id: fix_agent
    type: agent
    agent_id: fix_agent
    depends:
      - node: check_review_verdict
        when: "nodes.check_review_verdict.exit_code == 0"
        counts_loop: fix_loop
      - node: fix_fallback
        when: "nodes.fix_fallback.output == 'Retry (reset counter)'"
        resets_loop: fix_loop
    join: always
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
    prompt: "Auto-fix exhausted after 5 attempts for the current step."
    options: ["Retry (reset counter)", "Skip This Step", "Abort Workflow"]
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
    agent_id: final_cleaner
    depends:
      - node: check_pending_steps
        when: "nodes.check_pending_steps.exit_code == 1"
      - node: final_approval
        when: "nodes.final_approval.output == 'Request Changes'"
    join: always
  - id: final_commit
    type: agent
    agent_id: final_commit
    depends:
      - node: final_cleaner
  - id: final_approval
    type: human
    depends:
      - node: final_commit
    prompt: "Accept the final delivery?"
    options: ["Accept & Deliver", "Request Changes"]
    output_file: "final_decision.txt"
`

// e2eAgentRunner stubs the agent nodes of the dev-workflow topology.
type e2eAgentRunner struct {
	mu         sync.Mutex
	counts     map[string]int
	totalSteps int
	verdictFor func(reviewN int) string
}

func (r *e2eAgentRunner) Supports(t NodeType) bool { return t == NodeTypeAgent }

func (r *e2eAgentRunner) Run(ctx context.Context, nctx *NodeContext) (*NodeResult, error) {
	r.mu.Lock()
	if r.counts == nil {
		r.counts = map[string]int{}
	}
	r.counts[nctx.Node.ID]++
	n := r.counts[nctx.Node.ID]
	r.mu.Unlock()

	switch nctx.Node.ID {
	case "planner_stub":
		requireErr := os.MkdirAll(filepath.Join(nctx.TmpDir, "plan"), 0o755)
		if requireErr != nil {
			return nil, requireErr
		}
		var b strings.Builder
		b.WriteString("steps:\n")
		for i := 1; i <= r.totalSteps; i++ {
			fmt.Fprintf(&b, "  - id: step-%d\n    status: pending\n", i)
		}
		if err := os.WriteFile(filepath.Join(nctx.TmpDir, "plan", "todo.yaml"), []byte(b.String()), 0o644); err != nil {
			return nil, err
		}
	case "coding_agent":
		// Mark any prior in_review step completed, then flip the first
		// pending step to in_review (mirrors the coder agent contract).
		path := filepath.Join(nctx.TmpDir, "plan", "todo.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		s := strings.Replace(string(data), "status: in_review", "status: completed", 1)
		s = strings.Replace(s, "status: pending", "status: in_review", 1)
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			return nil, err
		}
	case "code_review_agent":
		verdict := "PASS"
		if r.verdictFor != nil {
			verdict = r.verdictFor(n)
		}
		if err := os.WriteFile(filepath.Join(nctx.TmpDir, "review_verdict.txt"), []byte(verdict+"\n"), 0o644); err != nil {
			return nil, err
		}
	}
	return &NodeResult{Status: StatusSucceeded, ExitCode: 0, Output: nctx.Node.ID}, nil
}

func (r *e2eAgentRunner) count(nodeID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[nodeID]
}

// replyScript maps human node IDs to the ordered replies the responder
// delivers (the last entry repeats when exhausted).
type replyScript map[string][]string

type e2eOutcome struct {
	result *WorkflowRunResult
	runner *e2eAgentRunner
	store  *memStore
	runID  string
	runDir string
	tmpDir string
	waited chan string // receives node IDs as suspensions occur
}

// runDevWorkflowE2E drives the e2e topology with the given step count, review
// verdict schedule and human reply script, returning the settled outcome.
func runDevWorkflowE2E(t *testing.T, totalSteps int, verdictFor func(reviewN int) string, script replyScript) *e2eOutcome {
	t.Helper()
	defn, err := ParseDefinition([]byte(devWorkflowE2EYAML))
	require.NoError(t, err)

	runner := &e2eAgentRunner{totalSteps: totalSteps, verdictFor: verdictFor}
	registry := NewNodeRunnerRegistry()
	registry.Register(NewCommandRunner(false))
	registry.Register(runner)
	store := newMemStore()
	rec := &suspendRecorder{}
	waited := make(chan string, 32)
	engine := NewEngine(registry)
	engine.SetRunStore(store)
	engine.SetHumanSuspender(func(req SuspendRequest) error {
		rec.record(req)
		select {
		case waited <- req.NodeID:
		default:
		}
		return nil
	})

	runID := "run-" + strings.ReplaceAll(t.Name(), "/", "-")
	runDir := t.TempDir()
	sessionID := "e2e-" + runID

	type result struct {
		res *WorkflowRunResult
		err error
	}
	outCh := make(chan result, 1)
	go func() {
		res, err := engine.Execute(context.Background(), defn, RunContext{
			SessionID: sessionID,
			RunID:     runID,
			RunDir:    runDir,
		})
		outCh <- result{res: res, err: err}
	}()

	// Human responder: replies to each suspension per the script.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		replied := map[string]int{}
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				for _, req := range rec.all() {
					n := replied[req.NodeID]
					if n >= len(script[req.NodeID]) {
						continue
					}
					replies := script[req.NodeID]
					reply := replies[min(n, len(replies)-1)]
					replied[req.NodeID] = n + 1
					_, _ = engine.Resume(context.Background(), req.RunID, reply)
				}
			}
		}
	}()

	select {
	case out := <-outCh:
		require.NoError(t, out.err)
		require.NotNil(t, out.res)
		return &e2eOutcome{
			result: out.res,
			runner: runner,
			store:  store,
			runID:  runID,
			runDir: runDir,
			tmpDir: filepath.Join(runDir, "tmp", sessionID),
			waited: waited,
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("dev-workflow e2e run %s did not settle in time", runID)
		return nil
	}
}

func (o *e2eOutcome) suspendedNodes() []string {
	var nodes []string
	for _, entry := range o.store.order {
		if strings.HasPrefix(entry, "wait:") {
			nodes = append(nodes, entry)
		}
	}
	return nodes
}

// Scenario A: two steps, both pass review on the first attempt; the loop
// advances and lands in Phase 5 which the user accepts.
func TestDevWorkflowE2EScenarioA_MultiStepPassAndDeliver(t *testing.T) {
	out := runDevWorkflowE2E(t, 2, nil, replyScript{
		"plan_approval":  {"Approve"},
		"final_approval": {"Accept & Deliver"},
	})

	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Equal(t, 2, out.runner.count("coding_agent"), "both steps developed")
	assert.Equal(t, 2, out.runner.count("code_review_agent"))
	assert.Equal(t, 0, out.runner.count("fix_agent"))
	assert.Equal(t, 1, out.runner.count("final_cleaner"))
	require.NotNil(t, out.result.Nodes["check_pending_steps"])
	assert.Equal(t, 1, out.result.Nodes["check_pending_steps"].ExitCode, "no pending steps remain")
}

// Scenario B: one step triggers two automatic fixes, then passes.
func TestDevWorkflowE2EScenarioB_SelfHealAfterTwoFixes(t *testing.T) {
	out := runDevWorkflowE2E(t, 1, func(n int) string {
		if n <= 2 {
			return "FIX_REQUIRED"
		}
		return "PASS"
	}, replyScript{
		"plan_approval":  {"Approve"},
		"final_approval": {"Accept & Deliver"},
	})

	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Equal(t, 2, out.runner.count("fix_agent"), "exactly two self-healing fix rounds")
	assert.Equal(t, 3, out.runner.count("code_review_agent"))
}

// Scenario C: five consecutive fix failures trip the circuit breaker and wake
// the fix_fallback human orphan (Skip This Step with no steps left routes
// straight to Phase 5).
func TestDevWorkflowE2EScenarioC_ExhaustionWakesFallback(t *testing.T) {
	out := runDevWorkflowE2E(t, 1, func(n int) string { return "FIX_REQUIRED" }, replyScript{
		"plan_approval":  {"Approve"},
		"fix_fallback":   {"Skip This Step"},
		"final_approval": {"Accept & Deliver"},
	})

	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Contains(t, out.suspendedNodes(), "wait:"+out.runID, "run suspended at the fallback human node")
	assert.Equal(t, 5, out.runner.count("fix_agent"), "fixer admitted exactly max_iterations times")
	assert.Equal(t, 6, out.runner.count("code_review_agent"))
	require.NotNil(t, out.result.Nodes["mark_step_skipped"])
	assert.Equal(t, StatusSucceeded, out.result.Nodes["mark_step_skipped"].Status)

	todo, err := os.ReadFile(filepath.Join(out.tmpDir, "plan", "todo.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(todo), "status: skipped (known-broken)")
}

// Scenario D: the fallback Retry resets the loop counter and the next fix
// attempt heals the step.
func TestDevWorkflowE2EScenarioD_FallbackRetryResetsAndHeals(t *testing.T) {
	out := runDevWorkflowE2E(t, 1, func(n int) string {
		if n <= 6 {
			return "FIX_REQUIRED"
		}
		return "PASS"
	}, replyScript{
		"plan_approval":  {"Approve"},
		"fix_fallback":   {"Retry (reset counter)"},
		"final_approval": {"Accept & Deliver"},
	})

	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Equal(t, 6, out.runner.count("fix_agent"), "5 exhausted attempts plus 1 post-retry fix")
	assert.Equal(t, 7, out.runner.count("code_review_agent"))
}

// Scenario E: Skip This Step keeps the broken commit, marks the todo entry
// known-broken and advances to the remaining step.
func TestDevWorkflowE2EScenarioE_SkipThisStepAdvances(t *testing.T) {
	out := runDevWorkflowE2E(t, 2, func(n int) string {
		if n <= 6 {
			return "FIX_REQUIRED"
		}
		return "PASS"
	}, replyScript{
		"plan_approval":  {"Approve"},
		"fix_fallback":   {"Skip This Step"},
		"final_approval": {"Accept & Deliver"},
	})

	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Equal(t, 5, out.runner.count("fix_agent"), "first step exhausted its fix quota")
	assert.Equal(t, 2, out.runner.count("coding_agent"), "second step still developed")

	todo, err := os.ReadFile(filepath.Join(out.tmpDir, "plan", "todo.yaml"))
	require.NoError(t, err)
	s := string(todo)
	assert.Contains(t, s, "status: skipped (known-broken)", "broken step is marked known-broken")
	assert.Contains(t, s, "status: in_review", "second step was picked up after the skip")
}

// Scenario F: Abort Workflow settles the run CANCELLED, never COMPLETED.
func TestDevWorkflowE2EScenarioF_AbortSettlesCanceled(t *testing.T) {
	out := runDevWorkflowE2E(t, 1, func(n int) string { return "FIX_REQUIRED" }, replyScript{
		"plan_approval": {"Approve"},
		"fix_fallback":  {"Abort Workflow"},
	})

	assert.Equal(t, RunStatusCanceled, out.result.Status)
	require.NotNil(t, out.result.Error)
	assert.Contains(t, out.result.Error.Error(), "aborted by user")
	assert.Equal(t, 5, out.runner.count("fix_agent"))
}

// Scenario G: after all steps complete, Phase 5 runs the final chain and the
// dormant fallback branch is swept as never-activated on the happy path.
func TestDevWorkflowE2EScenarioG_Phase5HappyPathSweepsOrphans(t *testing.T) {
	out := runDevWorkflowE2E(t, 1, nil, replyScript{
		"plan_approval":  {"Approve"},
		"final_approval": {"Accept & Deliver"},
	})

	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Equal(t, 1, out.runner.count("final_cleaner"))
	assert.Equal(t, 1, out.runner.count("final_commit"))
	require.NotNil(t, out.result.Nodes["fix_fallback"])
	assert.Equal(t, StatusSkipped, out.result.Nodes["fix_fallback"].Status)
	assert.Equal(t, SkipReasonNeverActivated, out.result.Nodes["fix_fallback"].SkipReason)
	require.NotNil(t, out.result.Nodes["mark_step_skipped"])
	assert.Equal(t, SkipReasonNeverActivated, out.result.Nodes["mark_step_skipped"].SkipReason)
}

// Scenario H: a Request Changes rejection loops back to final_cleaner, which
// re-converges and is accepted on the second round.
func TestDevWorkflowE2EScenarioH_FinalRejectionReconverges(t *testing.T) {
	out := runDevWorkflowE2E(t, 1, nil, replyScript{
		"plan_approval":  {"Approve"},
		"final_approval": {"Request Changes", "Accept & Deliver"},
	})

	assert.Equal(t, RunStatusCompleted, out.result.Status)
	assert.Equal(t, 2, out.runner.count("final_cleaner"), "rejection re-drives the final cleaner")
	assert.Equal(t, 2, out.runner.count("final_commit"))
}
