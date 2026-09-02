package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// runHumanNode executes a `type: human` node. When a reply was pre-supplied
// (resume path) the node settles immediately; otherwise the run suspends: the
// WAITING_HUMAN snapshot is persisted, the suspension is delivered to the host
// application and the worker blocks until Resume delivers the user reply or
// the run is cancelled.
func (e *Engine) runHumanNode(ctx context.Context, rc RunContext, nctx *NodeContext, store RunStore, dagSpec string, snapshotStates func() snapshotCapture) *workflowspec.NodeResult {
	node := nctx.Node
	emit := nctx.EventEmitter

	if rc.Headless || nctx.Headless || (nctx.Defn != nil && nctx.Defn.NoHuman) {
		return &workflowspec.NodeResult{Status: workflowspec.StatusFailed, Error: fmt.Errorf("node %s: headless execution: human nodes not supported", node.ID)}
	}

	if reply := rc.HumanReplies[node.ID]; reply != "" {
		return humanReplyResult(nctx, reply)
	}

	suspend := rc.SuspendHuman
	if suspend == nil {
		suspend = e.suspendHuman
	}
	if suspend == nil {
		return &workflowspec.NodeResult{Status: workflowspec.StatusFailed, Error: fmt.Errorf("node %s: no human suspension gateway configured", node.ID)}
	}

	prompt := nctx.Interpolate(node.Prompt)
	if len(node.Options) > 0 {
		prompt = prompt + "\n\nOptions: " + strings.Join(node.Options, " / ")
	}

	// Collect artifact files referenced by the prompt so the host app can
	// register them for the session and surface them to the frontend.
	artifactPaths := ExtractArtifactPathsInSession(node.Prompt, prompt, nctx.TmpDir, nctx.RunDir, nctx.SessionDir)
	artifactViewerPaths := make([]string, 0, len(artifactPaths))
	for _, p := range artifactPaths {
		artifactViewerPaths = append(artifactViewerPaths, ViewerArtifactPathInSession(p, nctx.TmpDir, DefaultSessionDir(nctx.SessionID)))
	}

	replyCh := make(chan string, 1)

	// Atomic registration, non-blocking peek, and snapshot persistence under waitMu (B7, N4)
	e.waitMu.Lock()
	// First re-suspension reuse original MessageID if available (N1)
	var messageID string
	if originalMsgID, ok := rc.ReuseMessageIDs[node.ID]; ok && originalMsgID != "" {
		messageID = originalMsgID
		delete(rc.ReuseMessageIDs, node.ID)
	} else {
		messageID = HumanMessageID(rc.RunID, node.ID, nctx.Iteration)
	}

	waiter := &humanWaiter{
		replyCh:   replyCh,
		nodeID:    node.ID,
		messageID: messageID,
		iteration: nctx.Iteration,
		runID:     rc.RunID,
	}

	if e.waitingByRun[rc.RunID] == nil {
		e.waitingByRun[rc.RunID] = make(map[string]*humanWaiter)
	}
	e.waitingByRun[rc.RunID][node.ID] = waiter
	e.waitingByMsg[messageID] = waiter

	hasImmediateReply := false
	select {
	case reply := <-replyCh:
		replyCh <- reply // put back to buffer
		hasImmediateReply = true
	default:
	}

	if !hasImmediateReply && store != nil {
		suspendedNodesMap := make(map[string]SuspendedNodeInfo)
		for nid, w := range e.waitingByRun[rc.RunID] {
			suspendedNodesMap[nid] = SuspendedNodeInfo{
				MessageID: w.messageID,
				Iteration: w.iteration,
			}
		}
		captured := snapshotStates() // waitMu -> mu lock ordering compliant
		if err := store.MarkWaitingHuman(&RunSnapshot{
			RunID:              rc.RunID,
			SessionID:          rc.SessionID,
			ParentRunID:        rc.ParentRunID,
			Status:             PersistStatusWaitingHuman,
			DAGSpec:            dagSpec,
			RunDir:             rc.RunDir,
			Input:              rc.Input,
			NodeStates:         captured.nodeStates,
			LoopIterations:     captured.loopIterations,
			ExecutionCounts:    captured.executionCounts,
			SuspendedNodeID:    node.ID,
			SuspendedMessageID: messageID,
			SuspendedNodes:     suspendedNodesMap,
		}); err != nil {
			log.Warn().Err(err).Str("run_id", rc.RunID).Str("node_id", node.ID).Msg("marking workflow waiting for human failed")
		}
	}
	if e.replayPending[rc.RunID] {
		delete(e.replayPending, rc.RunID)
	}
	e.waitMu.Unlock()

	defer func() {
		e.waitMu.Lock()
		delete(e.waitingByMsg, messageID)
		if nodeMap := e.waitingByRun[rc.RunID]; nodeMap != nil {
			delete(nodeMap, node.ID)
			if len(nodeMap) == 0 {
				delete(e.waitingByRun, rc.RunID)
			}
		}
		e.waitMu.Unlock()
	}()

	if err := suspend(SuspendRequest{
		RunID:     rc.RunID,
		SessionID: rc.SessionID,
		NodeID:    node.ID,
		MessageID: messageID,
		Prompt:    prompt,
		Options:   node.Options,
		AgentName: rc.AgentName,
		Artifacts: artifactViewerPaths,
	}); err != nil {
		return &workflowspec.NodeResult{Status: workflowspec.StatusFailed, Error: fmt.Errorf("node %s: delivering human suspension: %w", node.ID, err)}
	}

	emit(WorkflowEvent{
		Type:      EventWorkflowSuspended,
		NodeID:    node.ID,
		NodeType:  workflowspec.NodeTypeHuman,
		Status:    workflowspec.NodeStatus(RunStatusWaitingHuman),
		Message:   prompt,
		MessageID: messageID,
		Artifacts: artifactViewerPaths,
		AgentName: rc.AgentName,
	})

	select {
	case reply := <-replyCh:
		if store != nil {
			e.waitMu.Lock()
			shouldMarkRunning := len(e.waitingByRun[rc.RunID]) <= 1
			e.waitMu.Unlock()
			if shouldMarkRunning {
				if err := store.MarkRunning(rc.RunID); err != nil {
					log.Warn().Err(err).Str("run_id", rc.RunID).Msg("marking workflow run running failed")
				}
			}
		}
		emit(WorkflowEvent{
			Type:     EventWorkflowResumed,
			NodeID:   node.ID,
			NodeType: workflowspec.NodeTypeHuman,
			Status:   workflowspec.StatusRunning,
			Message:  fmt.Sprintf("node %s resumed with user reply", node.ID),
		})
		return humanReplyResult(nctx, reply)
	case <-ctx.Done():
		return &workflowspec.NodeResult{
			Status: workflowspec.StatusFailed,
			Error:  fmt.Errorf("node %s: run cancelled while waiting for human input", node.ID),
		}
	}
}

