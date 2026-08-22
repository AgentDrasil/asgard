package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
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
	// RunID is the persistent run identifier recorded in the RunStore
	// (defaults to a generated UUID). It is reused on resume so the
	// deterministic human MessageIDs stay stable across restarts.
	RunID string
	// RunDir is the base working directory for the run.
	RunDir string
	// Input is the user prompt that triggered the run (interpolated as
	// ${input} / ${prompt}).
	Input string
	// AgentName names the workflow agent for chat routing of human nodes.
	AgentName string
	// DAGSpec overrides the raw YAML snapshot persisted for this run
	// (defaults to the definition's source).
	DAGSpec string
	// Store persists run snapshots; nil falls back to the engine-level store.
	Store RunStore
	// SuspendHuman delivers human-node suspensions to the host application;
	// nil falls back to the engine-level hook.
	SuspendHuman SuspendHumanFunc
	// SeedNodes are pre-settled node results restored from a snapshot; their
	// workers complete immediately without re-execution.
	SeedNodes map[string]*workflowspec.NodeResult
	// SeedLoopIterations / SeedExecutionCounts restore the loop iteration
	// counters and per-node execution counts from a persisted snapshot so
	// circuit-breaker quotas and human MessageIDs survive restarts.
	SeedLoopIterations  map[string]int
	SeedExecutionCounts map[string]int
	// ActivateNodes are enqueued directly after seed replay; they wake the
	// suspended node (e.g. an on_exhausted orphan human node that seed
	// replay cannot reach because it has no static in-edges).
	ActivateNodes []string
	// HumanReplies maps human node IDs to pre-supplied user replies (resume).
	HumanReplies map[string]string
	// WorkflowRunDirs carries workflow/parent configured run directories.
	WorkflowRunDirs []string
	// WorkflowMountDirs carries workflow/parent configured mount directories.
	WorkflowMountDirs workflowspec.MountDirsConfig
	// Inline marks an in-process sub-workflow execution: StartRun/SettleRun
	// persistence at the top level is skipped (the parent run owns it).
	Inline bool
	// Headless marks no-interaction execution; human nodes cannot suspend.
	Headless bool
	// TmpDir explicitly inherits the parent run's temp directory when
	// non-empty (skips derivation and MkdirAll).
	TmpDir string
	// EmitEvent receives engine lifecycle events; may be nil.
	EmitEvent func(event WorkflowEvent)
	// Resume marks a re-driven run from a persisted snapshot.
	Resume bool
	// ParentRunID links a sub-workflow (inline) run to its parent run.
	ParentRunID string
	// ReuseMessageIDs maps node IDs to original MessageIDs to reuse on first re-suspension.
	ReuseMessageIDs map[string]string
}

type humanWaiter struct {
	replyCh   chan string
	nodeID    string
	messageID string
	iteration int
	runID     string
}

// Engine schedules workflow DAGs with fork-join parallelism.
type Engine struct {
	registry *NodeRunnerRegistry

	// store / suspendHuman are the engine-level persistence defaults, wired
	// once by the host application (lib/api).
	store        RunStore
	suspendHuman SuspendHumanFunc

	// waitMu guards waitingByMsg, waitingByRun, executing, and replayPending.
	waitMu        sync.Mutex
	waitingByMsg  map[string]*humanWaiter            // key: messageID -> waiter
	waitingByRun  map[string]map[string]*humanWaiter // key: runID -> nodeID -> waiter
	executing     map[string]bool                    // key: runID -> true (active execution)
	replayPending map[string]bool                    // key: runID -> true (replay initializing)
}

// NewEngine creates an engine backed by the given runner registry.
func NewEngine(registry *NodeRunnerRegistry) *Engine {
	return &Engine{
		registry:      registry,
		waitingByMsg:  make(map[string]*humanWaiter),
		waitingByRun:  make(map[string]map[string]*humanWaiter),
		executing:     make(map[string]bool),
		replayPending: make(map[string]bool),
	}
}

// SetRunStore wires the engine-level persistence store used by Resume and by
// runs whose RunContext carries no Store.
func (e *Engine) SetRunStore(store RunStore) {
	e.store = store
}

// SetHumanSuspender wires the engine-level suspension delivery hook.
func (e *Engine) SetHumanSuspender(f SuspendHumanFunc) {
	e.suspendHuman = f
}

// Registry exposes the engine's runner registry.
func (e *Engine) Registry() *NodeRunnerRegistry {
	return e.registry
}

// SetAgents preloads agents into the registered agent runner if supported.
func (e *Engine) SetAgents(agentList []*agentspec.Agent) {
	if e == nil || e.registry == nil {
		return
	}
	if runner, ok := e.registry.Get(workflowspec.NodeTypeAgent); ok {
		if preloader, ok := runner.(interface{ SetAgents([]*agentspec.Agent) }); ok {
			preloader.SetAgents(agentList)
		}
	}
}

