package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

// NodeAction tells the scheduler whether a ready node should run or be skipped.
type NodeAction int

const (
	ActionRun NodeAction = iota
	ActionSkip
)

// RunContext carries per-invocation runtime data into Engine.Execute.
type RunContext struct {
	// SessionID identifies the workflow run (defaults to a generated UUID).
	SessionID string
	// RunDir is the base working directory for the run.
	RunDir string
	// Input is the user prompt that triggered the run (interpolated as
	// ${input} / ${prompt}).
	Input string
	// EmitEvent receives engine lifecycle events; may be nil.
	EmitEvent func(event WorkflowEvent)
}

// Engine schedules workflow DAGs with fork-join parallelism.
type Engine struct {
	registry *NodeRunnerRegistry
}

// NewEngine creates an engine backed by the given runner registry.
func NewEngine(registry *NodeRunnerRegistry) *Engine {
	return &Engine{registry: registry}
}

// Registry exposes the engine's runner registry.
func (e *Engine) Registry() *NodeRunnerRegistry {
	return e.registry
}

// EvaluateNodeReadiness decides whether a node whose dependencies have all
// settled should run, be skipped, and with which SkipReason. It precisely
// distinguishes intentional condition skips from cascaded failure skips:
//
//   - SkipReasonConditionFalse: a dependency edge had `when: false`, or an
//     upstream was intentionally skipped (condition-false).
//   - SkipReasonCascadedFailure: an upstream FAILED or was itself skipped by
//     cascaded failure, and the node does not opt in via on_fail/on_skip/join.
func EvaluateNodeReadiness(node *NodeSpec, upstreams map[string]*NodeResult) (NodeAction, SkipReason) {
	hasConditionFalseEdge := false
	hasCascadedFailureEdge := false
	hasFailedEdge := false

	for _, dep := range node.Depends {
		parentResult := upstreams[dep.NodeID]

		// 1. A dependency edge with an explicit `when` only arbitrates that
		// edge; parent failure does not block a matching when-branch.
		if dep.When != "" {
			match, err := EvaluateSimpleExpr(dep.When, upstreams, nil)
			if err != nil {
				log.Warn().Err(err).Str("node", node.ID).Msgf("when expression %q evaluation failed; treating as false", dep.When)
				match = false
			}
			if !match {
				hasConditionFalseEdge = true
			}
			continue
		}

		// 2. Default dependency edge: inspect upstream status / skip reason.
		if parentResult == nil {
			continue
		}
		switch parentResult.Status {
		case StatusFailed:
			hasFailedEdge = true
		case StatusSkipped:
			if parentResult.SkipReason == SkipReasonCascadedFailure {
				hasCascadedFailureEdge = true
			} else {
				hasConditionFalseEdge = true
			}
		}
	}

	// 3. Settlement (priority: Failed / CascadedFailure > ConditionFalse).
	if (hasFailedEdge || hasCascadedFailureEdge) && !node.AllowsFail() {
		return ActionSkip, SkipReasonCascadedFailure
	}
	if hasConditionFalseEdge && !node.AllowsSkip() {
		return ActionSkip, SkipReasonConditionFalse
	}

	return ActionRun, ""
}