// humanReplyResult settles a human node from a user reply, persisting the
// reply into the node's declared output_file artifact when configured.
func humanReplyResult(nctx *NodeContext, reply string) *workflowspec.NodeResult {
	result := &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded, Output: reply}
	if nctx.Node.OutputFile == "" {
		return result
	}
	path := nctx.Node.OutputFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(nctx.TmpDir, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("creating human output artifact dir failed")
		return result
	}
	if err := os.WriteFile(path, []byte(reply), 0o644); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("writing human output artifact failed")
		return result
	}
	result.Artifacts = map[string]string{nctx.Node.OutputFile: path}
	return result
}

// Resume delivers a user reply to a suspended run. When the run has a live
// in-process waiter the reply unblocks it and (ResumeDeliveredLive, nil, nil) is returned.
// Otherwise (e.g. after a server restart) the run is re-driven from its
// persisted snapshot with the suspended node settled from the reply. Re-driven
// events are only logged.
func (e *Engine) Resume(ctx context.Context, runID string, replyText string) (ResumeOutcome, *WorkflowRunResult, error) {
	return e.ResumeWithEmitter(ctx, runID, replyText, nil)
}

// buildResumeContext assembles the RunContext that re-drives a suspended run
// from its persisted snapshot: seeded node results, restored loop/execution
// counters and direct activation of the suspended node. An empty replyText
// leaves the suspended node without a pre-supplied answer so it suspends
// again instead of settling immediately.
func (e *Engine) buildResumeContext(snap *RunSnapshot, replyText string, emit func(WorkflowEvent)) RunContext {
	rc := RunContext{
		RunID:               snap.RunID,
		SessionID:           snap.SessionID,
		ParentRunID:         snap.ParentRunID,
		RunDir:              snap.RunDir,
		Input:               snap.Input,
		DAGSpec:             snap.DAGSpec,
		Store:               e.store,
		SuspendHuman:        e.suspendHuman,
		SeedNodes:           fromPersistedStates(snap.NodeStates),
		SeedLoopIterations:  copyIntMap(snap.LoopIterations),
		SeedExecutionCounts: copyIntMap(snap.ExecutionCounts),
		Resume:              true,
		ReuseMessageIDs:     make(map[string]string),
		HumanReplies:        make(map[string]string),
		EmitEvent: func(ev WorkflowEvent) {
			if emit != nil {
				emit(ev)
				return
			}
			log.Info().Str("run_id", snap.RunID).Str("node", ev.NodeID).Str("type", string(ev.Type)).Msg("resumed workflow event")
		},
	}

	for nid, sinfo := range snap.SuspendedNodes {
		if sinfo.MessageID != "" {
			rc.ReuseMessageIDs[nid] = sinfo.MessageID
		}
	}

	if len(snap.SuspendedNodes) > 0 {
		rc.ActivateNodes = make([]string, 0, len(snap.SuspendedNodes))
		for nid := range snap.SuspendedNodes {
			rc.ActivateNodes = append(rc.ActivateNodes, nid)
		}
		if snap.SuspendedNodeID != "" && replyText != "" {
			rc.HumanReplies[snap.SuspendedNodeID] = replyText
		}
	} else if snap.SuspendedNodeID != "" {
		rc.ActivateNodes = []string{snap.SuspendedNodeID}
		if snap.SuspendedMessageID != "" {
			rc.ReuseMessageIDs[snap.SuspendedNodeID] = snap.SuspendedMessageID
		}
		if replyText != "" {
			rc.HumanReplies[snap.SuspendedNodeID] = replyText
		}
	}
	return rc
}