// EvaluateNodeReadiness decides whether a node whose dependencies have all
// settled should run, be skipped, and with which SkipReason. It precisely
// distinguishes intentional condition skips from cascaded failure skips:
//
//   - SkipReasonConditionFalse: a dependency edge had `when: false`, or an
//     upstream was intentionally skipped (condition-false).
//   - SkipReasonCascadedFailure: an upstream FAILED or was itself skipped by
//     cascaded failure, and the node does not opt in via on_fail/on_skip/join.
func EvaluateNodeReadiness(node *workflowspec.NodeSpec, upstreams map[string]*workflowspec.NodeResult) (NodeAction, workflowspec.SkipReason) {
	hasConditionFalseEdge := false
	hasCascadedFailureEdge := false
	hasFailedEdge := false

	for _, dep := range node.Depends {
		parentResult := upstreams[dep.NodeID]

		// 1. A dependency edge with an explicit `when` only arbitrates that
		// edge; parent failure does not block a matching when-branch.
		if dep.When != "" {
			// Fail-closed skip guard: a skipped parent (any skip reason)
			// produced no execution result; its zero-valued exit code/output must never
			// satisfy an exit_code condition (e.g. `exit_code == 0`).
			// However, if the condition explicitly queries `status` or `skip_reason` on
			// the target node, allow evaluation against the settled skipped result.
			if parentResult != nil && parentResult.Status == workflowspec.StatusSkipped {
				_, field, ok := workflowspec.ParseExprTarget(dep.When)
				isStatusQuery := ok && (field == "status" || field == "skip_reason")
				if !isStatusQuery {
					hasConditionFalseEdge = true
					continue
				}
			}
			match, err := workflowspec.EvaluateSimpleExpr(dep.When, upstreams, nil)
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
		case workflowspec.StatusFailed:
			hasFailedEdge = true
		case workflowspec.StatusSkipped:
			if parentResult.SkipReason == workflowspec.SkipReasonCascadedFailure {
				hasCascadedFailureEdge = true
			} else {
				hasConditionFalseEdge = true
			}
		}
	}

	// 3. Settlement (priority: Failed / CascadedFailure > ConditionFalse).
	if (hasFailedEdge || hasCascadedFailureEdge) && !node.AllowsFail() {
		return ActionSkip, workflowspec.SkipReasonCascadedFailure
	}
	if hasConditionFalseEdge && !node.AllowsSkip() {
		return ActionSkip, workflowspec.SkipReasonConditionFalse
	}

	return ActionRun, ""
}