// Execute runs the whole DAG and returns the settled run result. Node worker
// failures never cancel sibling workers (errgroup receives nil); only
// cancellation of ctx (user cancel) broadcasts to all workers.
func (e *Engine) Execute(ctx context.Context, defn *WorkflowDefinition, rc RunContext) (*WorkflowRunResult, error) {
	if err := defn.Validate(); err != nil {
		return nil, err
	}

	if rc.SessionID == "" {
		rc.SessionID = uuid.Must(uuid.NewV7()).String()
	}
	if rc.RunDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolving default run dir: %w", err)
		}
		rc.RunDir = wd
	}

	artifactsDir := Interpolate(defn.ArtifactsDir, func(key string) (string, bool) {
		if key == "session_id" {
			return rc.SessionID, true
		}
		return "", false
	})
	if artifactsDir == "" {
		artifactsDir = filepath.Join(rc.RunDir, ".artifacts", rc.SessionID)
	}
	if !filepath.IsAbs(artifactsDir) {
		artifactsDir = filepath.Join(rc.RunDir, artifactsDir)
	}
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating artifacts dir: %w", err)
	}

	emit := func(ev WorkflowEvent) {
		if rc.EmitEvent == nil {
			return
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now()
		}
		ev.Workflow = defn.Name
		ev.SessionID = rc.SessionID
		rc.EmitEvent(ev)
	}
	emit(WorkflowEvent{Type: EventWorkflowStarted, Message: fmt.Sprintf("workflow %s started", defn.Name)})

	g, gctx := errgroup.WithContext(ctx)

	var mu sync.Mutex
	results := make(map[string]*NodeResult, len(defn.Nodes))
	dones := make(map[string]chan struct{}, len(defn.Nodes))
	for _, node := range defn.Nodes {
		dones[node.ID] = make(chan struct{})
	}
	values := &RunValues{}

	for _, node := range defn.Nodes {
		g.Go(func() error {
			defer close(dones[node.ID])

			// Fork-Join: wait until every direct dependency has settled.
			for _, dep := range node.Depends {
				select {
				case <-dones[dep.NodeID]:
				case <-gctx.Done():
					return nil
				}
			}
			if gctx.Err() != nil {
				return nil
			}

			mu.Lock()
			upstreams := make(map[string]*NodeResult, len(results))
			for k, v := range results {
				upstreams[k] = v
			}
			mu.Unlock()

			action, reason := EvaluateNodeReadiness(node, upstreams)
			if action == ActionSkip {
				res := &NodeResult{Status: StatusSkipped, SkipReason: reason}
				mu.Lock()
				results[node.ID] = res
				mu.Unlock()
				emit(WorkflowEvent{
					Type:       EventNodeSkipped,
					NodeID:     node.ID,
					NodeType:   node.Type,
					Status:     StatusSkipped,
					SkipReason: reason,
					Message:    fmt.Sprintf("node %s skipped (%s)", node.ID, reason),
				})
				return nil
			}

			runner, ok := e.registry.Get(node.Type)
			if !ok {
				err := fmt.Errorf("no runner registered for node type %q", node.Type)
				mu.Lock()
				results[node.ID] = &NodeResult{Status: StatusFailed, Error: err}
				mu.Unlock()
				emit(WorkflowEvent{Type: EventNodeFinished, NodeID: node.ID, NodeType: node.Type, Status: StatusFailed, Message: err.Error()})
				return nil
			}

			emit(WorkflowEvent{Type: EventNodeStarted, NodeID: node.ID, NodeType: node.Type, Status: StatusRunning, Message: fmt.Sprintf("node %s started", node.ID)})

			nctx := &NodeContext{
				SessionID:    rc.SessionID,
				RunDir:       rc.RunDir,
				ArtifactsDir: artifactsDir,
				Input:        rc.Input,
				Defn:         defn,
				Node:         node,
				Upstreams:    upstreams,
				EventEmitter: emit,
				Values:       values,
			}

			result, err := runner.Run(gctx, nctx)
			result = normalizeResult(result, err)

			mu.Lock()
			results[node.ID] = result
			mu.Unlock()

			msg := fmt.Sprintf("node %s %s", node.ID, result.Status)
			if result.Error != nil {
				msg = fmt.Sprintf("node %s %s: %v", node.ID, result.Status, result.Error)
			}
			emit(WorkflowEvent{
				Type:     EventNodeFinished,
				NodeID:   node.ID,
				NodeType: node.Type,
				Status:   result.Status,
				Message:  msg,
			})
			// Node-level failures are recorded in results only; returning nil
			// keeps the errgroup context alive so sibling nodes keep running.
			return nil
		})
	}

	_ = g.Wait()

	run := &WorkflowRunResult{Nodes: results}
	if ctx.Err() != nil {
		run.Status = RunStatusCanceled
		run.Error = ctx.Err()
	} else {
		run.Status = settleGlobalStatus(defn, results)
	}

	emit(WorkflowEvent{
		Type:    EventWorkflowFinished,
		Status:  NodeStatus(run.Status),
		Message: fmt.Sprintf("workflow %s %s", defn.Name, run.Status),
	})
	return run, nil
}

// normalizeResult coerces runner output into a well-formed NodeResult.
func normalizeResult(result *NodeResult, err error) *NodeResult {
	if result == nil {
		result = &NodeResult{}
	}
	if err != nil {
		result.Status = StatusFailed
		result.Error = err
		return result
	}
	if result.Status == "" {
		result.Status = StatusSucceeded
	}
	if result.Status == StatusFailed && result.Error == nil {
		result.Error = fmt.Errorf("node failed with exit code %d", result.ExitCode)
	}
	return result
}

// settleGlobalStatus computes the final workflow status:
//
//   - FAILED if any node was skipped by cascaded failure, or if any FAILED
//     node was not absorbed by a downstream when-branch that succeeded.
//   - COMPLETED otherwise (condition-false skips are considered success).
func settleGlobalStatus(defn *WorkflowDefinition, results map[string]*NodeResult) WorkflowRunStatus {
	dependentsWithWhen := make(map[string][]string)
	for _, node := range defn.Nodes {
		for _, dep := range node.Depends {
			if dep.When != "" {
				dependentsWithWhen[dep.NodeID] = append(dependentsWithWhen[dep.NodeID], node.ID)
			}
		}
	}

	failureHandled := func(nodeID string) bool {
		for _, dep := range dependentsWithWhen[nodeID] {
			if res := results[dep]; res != nil && res.Status == StatusSucceeded {
				return true
			}
		}
		return false
	}

	for _, node := range defn.Nodes {
		res := results[node.ID]
		if res == nil {
			return RunStatusFailed
		}
		if res.Status == StatusSkipped && res.SkipReason == SkipReasonCascadedFailure {
			return RunStatusFailed
		}
		if res.Status == StatusFailed && !failureHandled(node.ID) {
			return RunStatusFailed
		}
	}
	return RunStatusCompleted
}
