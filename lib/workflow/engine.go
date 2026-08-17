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

	"github.com/AgentDrasil/asgard/lib/agents"
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
	SeedNodes map[string]*NodeResult
	// HumanReplies maps human node IDs to pre-supplied user replies (resume).
	HumanReplies map[string]string
	// WorkflowRunDirs carries workflow/parent configured run directories.
	WorkflowRunDirs []string
	// WorkflowMountDirs carries workflow/parent configured mount directories.
	WorkflowMountDirs MountDirsConfig
	// EmitEvent receives engine lifecycle events; may be nil.
	EmitEvent func(event WorkflowEvent)
}

// Engine schedules workflow DAGs with fork-join parallelism.
type Engine struct {
	registry *NodeRunnerRegistry

	// store / suspendHuman are the engine-level persistence defaults, wired
	// once by the host application (lib/api).
	store        RunStore
	suspendHuman SuspendHumanFunc

	// waiting tracks live suspended runs for in-process resume delivery.
	waitMu  sync.Mutex
	waiting map[string]chan string
}

// NewEngine creates an engine backed by the given runner registry.
func NewEngine(registry *NodeRunnerRegistry) *Engine {
	return &Engine{
		registry: registry,
		waiting:  make(map[string]chan string),
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
func (e *Engine) SetAgents(agentList []*agents.Agent) {
	if e == nil || e.registry == nil {
		return
	}
	if runner, ok := e.registry.Get(NodeTypeAgent); ok {
		if preloader, ok := runner.(interface{ SetAgents([]*agents.Agent) }); ok {
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

	tmpDir := Interpolate(defn.TmpDir, func(key string) (string, bool) {
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

	if store != nil {
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
	emit(WorkflowEvent{Type: EventWorkflowStarted, Message: fmt.Sprintf("workflow %s started", defn.Name)})

	g, gctx := errgroup.WithContext(ctx)

	var mu sync.Mutex
	results := make(map[string]*NodeResult, len(defn.Nodes))
	for id, res := range rc.SeedNodes {
		results[id] = res
	}
	values := &RunValues{}

	snapshotStates := func() map[string]PersistedNodeState {
		mu.Lock()
		defer mu.Unlock()
		return toPersistedStates(results)
	}

	const maxNodeExecutions = 100
	executionCount := make(map[string]int, len(defn.Nodes))

	nodeByID := make(map[string]*NodeSpec, len(defn.Nodes))
	for _, n := range defn.Nodes {
		nodeByID[n.ID] = n
	}

	// Classify dependencies
	unconditionalDeps := make(map[string][]string, len(defn.Nodes))
	conditionalDeps := make(map[string][]NodeDependency, len(defn.Nodes))
	dependents := make(map[string][]*NodeSpec, len(defn.Nodes))

	for _, node := range defn.Nodes {
		for _, dep := range node.Depends {
			dependents[dep.NodeID] = append(dependents[dep.NodeID], node)
			if dep.When != "" {
				conditionalDeps[node.ID] = append(conditionalDeps[node.ID], dep)
			} else {
				unconditionalDeps[node.ID] = append(unconditionalDeps[node.ID], dep.NodeID)
			}
		}
	}

	readyQueue := make([]string, 0, len(defn.Nodes))
	running := make(map[string]bool)
	eventCh := make(chan struct{}, 100)

	enqueue := func(nodeID string) {
		if running[nodeID] {
			return
		}
		for _, id := range readyQueue {
			if id == nodeID {
				return
			}
		}
		if executionCount[nodeID] >= maxNodeExecutions {
			log.Warn().Str("node", nodeID).Msg("max execution count reached, skipping loop re-entry")
			return
		}
		readyQueue = append(readyQueue, nodeID)
	}

	// evaluateDownstream checks dependents of settled nodes
	var evaluateDownstream func(settledNodeID string)
	evaluateDownstream = func(settledNodeID string) {
		// A condition-false skip is a dead branch end: the branch did no
		// work, so already-settled dependents (e.g. loop join nodes) must
		// not be re-triggered — otherwise conditional loops would re-execute
		// forever. Dependents that never ran are still evaluated normally.
		skipIsDeadBranch := false
		if res := results[settledNodeID]; res != nil && res.Status == StatusSkipped && res.SkipReason == SkipReasonConditionFalse {
			skipIsDeadBranch = true
		}
		for _, depNode := range dependents[settledNodeID] {
			if skipIsDeadBranch {
				if _, settled := results[depNode.ID]; settled {
					continue
				}
			}
			// 1. Check if any conditional edge from settledNodeID triggered
			hasMatchingConditional := false
			for _, cond := range conditionalDeps[depNode.ID] {
				if cond.NodeID == settledNodeID {
					match, err := EvaluateSimpleExpr(cond.When, results, nil)
					if err != nil {
						log.Warn().Err(err).Str("node", depNode.ID).Msgf("when expression %q evaluation failed", cond.When)
						match = false
					}
					if match {
						hasMatchingConditional = true
						break
					}
				}
			}

			if hasMatchingConditional {
				enqueue(depNode.ID)
				continue
			}

			// 2. Check if settledNodeID is an unconditional parent of depNode
			isUnconditionalParent := false
			for _, parentID := range unconditionalDeps[depNode.ID] {
				if parentID == settledNodeID {
					isUnconditionalParent = true
					break
				}
			}

			if isUnconditionalParent {
				allUnconditionalSettled := true
				for _, parentID := range unconditionalDeps[depNode.ID] {
					if _, ok := results[parentID]; !ok {
						allUnconditionalSettled = false
						break
					}
				}

				settledRes := results[settledNodeID]
				settledRan := settledRes != nil && settledRes.Status == StatusSucceeded

				if allUnconditionalSettled || (depNode.Join == "always" && settledRan) {
					action, reason := EvaluateNodeReadiness(depNode, results)
					if action == ActionSkip {
						if _, already := results[depNode.ID]; !already || results[depNode.ID].Status != StatusSkipped {
							res := &NodeResult{Status: StatusSkipped, SkipReason: reason}
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
								Status:     StatusSkipped,
								SkipReason: reason,
								Message:    fmt.Sprintf("node %s skipped (%s)", depNode.ID, reason),
							})
							evaluateDownstream(depNode.ID)
						}
					} else {
						enqueue(depNode.ID)
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
					if _, already := results[depNode.ID]; !already || results[depNode.ID].Status != StatusSkipped {
						res := &NodeResult{Status: StatusSkipped, SkipReason: SkipReasonConditionFalse}
						results[depNode.ID] = res
						log.Info().
							Str("workflow", defn.Name).
							Str("session_id", rc.SessionID).
							Str("node_id", depNode.ID).
							Str("node_type", string(depNode.Type)).
							Str("agent_id", depNode.AgentID).
							Str("skip_reason", string(SkipReasonConditionFalse)).
							Msgf("[Workflow %s] Node %q (type=%s, agent=%s) SKIPPED: %s", defn.Name, depNode.ID, depNode.Type, depNode.AgentID, SkipReasonConditionFalse)
						emit(WorkflowEvent{
							Type:       EventNodeSkipped,
							NodeID:     depNode.ID,
							NodeType:   depNode.Type,
							AgentID:    depNode.AgentID,
							Status:     StatusSkipped,
							SkipReason: SkipReasonConditionFalse,
							Message:    fmt.Sprintf("node %s skipped (%s)", depNode.ID, SkipReasonConditionFalse),
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
	} else {
		for _, node := range defn.Nodes {
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
			executionCount[nodeID]++
			node := nodeByID[nodeID]

			g.Go(func() error {
				mu.Lock()
				upstreams := make(map[string]*NodeResult, len(results))
				for k, v := range results {
					upstreams[k] = v
				}
				mu.Unlock()

				action, reason := EvaluateNodeReadiness(node, upstreams)
				if action == ActionSkip {
					mu.Lock()
					res := &NodeResult{Status: StatusSkipped, SkipReason: reason}
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
						Status:     StatusSkipped,
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
					Int("iteration", executionCount[node.ID]).
					Msgf("[Workflow %s] Node %q (type=%s, agent=%s) STARTED (iteration %d)", defn.Name, node.ID, node.Type, node.AgentID, executionCount[node.ID])

				emit(WorkflowEvent{
					Type:     EventNodeStarted,
					NodeID:   node.ID,
					NodeType: node.Type,
					AgentID:  node.AgentID,
					Status:   StatusRunning,
					Message:  fmt.Sprintf("node %s started", node.ID),
				})

				wfRunDirs, wfMountDirs := resolveWorkflowDirs(rc, defn)

				nctx := &NodeContext{
					SessionID:         rc.SessionID,
					RunDir:            rc.RunDir,
					TmpDir:            tmpDir,
					Input:             rc.Input,
					Defn:              defn,
					Node:              node,
					Upstreams:         upstreams,
					EventEmitter:      emit,
					Values:            values,
					Iteration:         executionCount[node.ID],
					WorkflowRunDirs:   wfRunDirs,
					WorkflowMountDirs: wfMountDirs,
				}

				var result *NodeResult
				var err error
				if node.Type == NodeTypeHuman {
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
				results[node.ID] = result
				delete(running, node.ID)
				msg := fmt.Sprintf("node %s %s", node.ID, result.Status)
				if result.Error != nil {
					msg = fmt.Sprintf("node %s %s: %v", node.ID, result.Status, result.Error)
				}

				evLog := log.Info()
				if result.Status == StatusFailed {
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
					Int("iteration", executionCount[node.ID]).
					Msgf("[Workflow %s] Node %q (type=%s, agent=%s) FINISHED: %s (exit=%d, iteration %d)", defn.Name, node.ID, node.Type, node.AgentID, result.Status, result.ExitCode, executionCount[node.ID])

				// Agent / llm nodes carry their final response text so hosts can
				// display and persist it as a chat message. Command node output
				// (raw stdout) is intentionally excluded — it stays in artifacts
				// and the tool log.
				nodeOutput := result.Output
				if node.Type == NodeTypeCommand || node.Type == NodeTypeHuman {
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

	run := &WorkflowRunResult{Nodes: results}
	if ctx.Err() != nil {
		run.Status = RunStatusCanceled
		run.Error = ctx.Err()
	} else {
		run.Status = settleGlobalStatus(defn, results)
	}

	if store != nil {
		if err := store.SettleRun(rc.RunID, persistStatus(run.Status), toPersistedStates(results)); err != nil {
			log.Warn().Err(err).Str("run_id", rc.RunID).Msg("persisting workflow run settlement failed")
		}
	}

	// The final event carries the same summary the A2A executor streams to
	// clients, so hosts persisting events render an identical transcript on
	// reload.
	emit(WorkflowEvent{
		Type:    EventWorkflowFinished,
		Status:  NodeStatus(run.Status),
		Message: summarizeRun(run),
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

// resolveWorkflowDirs computes the effective RunDirs and MountDirs for a workflow run
// by combining runtime context overrides (rc) with definition-level defaults (defn).
// ReadOnly and ReadWrite mounts are resolved independently to preserve granular configuration.
func resolveWorkflowDirs(rc RunContext, defn *WorkflowDefinition) ([]string, MountDirsConfig) {
	var runDirs []string
	if len(rc.WorkflowRunDirs) > 0 {
		runDirs = append([]string(nil), rc.WorkflowRunDirs...)
	} else if defn != nil && len(defn.RunDirs) > 0 {
		runDirs = append([]string(nil), defn.RunDirs...)
	} else if rc.RunDir != "" {
		runDirs = []string{rc.RunDir}
	}

	var mountDirs MountDirsConfig
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