// Execute runs the whole DAG and returns the settled run result. Node worker
// failures never cancel sibling workers (errgroup receives nil); only
// cancellation of ctx (user cancel) broadcasts to all workers.
func (e *Engine) Execute(ctx context.Context, defn *workflowspec.WorkflowDefinition, rc RunContext) (*WorkflowRunResult, error) {
	if err := defn.Validate(); err != nil {
		return nil, err
	}

	if rc.SessionID == "" {
		rc.SessionID = uuid.Must(uuid.NewV7()).String()
	}
	if rc.RunID == "" {
		rc.RunID = uuid.Must(uuid.NewV7()).String()
	}
	if rc.RunDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolving default run dir: %w", err)
		}
		rc.RunDir = wd
	}

	store := rc.Store
	if store == nil {
		store = e.store
	}
	dagSpec := rc.DAGSpec
	if dagSpec == "" {
		dagSpec = defn.RawSpec()
	}

	var tmpDir string
	if rc.TmpDir != "" {
		tmpDir = rc.TmpDir
	} else {
		tmpDir = workflowspec.Interpolate(defn.TmpDir, func(key string) (string, bool) {
			switch key {
			case "session_id":
				return rc.SessionID, true
			case "run_dir":
				return rc.RunDir, true
			}
			return "", false
		})
		if tmpDir == "" {
			tmpDir = DefaultTmpDir(rc.SessionID)
		}
		if !filepath.IsAbs(tmpDir) {
			tmpDir = filepath.Join(rc.RunDir, tmpDir)
		}
		if err := os.MkdirAll(tmpDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating tmp dir: %w", err)
		}
	}

	e.waitMu.Lock()
	e.executing[rc.RunID] = true
	e.waitMu.Unlock()
	defer func() {
		e.waitMu.Lock()
		delete(e.executing, rc.RunID)
		e.waitMu.Unlock()
	}()

	if store != nil && !rc.Inline && !rc.Resume {
		if err := store.StartRun(&RunSnapshot{
			RunID:      rc.RunID,
			SessionID:  rc.SessionID,
			Status:     PersistStatusRunning,
			DAGSpec:    dagSpec,
			RunDir:     rc.RunDir,
			Input:      rc.Input,
			NodeStates: map[string]PersistedNodeState{},
		}); err != nil {
			log.Warn().Err(err).Str("run_id", rc.RunID).Msg("persisting workflow run start failed")
		}
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
		if ev.AgentName == "" {
			ev.AgentName = rc.AgentName
		}
		rc.EmitEvent(ev)
	}
	if !rc.Inline {
		emit(WorkflowEvent{Type: EventWorkflowStarted, Message: fmt.Sprintf("workflow %s started", defn.Name)})
	}

	g, gctx := errgroup.WithContext(ctx)

	var mu sync.Mutex
	hasSuspended := false
	results := make(map[string]*workflowspec.NodeResult, len(defn.Nodes))
	for id, res := range rc.SeedNodes {
		results[id] = res
	}
	values := &RunValues{}

	maxNodeExecutions := defn.MaxNodeExecutions
	if maxNodeExecutions <= 0 {
		maxNodeExecutions = 100
	}
	executionCount := make(map[string]int, len(defn.Nodes))
	for id, n := range rc.SeedExecutionCounts {
		executionCount[id] = n
	}

	// Loop bookkeeping: per-loop iteration counters, the child-loop index and
	// the set of on_exhausted orphan nodes (excluded from roots, swept as
	// SKIPPED (never_activated) when they never fire).
	loopsByID := make(map[string]*workflowspec.LoopSpec, len(defn.Loops))
	childLoops := make(map[string][]string, len(defn.Loops))
	onExhaustedTargets := make(map[string]bool, len(defn.Loops))
	for _, loop := range defn.Loops {
		loopsByID[loop.ID] = loop
		if loop.Parent != "" {
			childLoops[loop.Parent] = append(childLoops[loop.Parent], loop.ID)
		}
		if loop.OnExhausted != "" {
			onExhaustedTargets[loop.OnExhausted] = true
		}
	}
	loopIter := make(map[string]int, len(defn.Loops))
	for id, n := range rc.SeedLoopIterations {
		loopIter[id] = n
	}

	// snapshotStates deep-copies everything MarkWaitingHuman persists
	// (settled results + loop/execution counters) under the engine lock.
	snapshotStates := func() snapshotCapture {
		mu.Lock()
		defer mu.Unlock()
		return snapshotCapture{
			nodeStates:      toPersistedStates(results),
			loopIterations:  copyIntMap(loopIter),
			executionCounts: copyIntMap(executionCount),
		}
	}

	// nodeLoops indexes the loop IDs each node belongs to; settling a node
	// snapshots those loops' iteration counters into its NodeResult. Must be
	// called while holding mu.
	nodeLoops := make(map[string][]string, len(defn.Nodes))
	for _, loop := range defn.Loops {
		for _, nid := range loop.Nodes {
			nodeLoops[nid] = append(nodeLoops[nid], loop.ID)
		}
	}
	snapshotNodeLoops := func(nodeID string) map[string]int {
		ids := nodeLoops[nodeID]
		if len(ids) == 0 {
			return nil
		}
		m := make(map[string]int, len(ids))
		for _, id := range ids {
			m[id] = loopIter[id]
		}
		return m
	}

	nodeByID := make(map[string]*workflowspec.NodeSpec, len(defn.Nodes))
	for _, n := range defn.Nodes {
		nodeByID[n.ID] = n
	}

	// Classify dependencies
	unconditionalDeps := make(map[string][]workflowspec.NodeDependency, len(defn.Nodes))
	conditionalDeps := make(map[string][]workflowspec.NodeDependency, len(defn.Nodes))
	dependents := make(map[string][]*workflowspec.NodeSpec, len(defn.Nodes))

	for _, node := range defn.Nodes {
		for _, dep := range node.Depends {
			dependents[dep.NodeID] = append(dependents[dep.NodeID], node)
			if dep.When != "" {
				conditionalDeps[node.ID] = append(conditionalDeps[node.ID], dep)
			} else {
				unconditionalDeps[node.ID] = append(unconditionalDeps[node.ID], dep)
			}
		}
	}

	readyQueue := make([]string, 0, len(defn.Nodes))
	running := make(map[string]bool)
	eventCh := make(chan struct{}, 100)

	// enqueue admits a node for execution unless it is already queued or
	// running, or the global per-node execution cap is reached. It reports
	// whether the node was actually admitted.
	enqueue := func(nodeID string) bool {
		if running[nodeID] {
			return false
		}
		for _, id := range readyQueue {
			if id == nodeID {
				return false
			}
		}
		if executionCount[nodeID] >= maxNodeExecutions {
			log.Warn().Str("node", nodeID).Msg("max execution count reached, skipping loop re-entry")
			return false
		}
		readyQueue = append(readyQueue, nodeID)
		return true
	}

	// isPending reports whether the node is queued or currently running.
	isPending := func(nodeID string) bool {
		if running[nodeID] {
			return true
		}
		for _, id := range readyQueue {
			if id == nodeID {
				return true
			}
		}
		return false
	}

	// resetDescendantLoops zeroes the counters of every descendant loop.
	var resetDescendantLoops func(loopID string)
	resetDescendantLoops = func(loopID string) {
		for _, childID := range childLoops[loopID] {
			loopIter[childID] = 0
			resetDescendantLoops(childID)
		}
	}

	// replaying marks the seed-replay phase: nodes already settled from a
	// restored snapshot must not be re-enqueued, and their counting edges
	// must not consume loop iterations.
	replaying := len(rc.SeedNodes) > 0

	// exhaustedNoFallback records that a loop quota was breached with no
	// on_exhausted fallback: the run must settle FAILED no matter what
	// in-flight successors report (fail-closed, race-free).
	exhaustedNoFallback := false

	// evaluateDownstream checks dependents of settled nodes; declared early
	// because admitViaEdge may recursively settle exhausted-loop targets.
	var evaluateDownstream func(settledNodeID string)

	// admitViaEdge applies the edge-level loop metadata (counts_loop /
	// resets_loop) before enqueueing the edge target. Counting edges first
	// check the loop quota: an exhausted loop suppresses the re-entry and
	// routes to the loop's on_exhausted orphan (or fails the target when no
	// fallback is declared).
	admitViaEdge := func(dep workflowspec.NodeDependency, targetID string) {
		// Seed-replay settled guard: already-settled nodes are neither
		// re-enqueued nor counted.
		if replaying {
			if _, settled := results[targetID]; settled {
				return
			}
		}

		if dep.ResetsLoop != "" {
			loopIter[dep.ResetsLoop] = 0
			resetDescendantLoops(dep.ResetsLoop)
			enqueue(targetID)
			return
		}

		if dep.CountsLoop == "" {
			enqueue(targetID)
			return
		}
		loop := loopsByID[dep.CountsLoop]
		if loop == nil {
			enqueue(targetID)
			return
		}

		// A no-op re-entry (target already queued/running) must neither
		// consume an iteration nor trigger exhaustion routing.
		if isPending(targetID) {
			return
		}

		if loop.MaxIterations > 0 && loopIter[loop.ID] >= loop.MaxIterations {
			log.Warn().
				Str("workflow", defn.Name).
				Str("session_id", rc.SessionID).
				Str("loop", loop.ID).
				Str("node", targetID).
				Msgf("loop %s exhausted after %d iterations", loop.ID, loop.MaxIterations)
			if loop.OnExhausted != "" {
				enqueue(loop.OnExhausted)
				return
			}
			// No fallback declared: the quota breach fails the re-entry
			// target so the run settles FAILED (fail-closed), even though
			// the target's earlier iterations succeeded.
			exhaustedNoFallback = true
			_, wasSettled := results[targetID]
			res := &workflowspec.NodeResult{
				Status: workflowspec.StatusFailed,
				Error:  fmt.Errorf("loop %q exhausted after %d iterations; node %s not re-entered", loop.ID, loop.MaxIterations, targetID),
			}
			results[targetID] = res
			log.Error().
				Str("workflow", defn.Name).
				Str("session_id", rc.SessionID).
				Str("node_id", targetID).
				Str("status", string(res.Status)).
				Err(res.Error).
				Msgf("[Workflow %s] Node %q FINISHED: %s (loop %s exhausted)", defn.Name, targetID, res.Status, loop.ID)
			emit(WorkflowEvent{
				Type:     EventNodeFinished,
				NodeID:   targetID,
				NodeType: nodeByID[targetID].Type,
				AgentID:  nodeByID[targetID].AgentID,
				Status:   workflowspec.StatusFailed,
				Message:  fmt.Sprintf("node %s FAILED: %v", targetID, res.Error),
			})
			// Only cascade into downstream evaluation for a target that
			// never ran. A loop backedge target is already settled: its
			// downstream holds results from the prior iteration, and
			// re-driving them (notably join: always nodes) would relive
			// the exhausted loop forever. Instead, invalidate the stale
			// results of the target's conditional successors so global
			// settlement cannot mistake them for failure absorption
			// (fail-closed).
			if !wasSettled {
				evaluateDownstream(targetID)
			} else {
				for _, depNode := range dependents[targetID] {
					hasWhenEdge := false
					for _, dep := range depNode.Depends {
						if dep.NodeID == targetID && dep.When != "" {
							hasWhenEdge = true
							break
						}
					}
					if hasWhenEdge {
						delete(results, depNode.ID)
					}
				}
			}
			return
		}

		if enqueue(targetID) {
			loopIter[loop.ID]++
			resetDescendantLoops(loop.ID)
		}
	}

	// evaluateDownstream checks dependents of settled nodes
	evaluateDownstream = func(settledNodeID string) {
		// A condition-false skip is a dead branch end: the branch did no
		// work, so already-settled dependents (e.g. loop join nodes) must
		// not be re-triggered — otherwise conditional loops would re-execute
		// forever. Dependents that never ran are still evaluated normally.
		skipIsDeadBranch := false
		if res := results[settledNodeID]; res != nil && res.Status == workflowspec.StatusSkipped && res.SkipReason == workflowspec.SkipReasonConditionFalse {
			skipIsDeadBranch = true
		}
		// Fail-closed skip guard: a skipped node (any skip reason — cascaded
		// failure, condition-false, never-activated) produced no result of
		// its own; its zero-valued exit code must never satisfy a
		// downstream `when` expression.
		parentSkipped := false
		if res := results[settledNodeID]; res != nil && res.Status == workflowspec.StatusSkipped {
			parentSkipped = true
		}
		for _, depNode := range dependents[settledNodeID] {
			if skipIsDeadBranch {
				if _, settled := results[depNode.ID]; settled {
					continue
				}
			}
			// 1. Check if any conditional edge from settledNodeID triggered
			var matchingEdge *workflowspec.NodeDependency
			for i, cond := range conditionalDeps[depNode.ID] {
				if cond.NodeID != settledNodeID {
					continue
				}
				match := false
				_, field, ok := workflowspec.ParseExprTarget(cond.When)
				isStatusQuery := ok && (field == "status" || field == "skip_reason")
				if !parentSkipped || isStatusQuery {
					var err error
					match, err = workflowspec.EvaluateSimpleExpr(cond.When, results, nil)
					if err != nil {
						log.Warn().Err(err).Str("node", depNode.ID).Msgf("when expression %q evaluation failed", cond.When)
						match = false
					}
				}
				if match {
					matchingEdge = &conditionalDeps[depNode.ID][i]
					break
				}
			}

			if matchingEdge != nil {
				admitViaEdge(*matchingEdge, depNode.ID)
				continue
			}

			// 2. Check if settledNodeID is an unconditional parent of depNode
			var unconditionalEdge *workflowspec.NodeDependency
			for i, dep := range unconditionalDeps[depNode.ID] {
				if dep.NodeID == settledNodeID {
					unconditionalEdge = &unconditionalDeps[depNode.ID][i]
					break
				}
			}

			if unconditionalEdge != nil {
				allUnconditionalSettled := true
				for _, dep := range unconditionalDeps[depNode.ID] {
					if _, ok := results[dep.NodeID]; !ok {
						allUnconditionalSettled = false
						break
					}
				}

				settledRes := results[settledNodeID]
				settledRan := settledRes != nil && settledRes.Status == workflowspec.StatusSucceeded

				if allUnconditionalSettled || (depNode.Join == "always" && settledRan) {
					action, reason := EvaluateNodeReadiness(depNode, results)
					if action == ActionSkip {
						if _, already := results[depNode.ID]; !already || results[depNode.ID].Status != workflowspec.StatusSkipped {
							res := &workflowspec.NodeResult{Status: workflowspec.StatusSkipped, SkipReason: reason}
							results[depNode.ID] = res
							log.Info().
								Str("workflow", defn.Name).
								Str("session_id", rc.SessionID).
								Str("node_id", depNode.ID).
								Str("node_type", string(depNode.Type)).
								Str("agent_id", depNode.AgentID).
								Str("skip_reason", string(reason)).
								Msgf("[Workflow %s] Node %q (type=%s, agent=%s) SKIPPED: %s", defn.Name, depNode.ID, depNode.Type, depNode.AgentID, reason)
							emit(WorkflowEvent{
								Type:       EventNodeSkipped,
								NodeID:     depNode.ID,
								NodeType:   depNode.Type,
								AgentID:    depNode.AgentID,
								Status:     workflowspec.StatusSkipped,
								SkipReason: reason,
								Message:    fmt.Sprintf("node %s skipped (%s)", depNode.ID, reason),
							})
							evaluateDownstream(depNode.ID)
						}
					} else {
						admitViaEdge(*unconditionalEdge, depNode.ID)
					}
				}
			} else if len(unconditionalDeps[depNode.ID]) == 0 {
				// Node has ONLY conditional dependencies. If all conditional parents have settled and none matched:
				allConditionalSettled := true
				for _, cond := range conditionalDeps[depNode.ID] {
					if _, ok := results[cond.NodeID]; !ok {
						allConditionalSettled = false
						break
					}
				}
				if allConditionalSettled {
					if _, already := results[depNode.ID]; !already || results[depNode.ID].Status != workflowspec.StatusSkipped {
						res := &workflowspec.NodeResult{Status: workflowspec.StatusSkipped, SkipReason: workflowspec.SkipReasonConditionFalse}
						results[depNode.ID] = res
						log.Info().
							Str("workflow", defn.Name).
							Str("session_id", rc.SessionID).
							Str("node_id", depNode.ID).
							Str("node_type", string(depNode.Type)).
							Str("agent_id", depNode.AgentID).
							Str("skip_reason", string(workflowspec.SkipReasonConditionFalse)).
							Msgf("[Workflow %s] Node %q (type=%s, agent=%s) SKIPPED: %s", defn.Name, depNode.ID, depNode.Type, depNode.AgentID, workflowspec.SkipReasonConditionFalse)
						emit(WorkflowEvent{
							Type:       EventNodeSkipped,
							NodeID:     depNode.ID,
							NodeType:   depNode.Type,
							AgentID:    depNode.AgentID,
							Status:     workflowspec.StatusSkipped,
							SkipReason: workflowspec.SkipReasonConditionFalse,
							Message:    fmt.Sprintf("node %s skipped (%s)", depNode.ID, workflowspec.SkipReasonConditionFalse),
						})
						evaluateDownstream(depNode.ID)
					}
				}
			}
		}
	}

	// Initial seeds & roots
	mu.Lock()
	if len(rc.SeedNodes) > 0 {
		for id := range rc.SeedNodes {
			evaluateDownstream(id)
		}
		// Seed replay is over: downstream admissions behave normally again.
		replaying = false
		// Wake the suspended node recorded in the snapshot. on_exhausted
		// orphan human nodes have no static in-edges, so seed replay can
		// never reach them; only this explicit activation re-enters them.
		for _, nodeID := range rc.ActivateNodes {
			if _, exists := nodeByID[nodeID]; !exists {
				continue
			}
			if _, settled := results[nodeID]; settled {
				continue
			}
			enqueue(nodeID)
		}
	} else {
		for _, node := range defn.Nodes {
			// on_exhausted orphans have no static in-edges; they activate
			// exclusively via loop-exhaustion routing and must not run as
			// initial roots.
			if onExhaustedTargets[node.ID] {
				continue
			}
			if len(node.Depends) == 0 || (len(unconditionalDeps[node.ID]) == 0 && len(conditionalDeps[node.ID]) == 0) {
				enqueue(node.ID)
			}
		}
	}
	mu.Unlock()

	// Scheduling Loop
	for gctx.Err() == nil {
		mu.Lock()
		toLaunch := make([]string, len(readyQueue))
		copy(toLaunch, readyQueue)
		readyQueue = readyQueue[:0]

		for _, nodeID := range toLaunch {
			running[nodeID] = true
			iteration := executionCount[nodeID] + 1
			executionCount[nodeID] = iteration
			node := nodeByID[nodeID]

			g.Go(func() error {
				mu.Lock()
				upstreams := make(map[string]*workflowspec.NodeResult, len(results))
				for k, v := range results {
					upstreams[k] = v
				}
				loopSnapshot := copyIntMap(loopIter)
				mu.Unlock()

				action, reason := EvaluateNodeReadiness(node, upstreams)
				if action == ActionSkip {
					mu.Lock()
					res := &workflowspec.NodeResult{Status: workflowspec.StatusSkipped, SkipReason: reason}
					results[node.ID] = res
					delete(running, node.ID)
					log.Info().
						Str("workflow", defn.Name).
						Str("session_id", rc.SessionID).
						Str("node_id", node.ID).
						Str("node_type", string(node.Type)).
						Str("agent_id", node.AgentID).
						Str("skip_reason", string(reason)).
						Msgf("[Workflow %s] Node %q (type=%s, agent=%s) SKIPPED: %s", defn.Name, node.ID, node.Type, node.AgentID, reason)
					emit(WorkflowEvent{
						Type:       EventNodeSkipped,
						NodeID:     node.ID,
						NodeType:   node.Type,
						AgentID:    node.AgentID,
						Status:     workflowspec.StatusSkipped,
						SkipReason: reason,
						Message:    fmt.Sprintf("node %s skipped (%s)", node.ID, reason),
					})
					evaluateDownstream(node.ID)
					mu.Unlock()
					select {
					case eventCh <- struct{}{}:
					default:
					}
					return nil
				}

				log.Info().
					Str("workflow", defn.Name).
					Str("session_id", rc.SessionID).
					Str("node_id", node.ID).
					Str("node_type", string(node.Type)).
					Str("agent_id", node.AgentID).
					Int("iteration", iteration).
					Msgf("[Workflow %s] Node %q (type=%s, agent=%s) STARTED (iteration %d)", defn.Name, node.ID, node.Type, node.AgentID, iteration)

				emit(WorkflowEvent{
					Type:     EventNodeStarted,
					NodeID:   node.ID,
					NodeType: node.Type,
					AgentID:  node.AgentID,
					Status:   workflowspec.StatusRunning,
					Message:  fmt.Sprintf("node %s started", node.ID),
				})

				wfRunDirs, wfMountDirs := resolveWorkflowDirs(rc, defn)

				nctx := &NodeContext{
					SessionID:         rc.SessionID,
					RunID:             rc.RunID,
					RunDir:            rc.RunDir,
					TmpDir:            tmpDir,
					Input:             rc.Input,
					Defn:              defn,
					Node:              node,
					Upstreams:         upstreams,
					EventEmitter:      emit,
					Values:            values,
					Iteration:         iteration,
					LoopIterations:    loopSnapshot,
					WorkflowRunDirs:   wfRunDirs,
					WorkflowMountDirs: wfMountDirs,
					Headless:          rc.Headless || defn.NoHuman,
				}

				var result *workflowspec.NodeResult
				var err error
				if node.Type == workflowspec.NodeTypeHuman {
					if rc.HumanReplies[node.ID] == "" {
						mu.Lock()
						hasSuspended = true
						mu.Unlock()
					}
					result = e.runHumanNode(gctx, rc, nctx, store, dagSpec, snapshotStates)
				} else {
					runner, ok := e.registry.Get(node.Type)
					if !ok {
						err = fmt.Errorf("no runner registered for node type %q", node.Type)
					} else {
						result, err = runner.Run(gctx, nctx)
					}
				}
				result = normalizeResult(result, err)

				mu.Lock()
				// Snapshot the owning loops' iteration counters (loopIter was
				// incremented at admission, so this is the iteration the node
				// ran in) for nodes.<id>.loop_iteration.<loop_id> expressions.
				result.LoopIterations = snapshotNodeLoops(node.ID)
				results[node.ID] = result
				delete(running, node.ID)
				msg := fmt.Sprintf("node %s %s", node.ID, result.Status)
				if result.Error != nil {
					msg = fmt.Sprintf("node %s %s: %v", node.ID, result.Status, result.Error)
				}

				evLog := log.Info()
				if result.Status == workflowspec.StatusFailed {
					evLog = log.Error().Err(result.Error)
				}
				evLog.
					Str("workflow", defn.Name).
					Str("session_id", rc.SessionID).
					Str("node_id", node.ID).
					Str("node_type", string(node.Type)).
					Str("agent_id", node.AgentID).
					Str("status", string(result.Status)).
					Int("exit_code", result.ExitCode).
					Int("iteration", iteration).
					Msgf("[Workflow %s] Node %q (type=%s, agent=%s) FINISHED: %s (exit=%d, iteration %d)", defn.Name, node.ID, node.Type, node.AgentID, result.Status, result.ExitCode, iteration)

				// Agent / llm nodes carry their final response text so hosts can
				// display and persist it as a chat message. Command node output
				// (raw stdout) is intentionally excluded — it stays in artifacts
				// and the tool log.
				nodeOutput := result.Output
				if node.Type == workflowspec.NodeTypeCommand || node.Type == workflowspec.NodeTypeHuman {
					nodeOutput = ""
				}

				emit(WorkflowEvent{
					Type:      EventNodeFinished,
					NodeID:    node.ID,
					NodeType:  node.Type,
					AgentID:   node.AgentID,
					AgentName: result.AgentName,
					Status:    result.Status,
					Message:   msg,
					Output:    nodeOutput,
					Artifacts: ArtifactViewerPaths(result.Artifacts, tmpDir),
				})
				evaluateDownstream(node.ID)
				mu.Unlock()

				// Post-Settle RefreshSuspension hook (B2, B5, B7, N2)
				if store != nil {
					e.waitMu.Lock()
					if activeWaiters := e.waitingByRun[rc.RunID]; len(activeWaiters) > 0 {
						suspendedNodesMap := make(map[string]SuspendedNodeInfo, len(activeWaiters))
						for nid, w := range activeWaiters {
							suspendedNodesMap[nid] = SuspendedNodeInfo{
								MessageID: w.messageID,
								Iteration: w.iteration,
							}
						}
						captured := snapshotStates()
						if err := store.RefreshSuspension(rc.RunID, captured.nodeStates, captured.loopIterations, captured.executionCounts, suspendedNodesMap); err != nil {
							log.Warn().Err(err).Str("run_id", rc.RunID).Msg("refreshing suspension state failed")
						}
					}
					e.waitMu.Unlock()
				}

				select {
				case eventCh <- struct{}{}:
				default:
				}
				return nil
			})
		}

		idle := len(readyQueue) == 0 && len(running) == 0
		mu.Unlock()

		if idle {
			break
		}

		select {
		case <-eventCh:
		case <-gctx.Done():
		}
	}

	_ = g.Wait()

	// Dormant Orphan Sweep: on_exhausted orphans that never fired (and their
	// downstream subtrees) are marked SKIPPED (never_activated) so a
	// happy path settles COMPLETED instead of a phantom FAILED. Requires mu:
	// reads results and dependents metadata.
	mu.Lock()
	sweepDormantOrphans := func() {
		downstreamDependents := make(map[string][]string, len(defn.Nodes))
		for _, node := range defn.Nodes {
			for _, dep := range node.Depends {
				downstreamDependents[dep.NodeID] = append(downstreamDependents[dep.NodeID], node.ID)
			}
		}
		var mark func(nodeID string)
		mark = func(nodeID string) {
			if _, activated := results[nodeID]; activated {
				return
			}
			results[nodeID] = &workflowspec.NodeResult{Status: workflowspec.StatusSkipped, SkipReason: workflowspec.SkipReasonNeverActivated}
			log.Info().
				Str("workflow", defn.Name).
				Str("session_id", rc.SessionID).
				Str("node_id", nodeID).
				Str("skip_reason", string(workflowspec.SkipReasonNeverActivated)).
				Msgf("[Workflow %s] Node %q SKIPPED (never activated on_exhausted branch)", defn.Name, nodeID)
			emit(WorkflowEvent{
				Type:       EventNodeSkipped,
				NodeID:     nodeID,
				NodeType:   nodeByID[nodeID].Type,
				AgentID:    nodeByID[nodeID].AgentID,
				Status:     workflowspec.StatusSkipped,
				SkipReason: workflowspec.SkipReasonNeverActivated,
				Message:    fmt.Sprintf("node %s skipped (%s)", nodeID, workflowspec.SkipReasonNeverActivated),
			})
			for _, next := range downstreamDependents[nodeID] {
				mark(next)
			}
		}
		for _, loop := range defn.Loops {
			if loop.OnExhausted != "" {
				mark(loop.OnExhausted)
			}
		}
	}
	sweepDormantOrphans()

	run := &WorkflowRunResult{Nodes: results}
	if ctx.Err() != nil {
		run.Status = RunStatusCanceled
		run.Error = ctx.Err()
	} else {
		run.Status = settleGlobalStatus(defn, results)
		// A loop quota breached without an on_exhausted fallback fails the
		// run deterministically: successors still in flight when the breach
		// was recorded may re-settle stale successes that would otherwise
		// absorb the failure into a green COMPLETED.
		if exhaustedNoFallback {
			if run.Status == RunStatusCompleted {
				run.Status = RunStatusFailed
			}
			if run.Error == nil {
				run.Error = fmt.Errorf("workflow failed: loop quota exhausted without an on_exhausted fallback")
			}
		}
		if run.Status == RunStatusFailed && run.Error == nil {
			for _, node := range defn.Nodes {
				if res := results[node.ID]; res != nil && res.Status == workflowspec.StatusFailed && res.Error != nil {
					run.Error = fmt.Errorf("workflow failed: node %s failed: %w", node.ID, res.Error)
					break
				}
			}
			if run.Error == nil {
				run.Error = fmt.Errorf("workflow failed: unhandled failure or unexecuted node")
			}
		}
	}

	// Explicit abort semantics: an activated on_exhausted orphan whose reply
	// signals abort (e.g. selecting "Abort Workflow" or replying "abort") terminates the
	// run as CANCELLED — never a green COMPLETED. Restrict to human nodes and check
	// for explicit option selection or exact "abort" reply.
	if ctx.Err() == nil {
		for _, loop := range defn.Loops {
			if loop.OnExhausted == "" {
				continue
			}
			node := nodeByID[loop.OnExhausted]
			if node == nil || node.Type != workflowspec.NodeTypeHuman {
				continue
			}
			if res := results[loop.OnExhausted]; res != nil && isHumanAbortReply(res.Output, node.Options) {
				run.Status = RunStatusCanceled
				run.Error = fmt.Errorf("workflow aborted by user at node %s", loop.OnExhausted)
				break
			}
		}
	}
	mu.Unlock()

	if store != nil && (!rc.Inline || hasSuspended) {
		if err := store.SettleRun(rc.RunID, persistStatus(run.Status), toPersistedStates(results)); err != nil {
			log.Warn().Err(err).Str("run_id", rc.RunID).Msg("persisting workflow run settlement failed")
		}
	}

	// The final event carries the workflow summary so hosts persisting events
	// render an identical transcript on reload.
	if !rc.Inline {
		emit(WorkflowEvent{
			Type:    EventWorkflowFinished,
			Status:  workflowspec.NodeStatus(run.Status),
			Message: SummarizeRun(run),
		})
	}
	return run, nil
}

