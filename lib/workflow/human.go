package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
)

// runHumanNode executes a `type: human` node. When a reply was pre-supplied
// (resume path) the node settles immediately; otherwise the run suspends: the
// WAITING_HUMAN snapshot is persisted, the suspension is delivered to the host
// application and the worker blocks until Resume delivers the user reply or
// the run is cancelled.
func (e *Engine) runHumanNode(ctx context.Context, rc RunContext, nctx *NodeContext, store RunStore, dagSpec string, snapshotStates func() map[string]PersistedNodeState) *NodeResult {
	node := nctx.Node
	emit := nctx.EventEmitter

	if reply := rc.HumanReplies[node.ID]; reply != "" {
		return humanReplyResult(nctx, reply)
	}

	suspend := rc.SuspendHuman
	if suspend == nil {
		suspend = e.suspendHuman
	}
	if suspend == nil {
		return &NodeResult{Status: StatusFailed, Error: fmt.Errorf("node %s: no human suspension gateway configured", node.ID)}
	}

	prompt := nctx.Interpolate(node.Prompt)
	if len(node.Options) > 0 {
		prompt = prompt + "\n\nOptions: " + strings.Join(node.Options, " / ")
	}
	messageID := HumanMessageID(rc.RunID, node.ID)

	// Register the waiter before delivering the suspension so a racing
	// Resume can never fall through to the snapshot re-drive path while a
	// live worker is about to block.
	replyCh := make(chan string, 1)
	e.waitMu.Lock()
	e.waiting[rc.RunID] = replyCh
	e.waitMu.Unlock()
	defer func() {
		e.waitMu.Lock()
		delete(e.waiting, rc.RunID)
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
	}); err != nil {
		return &NodeResult{Status: StatusFailed, Error: fmt.Errorf("node %s: delivering human suspension: %w", node.ID, err)}
	}

	if store != nil {
		if err := store.MarkWaitingHuman(&RunSnapshot{
			RunID:              rc.RunID,
			SessionID:          rc.SessionID,
			Status:             PersistStatusWaitingHuman,
			DAGSpec:            dagSpec,
			RunDir:             rc.RunDir,
			Input:              rc.Input,
			NodeStates:         snapshotStates(),
			SuspendedNodeID:    node.ID,
			SuspendedMessageID: messageID,
		}); err != nil {
			log.Warn().Err(err).Str("run_id", rc.RunID).Msg("persisting WAITING_HUMAN snapshot failed")
		}
	}

	emit(WorkflowEvent{
		Type:      EventWorkflowSuspended,
		NodeID:    node.ID,
		NodeType:  NodeTypeHuman,
		Status:    NodeStatus(RunStatusWaitingHuman),
		Message:   prompt,
		MessageID: messageID,
	})

	select {
	case reply := <-replyCh:
		emit(WorkflowEvent{
			Type:     EventWorkflowResumed,
			NodeID:   node.ID,
			NodeType: NodeTypeHuman,
			Status:   StatusRunning,
			Message:  fmt.Sprintf("node %s resumed with user reply", node.ID),
		})
		return humanReplyResult(nctx, reply)
	case <-ctx.Done():
		return &NodeResult{
			Status: StatusFailed,
			Error:  fmt.Errorf("node %s: run cancelled while waiting for human input", node.ID),
		}
	}
}

// humanReplyResult settles a human node from a user reply, persisting the
// reply into the node's declared output_file artifact when configured.
func humanReplyResult(nctx *NodeContext, reply string) *NodeResult {
	result := &NodeResult{Status: StatusSucceeded, Output: reply}
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
// in-process waiter the reply unblocks it and (nil, nil) is returned.
// Otherwise (e.g. after a server restart) the run is re-driven from its
// persisted snapshot with the suspended node settled from the reply.
func (e *Engine) Resume(ctx context.Context, runID string, replyText string) (*WorkflowRunResult, error) {
	if runID == "" || replyText == "" {
		return nil, fmt.Errorf("run_id and reply_text are required")
	}
	if e.deliverResume(runID, replyText) {
		return nil, nil
	}

	store := e.store
	if store == nil {
		return nil, fmt.Errorf("workflow run store is not configured")
	}
	snap, err := store.GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("loading workflow run %s: %w", runID, err)
	}
	if snap == nil {
		return nil, fmt.Errorf("workflow run %s not found", runID)
	}
	if snap.Status != PersistStatusWaitingHuman {
		return nil, fmt.Errorf("workflow run %s is not waiting for human input (status %s)", runID, snap.Status)
	}
	if snap.SuspendedNodeID == "" {
		return nil, fmt.Errorf("workflow run %s has no suspended node recorded", runID)
	}

	defn, err := ParseDefinition([]byte(snap.DAGSpec))
	if err != nil {
		return nil, fmt.Errorf("restoring workflow definition for run %s: %w", runID, err)
	}

	rc := RunContext{
		RunID:        snap.RunID,
		SessionID:    snap.SessionID,
		RunDir:       snap.RunDir,
		Input:        snap.Input,
		DAGSpec:      snap.DAGSpec,
		Store:        store,
		SuspendHuman: e.suspendHuman,
		SeedNodes:    fromPersistedStates(snap.NodeStates),
		HumanReplies: map[string]string{snap.SuspendedNodeID: replyText},
		EmitEvent: func(ev WorkflowEvent) {
			log.Info().Str("run_id", runID).Str("node", ev.NodeID).Str("type", string(ev.Type)).Msg("resumed workflow event")
		},
	}
	return e.Execute(ctx, defn, rc)
}

// FindWaitingRun returns the WAITING_HUMAN run snapshot for a session, if any.
func (e *Engine) FindWaitingRun(sessionID string) (*RunSnapshot, error) {
	if e.store == nil {
		return nil, nil
	}
	return e.store.FindWaitingHuman(sessionID)
}

// deliverResume routes a reply to a live suspended worker.
func (e *Engine) deliverResume(runID string, reply string) bool {
	e.waitMu.Lock()
	defer e.waitMu.Unlock()
	if ch, ok := e.waiting[runID]; ok {
		select {
		case ch <- reply:
			return true
		default:
		}
	}
	return false
}
