package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// QuotaMessageID derives the deterministic ask_user MessageID for an agent
// node suspended on quota exhaustion. It is distinct from human-node IDs
// (suffixed "-quota") and appends the in-execution suspension counter when the
// same node execution re-suspends after a "continue" reply that found quota
// still exhausted.
func QuotaMessageID(runID, nodeID string, iteration, seq int) string {
	base := HumanMessageID(runID, nodeID+"-quota", iteration)
	if seq > 1 {
		return fmt.Sprintf("%s-%d", base, seq)
	}
	return base
}

// runQuotaSuspension parks an agent node waiting for a user quota decision,
// mirroring runHumanNode: it registers a waiter, persists the WAITING_HUMAN
// snapshot, delivers the suspension (ask_user chat message with option
// buttons) and blocks until Resume delivers the user reply or the run is
// cancelled. markSuspended flags the run as suspended for inline persistence
// semantics; emit forwards lifecycle events to the run subscriber.
func (e *Engine) runQuotaSuspension(
	ctx context.Context,
	rc RunContext,
	node *workflowspec.NodeSpec,
	iteration, seq int,
	store RunStore,
	dagSpec string,
	snapshotStates func() snapshotCapture,
	markSuspended func(),
	emit func(WorkflowEvent),
	prompt string,
	options []string,
) (string, error) {
	// Pre-supplied reply (re-drive from a persisted snapshot after restart):
	// settle without suspending again so the runner can apply the decision.
	// Consumed once: if the quota is still exhausted after applying it, the
	// next suspension waits for a fresh user decision instead of replaying
	// this reply in a loop.
	if reply := rc.HumanReplies[node.ID]; reply != "" {
		delete(rc.HumanReplies, node.ID)
		return reply, nil
	}

	suspend := rc.SuspendHuman
	if suspend == nil {
		suspend = e.suspendHuman
	}
	if suspend == nil {
		return "", fmt.Errorf("node %s: no suspension gateway configured for quota decision", node.ID)
	}

	promptText := prompt
	if len(options) > 0 {
		promptText = promptText + "\n\nOptions: " + strings.Join(options, " / ")
	}

	replyCh := make(chan string, 1)

	e.waitMu.Lock()
	// Re-suspension after a restart reuses the original MessageID so the
	// ask_user reply routes back to this node (N1).
	var messageID string
	if originalMsgID, ok := rc.ReuseMessageIDs[node.ID]; ok && originalMsgID != "" {
		messageID = originalMsgID
		delete(rc.ReuseMessageIDs, node.ID)
	} else {
		messageID = QuotaMessageID(rc.RunID, node.ID, iteration, seq)
	}

	waiter := &humanWaiter{
		replyCh:   replyCh,
		nodeID:    node.ID,
		messageID: messageID,
		iteration: iteration,
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
		captured := snapshotStates()
		delete(captured.nodeStates, node.ID)
		for nid := range suspendedNodesMap {
			delete(captured.nodeStates, nid)
		}
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
			log.Warn().Err(err).Str("run_id", rc.RunID).Str("node_id", node.ID).Msg("marking workflow waiting for quota decision failed")
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
		Prompt:    promptText,
		Options:   options,
		AgentName: rc.AgentName,
	}); err != nil {
		return "", fmt.Errorf("node %s: delivering quota suspension: %w", node.ID, err)
	}

	markSuspended()

	emit(WorkflowEvent{
		Type:      EventWorkflowSuspended,
		NodeID:    node.ID,
		NodeType:  node.Type,
		AgentID:   node.AgentID,
		Status:    workflowspec.NodeStatus(RunStatusWaitingHuman),
		Message:   promptText,
		MessageID: messageID,
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
			NodeType: node.Type,
			AgentID:  node.AgentID,
			Status:   workflowspec.StatusRunning,
			Message:  fmt.Sprintf("node %s resumed with user quota decision", node.ID),
		})
		return reply, nil
	case <-ctx.Done():
		return "", fmt.Errorf("node %s: run cancelled while waiting for quota decision", node.ID)
	}
}