// isHumanAbortReply checks whether a human node's reply matches an abort option
// or an exact abort keyword, avoiding false positives on substring matches like "don't abort".
func isHumanAbortReply(output string, options []string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(output))
	if trimmed == "" {
		return false
	}
	if trimmed == "abort" || trimmed == "abort workflow" || trimmed == "cancel" || trimmed == "cancelled" || trimmed == "canceled" {
		return true
	}
	for _, opt := range options {
		optLower := strings.TrimSpace(strings.ToLower(opt))
		if strings.Contains(optLower, "abort") || strings.Contains(optLower, "cancel") {
			if trimmed == optLower {
				return true
			}
		}
	}
	return false
}

// normalizeResult coerces runner output into a well-formed NodeResult.
func normalizeResult(result *workflowspec.NodeResult, err error) *workflowspec.NodeResult {
	if result == nil {
		result = &workflowspec.NodeResult{}
	}
	if err != nil {
		result.Status = workflowspec.StatusFailed
		result.Error = err
		return result
	}
	if result.Status == "" {
		result.Status = workflowspec.StatusSucceeded
	}
	if result.Status == workflowspec.StatusFailed && result.Error == nil {
		result.Error = fmt.Errorf("node failed with exit code %d", result.ExitCode)
	}
	return result
}