// ResumeByMessageID delivers a reply to the human node matching messageID.
// If the node has an active in-memory waiter, it delivers the reply directly.
// Otherwise, it restores the run from snapshot with guards against concurrent replay and active execution.
func (e *Engine) ResumeByMessageID(ctx context.Context, messageID string, replyText string, emit func(WorkflowEvent)) (ResumeOutcome, *WorkflowRunResult, error) {
	if messageID == "" || replyText == "" {
		return ResumeIgnored, nil, fmt.Errorf("message_id and reply_text are required")
	}

	if e.DeliverResumeByMessageID(messageID, replyText) {
		return ResumeDeliveredLive, nil, nil
	}

	store := e.store
	if store == nil {
		return ResumeIgnored, nil, fmt.Errorf("workflow run store is not configured")
	}

	snap, err := store.FindWaitingHumanByMessageID(messageID)
	if err != nil {
		return ResumeIgnored, nil, fmt.Errorf("loading waiting workflow run for message %s: %w", messageID, err)
	}
	if snap == nil {
		return ResumeIgnored, nil, fmt.Errorf("no waiting workflow run found for message %s", messageID)
	}
	if snap.Status != PersistStatusWaitingHuman {
		return ResumeIgnored, nil, fmt.Errorf("workflow run %s is not waiting for human input (status %s)", snap.RunID, snap.Status)
	}

	// Locate target node for this messageID
	targetNodeID := snap.SuspendedNodeID
	if len(snap.SuspendedNodes) > 0 {
		found := false
		for nid, sinfo := range snap.SuspendedNodes {
			if sinfo.MessageID == messageID {
				targetNodeID = nid
				found = true
				break
			}
		}
		if !found && snap.SuspendedMessageID != messageID {
			// In case SQLite LIKE matched case-insensitively, perform exact match check
			return ResumeIgnored, nil, fmt.Errorf("message %s does not match any suspended node in run %s", messageID, snap.RunID)
		}
	}

	e.waitMu.Lock()
	// Guard 1: replayPending wait loop (B8)
	if e.replayPending[snap.RunID] {
		e.waitMu.Unlock()
		deadline := time.Now().Add(100 * time.Millisecond)
		for time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
			if e.DeliverResumeByMessageID(messageID, replyText) {
				return ResumeDeliveredLive, nil, nil
			}
			e.waitMu.Lock()
			stillPending := e.replayPending[snap.RunID]
			e.waitMu.Unlock()
			if !stillPending {
				break
			}
		}
		if e.DeliverResumeByMessageID(messageID, replyText) {
			return ResumeDeliveredLive, nil, nil
		}
		log.Warn().Str("run_id", snap.RunID).Str("message_id", messageID).Msg("replay pending timeout; resume discarded safely")
		return ResumeIgnored, nil, nil
	}

	// Guard 2: executing or registered waiters guard (B6, N3)
	if e.executing[snap.RunID] || len(e.waitingByRun[snap.RunID]) > 0 {
		e.waitMu.Unlock()
		log.Warn().Str("run_id", snap.RunID).Str("message_id", messageID).Msg("workflow run is actively executing or has registered waiters; late or duplicate reply safely ignored")
		return ResumeIgnored, nil, nil
	}

	// Guard 3: set replayPending flag with defer cleanup (N5/R4)
	e.replayPending[snap.RunID] = true
	defer func() {
		e.waitMu.Lock()
		delete(e.replayPending, snap.RunID)
		e.waitMu.Unlock()
	}()
	e.waitMu.Unlock()

	defn, err := workflowspec.ParseDefinition([]byte(snap.DAGSpec))
	if err != nil {
		return ResumeIgnored, nil, fmt.Errorf("restoring workflow definition for run %s: %w", snap.RunID, err)
	}

	rc := e.buildResumeContext(snap, "", emit)
	if targetNodeID != "" {
		rc.HumanReplies[targetNodeID] = replyText
	}
	if err := store.MarkRunning(snap.RunID); err != nil {
		log.Warn().Err(err).Str("run_id", snap.RunID).Msg("marking workflow run running failed")
	}
	res, err := e.Execute(ctx, defn, rc)
	return ResumeReDriven, res, err
}

