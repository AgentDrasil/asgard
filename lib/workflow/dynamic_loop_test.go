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
}