// settleGlobalStatus computes the final workflow status:
//
//   - FAILED if any node was skipped by cascaded failure, or if any FAILED
//     node was not absorbed by a downstream when-branch that succeeded.
//   - COMPLETED otherwise (condition-false skips are considered success).
func settleGlobalStatus(defn *workflowspec.WorkflowDefinition, results map[string]*workflowspec.NodeResult) WorkflowRunStatus {
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
			if res := results[dep]; res != nil && res.Status == workflowspec.StatusSucceeded {
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
		if res.Status == workflowspec.StatusSkipped && res.SkipReason == workflowspec.SkipReasonCascadedFailure {
			return RunStatusFailed
		}
		if res.Status == workflowspec.StatusFailed && !failureHandled(node.ID) {
			return RunStatusFailed
		}
	}
	return RunStatusCompleted
}

// resolveWorkflowDirs computes the effective RunDirs and MountDirs for a workflow run
// by combining runtime context overrides (rc) with definition-level defaults (defn).
// ReadOnly and ReadWrite mounts are resolved independently to preserve granular configuration.
func resolveWorkflowDirs(rc RunContext, defn *workflowspec.WorkflowDefinition) ([]string, workflowspec.MountDirsConfig) {
	var runDirs []string
	if len(rc.WorkflowRunDirs) > 0 {
		runDirs = append([]string(nil), rc.WorkflowRunDirs...)
	} else if defn != nil && len(defn.RunDirs) > 0 {
		runDirs = append([]string(nil), defn.RunDirs...)
	} else if rc.RunDir != "" {
		runDirs = []string{rc.RunDir}
	}

	var mountDirs workflowspec.MountDirsConfig
	// ReadOnly mount fallback: runtime rc overrides defn if provided; otherwise fallback to defn
	if len(rc.WorkflowMountDirs.ReadOnly) > 0 {
		mountDirs.ReadOnly = append([]string(nil), rc.WorkflowMountDirs.ReadOnly...)
	} else if defn != nil && len(defn.MountDirs.ReadOnly) > 0 {
		mountDirs.ReadOnly = append([]string(nil), defn.MountDirs.ReadOnly...)
	}
	// ReadWrite mount fallback: runtime rc overrides defn if provided; otherwise fallback to defn
	if len(rc.WorkflowMountDirs.ReadWrite) > 0 {
		mountDirs.ReadWrite = append([]string(nil), rc.WorkflowMountDirs.ReadWrite...)
	} else if defn != nil && len(defn.MountDirs.ReadWrite) > 0 {
		mountDirs.ReadWrite = append([]string(nil), defn.MountDirs.ReadWrite...)
	}

	return runDirs, mountDirs
}