// ResumeWithEmitter is like Resume but forwards every event of a re-driven run
// to emit (when non-nil) so hosts can persist and surface resume progress.
func (e *Engine) ResumeWithEmitter(ctx context.Context, runID string, replyText string, emit func(WorkflowEvent)) (ResumeOutcome, *WorkflowRunResult, error) {
	if runID == "" || replyText == "" {
		return ResumeIgnored, nil, fmt.Errorf("run_id and reply_text are required")
	}
	if e.deliverResume(runID, replyText) {
		return ResumeDeliveredLive, nil, nil
	}

	e.waitMu.Lock()
	if e.executing[runID] || len(e.waitingByRun[runID]) > 0 {
		e.waitMu.Unlock()
		log.Warn().Str("run_id", runID).Msg("workflow run is active or has pending human nodes; message_id required")
		return ResumeIgnored, nil, fmt.Errorf("workflow run %s is active or has multiple pending human nodes, message_id required", runID)
	}
	e.waitMu.Unlock()

	store := e.store
	if store == nil {
		return ResumeIgnored, nil, fmt.Errorf("workflow run store is not configured")
	}
	snap, err := store.GetRun(runID)
	if err != nil {
		return ResumeIgnored, nil, fmt.Errorf("loading workflow run %s: %w", runID, err)
	}
	if snap == nil {
		return ResumeIgnored, nil, fmt.Errorf("workflow run %s not found", runID)
	}
	if snap.Status != PersistStatusWaitingHuman {
		return ResumeIgnored, nil, fmt.Errorf("workflow run %s is not waiting for human input (status %s)", runID, snap.Status)
	}
	if len(snap.SuspendedNodes) > 1 {
		return ResumeIgnored, nil, fmt.Errorf("workflow run %s has multiple suspended nodes; message_id required", runID)
	}
	if snap.SuspendedNodeID == "" && len(snap.SuspendedNodes) == 0 {
		return ResumeIgnored, nil, fmt.Errorf("workflow run %s has no suspended node recorded", runID)
	}

	e.waitMu.Lock()
	if e.replayPending[snap.RunID] {
		e.waitMu.Unlock()
		return ResumeIgnored, nil, fmt.Errorf("workflow run %s is already replaying", runID)
	}
	e.replayPending[snap.RunID] = true
	defer func() {
		e.waitMu.Lock()
		delete(e.replayPending, snap.RunID)
		e.waitMu.Unlock()
	}()
	e.waitMu.Unlock()

	defn, err := workflowspec.ParseDefinition([]byte(snap.DAGSpec))
	if err != nil {
		return ResumeIgnored, nil, fmt.Errorf("restoring workflow definition for run %s: %w", runID, err)
	}

	rc := e.buildResumeContext(snap, replyText, emit)
	if err := store.MarkRunning(snap.RunID); err != nil {
		log.Warn().Err(err).Str("run_id", snap.RunID).Msg("marking workflow run running failed")
	}
	res, err := e.Execute(ctx, defn, rc)
	return ResumeReDriven, res, err
}

// FindWaitingRun returns the WAITING_HUMAN run snapshot for a session, if any.
// Deprecated (pending step-4 API migration): use FindWaitingRuns instead, which returns every concurrently suspended run of a session.
func (e *Engine) FindWaitingRun(sessionID string) (*RunSnapshot, error) {
	if e.store == nil {
		return nil, nil
	}
	return e.store.FindWaitingHuman(sessionID)
}

// FindWaitingRuns returns all WAITING_HUMAN run snapshots for a session, most recently updated first.
func (e *Engine) FindWaitingRuns(sessionID string) ([]*RunSnapshot, error) {
	if e.store == nil {
		return nil, nil
	}
	return e.store.FindWaitingHumans(sessionID)
}

// FindWaitingRunByMessageID returns the WAITING_HUMAN run snapshot owning the given ask_user MessageID.
func (e *Engine) FindWaitingRunByMessageID(messageID string) (*RunSnapshot, error) {
	if e.store == nil {
		return nil, nil
	}
	return e.store.FindWaitingHumanByMessageID(messageID)
}

// DeliverResumeByMessageID routes a reply to a live suspended worker matching messageID.
func (e *Engine) DeliverResumeByMessageID(messageID string, reply string) bool {
	e.waitMu.Lock()
	defer e.waitMu.Unlock()
	if w, ok := e.waitingByMsg[messageID]; ok {
		select {
		case w.replyCh <- reply:
			return true
		default:
		}
	}
	return false
}

// deliverResume routes a reply to a live suspended worker.
// When there is exactly 1 waiter for runID, it delivers the reply.
// When there are >1 waiters, it logs a warning and returns false.
func (e *Engine) deliverResume(runID string, reply string) bool {
	e.waitMu.Lock()
	defer e.waitMu.Unlock()
	nodeWaiters := e.waitingByRun[runID]
	if len(nodeWaiters) == 0 {
		return false
	}
	if len(nodeWaiters) > 1 {
		log.Warn().Str("run_id", runID).Int("waiters", len(nodeWaiters)).Msg("deliverResume: workflow has multiple waiting human nodes; message_id required")
		return false
	}
	for _, w := range nodeWaiters {
		select {
		case w.replyCh <- reply:
			return true
		default:
		}
	}
	return false
}
