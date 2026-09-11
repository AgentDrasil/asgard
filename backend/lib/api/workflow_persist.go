package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// workflowRunStore adapts the dbmodels WorkflowRun repository to the engine's
// workflow.RunStore contract.
type workflowRunStore struct {
	repo *dbmodels.WorkflowRunRepository
}

var _ workflow.RunStore = (*workflowRunStore)(nil)

func newWorkflowRunStore(repo *dbmodels.WorkflowRunRepository) *workflowRunStore {
	return &workflowRunStore{repo: repo}
}

func (s *workflowRunStore) StartRun(run *workflow.RunSnapshot) error {
	return s.repo.SaveRun(&dbmodels.WorkflowRun{
		RunID:       run.RunID,
		SessionID:   run.SessionID,
		Status:      dbmodels.WorkflowStatusRunning,
		DAGSpec:     run.DAGSpec,
		NodeStates:  "{}",
		RunDir:      run.RunDir,
		Input:       run.Input,
		ParentRunID: run.ParentRunID,
	})
}

func (s *workflowRunStore) MarkWaitingHuman(run *workflow.RunSnapshot) error {
	states, err := dbmodels.EncodeNodeStates(toDBNodeStates(run.NodeStates))
	if err != nil {
		return err
	}
	loopIterations, err := dbmodels.EncodeIntMap(run.LoopIterations)
	if err != nil {
		return err
	}
	executionCounts, err := dbmodels.EncodeIntMap(run.ExecutionCounts)
	if err != nil {
		return err
	}
	suspendedNodes, err := dbmodels.EncodeSuspendedNodes(toDBSuspendedNodes(run.SuspendedNodes))
	if err != nil {
		return err
	}
	return s.repo.SaveRun(&dbmodels.WorkflowRun{
		RunID:              run.RunID,
		SessionID:          run.SessionID,
		Status:             dbmodels.WorkflowStatusWaitingHuman,
		DAGSpec:            run.DAGSpec,
		NodeStates:         states,
		LoopIterations:     loopIterations,
		ExecutionCounts:    executionCounts,
		SuspendedNodeID:    run.SuspendedNodeID,
		SuspendedMessageID: run.SuspendedMessageID,
		SuspendedNodes:     suspendedNodes,
		ParentRunID:        run.ParentRunID,
		RunDir:             run.RunDir,
		Input:              run.Input,
	})
}

func (s *workflowRunStore) SettleRun(runID string, status string, states map[string]workflow.PersistedNodeState) error {
	run, err := s.repo.GetRunRow(runID)
	if err != nil {
		return err
	}
	if run == nil {
		return nil
	}
	encoded, err := dbmodels.EncodeNodeStates(toDBNodeStates(states))
	if err != nil {
		return err
	}
	switch status {
	case workflow.PersistStatusCompleted:
		run.Status = dbmodels.WorkflowStatusCompleted
	case workflow.PersistStatusFailed:
		run.Status = dbmodels.WorkflowStatusFailed
	case workflow.PersistStatusCancelled:
		run.Status = dbmodels.WorkflowStatusCancelled
	default:
		run.Status = status
	}
	run.NodeStates = encoded
	return s.repo.SaveRun(run)
}

func (s *workflowRunStore) GetRun(runID string) (*workflow.RunSnapshot, error) {
	run, err := s.repo.GetRun(runID)
	if err != nil || run == nil {
		return nil, err
	}
	return dbRunToSnapshot(run)
}

func (s *workflowRunStore) FindWaitingHuman(sessionID string) (*workflow.RunSnapshot, error) {
	run, err := s.repo.FindWaitingHumanBySession(sessionID)
	if err != nil || run == nil {
		return nil, err
	}
	return dbRunToSnapshot(run)
}

func (s *workflowRunStore) FindWaitingHumans(sessionID string) ([]*workflow.RunSnapshot, error) {
	runs, err := s.repo.FindWaitingHumansBySession(sessionID)
	if err != nil {
		return nil, err
	}
	snaps := make([]*workflow.RunSnapshot, 0, len(runs))
	for _, run := range runs {
		snap, err := dbRunToSnapshot(run)
		if err != nil {
			return nil, err
		}
		snaps = append(snaps, snap)
	}
	return snaps, nil
}

func (s *workflowRunStore) FindWaitingHumanByMessageID(messageID string) (*workflow.RunSnapshot, error) {
	run, err := s.repo.FindWaitingHumanByMessageID(messageID)
	if err != nil || run == nil {
		return nil, err
	}
	return dbRunToSnapshot(run)
}

func (s *workflowRunStore) RefreshSuspension(runID string, states map[string]workflow.PersistedNodeState, loopIterations, executionCounts map[string]int, suspendedNodes map[string]workflow.SuspendedNodeInfo) error {
	return s.repo.RefreshSuspension(
		runID,
		toDBNodeStates(states),
		loopIterations,
		executionCounts,
		toDBSuspendedNodes(suspendedNodes),
	)
}

func (s *workflowRunStore) MarkRunning(runID string) error {
	return s.repo.UpdateRunStatus(runID, dbmodels.WorkflowStatusRunning)
}

func dbRunToSnapshot(run *dbmodels.WorkflowRun) (*workflow.RunSnapshot, error) {
	states, err := dbmodels.DecodeNodeStates(run.NodeStates)
	if err != nil {
		return nil, err
	}
	loopIterations, err := dbmodels.DecodeIntMap(run.LoopIterations)
	if err != nil {
		return nil, err
	}
	executionCounts, err := dbmodels.DecodeIntMap(run.ExecutionCounts)
	if err != nil {
		return nil, err
	}
	suspendedNodes, err := dbmodels.DecodeSuspendedNodes(run.SuspendedNodes)
	if err != nil {
		return nil, err
	}
	snap := &workflow.RunSnapshot{
		RunID:              run.RunID,
		SessionID:          run.SessionID,
		Status:             run.Status,
		DAGSpec:            run.DAGSpec,
		RunDir:             run.RunDir,
		Input:              run.Input,
		LoopIterations:     loopIterations,
		ExecutionCounts:    executionCounts,
		SuspendedNodeID:    run.SuspendedNodeID,
		SuspendedMessageID: run.SuspendedMessageID,
		SuspendedNodes:     fromDBSuspendedNodes(suspendedNodes),
		ParentRunID:        run.ParentRunID,
		CreatedAt:          run.CreatedAt,
		UpdatedAt:          run.UpdatedAt,
	}
	snap.NodeStates = make(map[string]workflow.PersistedNodeState, len(states))
	for id, state := range states {
		snap.NodeStates[id] = workflow.PersistedNodeState{
			Status:     state.Status,
			ExitCode:   state.ExitCode,
			Output:     state.Output,
			OutputPath: state.OutputPath,
			SkipReason: state.SkipReason,
			Error:      state.Error,
			CLI:        state.CLI,
			Model:      state.Model,
		}
	}
	return snap, nil
}

func toDBNodeStates(states map[string]workflow.PersistedNodeState) map[string]dbmodels.NodeState {
	out := make(map[string]dbmodels.NodeState, len(states))
	for id, state := range states {
		out[id] = dbmodels.NodeState{
			Status:     state.Status,
			ExitCode:   state.ExitCode,
			Output:     state.Output,
			OutputPath: state.OutputPath,
			SkipReason: state.SkipReason,
			Error:      state.Error,
			CLI:        state.CLI,
			Model:      state.Model,
		}
	}
	return out
}

func toDBSuspendedNodes(nodes map[string]workflow.SuspendedNodeInfo) map[string]dbmodels.SuspendedNodeInfo {
	if nodes == nil {
		return nil
	}
	out := make(map[string]dbmodels.SuspendedNodeInfo, len(nodes))
	for id, info := range nodes {
		out[id] = dbmodels.SuspendedNodeInfo{
			MessageID: info.MessageID,
			Iteration: info.Iteration,
		}
	}
	return out
}

func fromDBSuspendedNodes(nodes map[string]dbmodels.SuspendedNodeInfo) map[string]workflow.SuspendedNodeInfo {
	if nodes == nil {
		return nil
	}
	out := make(map[string]workflow.SuspendedNodeInfo, len(nodes))
	for id, info := range nodes {
		out[id] = workflow.SuspendedNodeInfo{
			MessageID: info.MessageID,
			Iteration: info.Iteration,
		}
	}
	return out
}

// suspendWorkflowHuman delivers a human-node suspension to the chat session:
// it registers the prompt's artifact files, appends the deterministic ask_user
// message (with artifact references) and fires a push notification, mirroring
// the single-agent AskUser flow.
func (s *Server) suspendWorkflowHuman(req workflow.SuspendRequest) error {
	if s.repo == nil {
		return nil
	}
	agentName := req.AgentName
	if agentName == "" {
		if session, _ := s.repo.GetSession(req.SessionID); session != nil {
			agentName = session.CurrentAgent
		}
	}
	if len(req.Artifacts) > 0 {
		if err := s.repo.AppendArtifacts(req.SessionID, req.Artifacts); err != nil {
			log.Warn().Err(err).Str("chat_id", req.SessionID).Msg("failed to append workflow artifacts to repo")
		} else {
			s.PublishSessionEvent(req.SessionID, SessionEvent{
				Type:    "artifact",
				Payload: map[string]any{"artifacts": req.Artifacts},
			})
		}
	}
	msg := dbmodels.ChatMessage{
		ID:            req.MessageID,
		Role:          "ask_user",
		Content:       req.Prompt,
		AgentName:     agentName,
		Timestamp:     time.Now().UnixMilli(),
		ArtifactFiles: req.Artifacts,
	}
	if err := s.repo.AppendMessage(req.SessionID, msg); err != nil {
		return err
	}
	s.PublishSessionEvent(req.SessionID, SessionEvent{
		Type:    "message",
		Message: &msg,
	})
	s.PublishSessionEvent(req.SessionID, SessionEvent{
		Type:    "status",
		Payload: map[string]any{"agent": req.AgentName, "isRunning": false},
	})
	s.SendPushNotification(req.SessionID, req.Prompt, agentName)
	return nil
}

// resolveWorkflowAgentKey maps a workflow event's AgentName to the agent key
// registered on the session (Config.ID), falling back to CurrentAgent.
func (s *Server) resolveWorkflowAgentKey(sessionID, agentName string) string {
	if agentName != "" {
		s.mu.RLock()
		for _, a := range s.agents {
			if a.Config.Name == agentName || a.Config.ID == agentName {
				s.mu.RUnlock()
				return a.Config.ID
			}
		}
		s.mu.RUnlock()
	}
	if s.repo != nil && sessionID != "" {
		if sess, err := s.repo.GetSession(sessionID); err == nil && sess != nil && sess.CurrentAgent != "" {
			return sess.CurrentAgent
		}
	}
	return agentName
}

// findAskUserMessage returns the transcript message with the given ID, or nil
// when it does not exist. Used to detect ask bubbles that were already replied
// before deciding whether a re-suspension can reuse their message ID.
func findAskUserMessage(repo *dbmodels.SessionRepository, chatID, msgID string) *dbmodels.ChatMessage {
	if repo == nil || chatID == "" || msgID == "" {
		return nil
	}
	sess, err := repo.GetSession(chatID)
	if err != nil || sess == nil {
		return nil
	}
	for i := range sess.Messages {
		if sess.Messages[i].ID == msgID {
			return &sess.Messages[i]
		}
	}
	return nil
}

// handleWorkflowEvent persists side effects of workflow node events. Node
// artifacts (e.g. command output_file results) are registered on the session
// so the frontend artifact viewer can list and open them. Node and workflow
// failures are appended as error messages so they remain visible in the chat
// after the stream closes or the page reloads. Successful node final
// responses and the workflow summary are appended as assistant messages for
// the same reason.
func (s *Server) handleWorkflowEvent(sessionID string, ev workflow.WorkflowEvent) {
	if s.repo == nil || sessionID == "" {
		return
	}
	if ev.Type == workflow.EventWorkflowStarted || ev.Type == workflow.EventWorkflowResumed {
		agentKey := s.resolveWorkflowAgentKey(sessionID, ev.AgentName)
		s.ensureExecutionGuard(sessionID, agentKey, true)
		if agentKey != "" {
			_ = s.repo.UpdateAgentStatus(sessionID, agentKey, dbmodels.AgentStatusRunning)
		}
		agentName := ev.AgentName
		if agentName == "" {
			agentName = agentKey
		}
		s.PublishSessionEvent(sessionID, SessionEvent{
			Type: "status",
			Payload: map[string]any{
				"agent":     agentName,
				"isRunning": true,
			},
		})
		return
	}
	if ev.Type == workflow.EventNodeStarted {
		agentIdentifier := ev.AgentID
		if agentIdentifier == "" {
			agentIdentifier = ev.NodeID
		}
		s.PublishSessionEvent(sessionID, SessionEvent{
			Type: "status",
			Payload: map[string]any{
				"agent":      agentIdentifier,
				"agent_name": ev.AgentName,
				"node_id":    ev.NodeID,
				"isRunning":  true,
			},
		})
		return
	}
	if ev.Type == workflow.EventWorkflowSuspended {
		s.releaseExecution(sessionID)
		agentKey := s.resolveWorkflowAgentKey(sessionID, ev.AgentName)
		if agentKey != "" {
			_ = s.repo.UpdateAgentStatus(sessionID, agentKey, dbmodels.AgentStatusWaitingHuman)
		}
		s.PublishSessionEvent(sessionID, SessionEvent{
			Type: "status",
			Payload: map[string]any{
				"agent":     ev.AgentName,
				"node_id":   ev.NodeID,
				"isRunning": false,
			},
		})
		if len(ev.Artifacts) > 0 {
			if err := s.repo.AppendArtifacts(sessionID, ev.Artifacts); err != nil {
				log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to append workflow suspended artifacts to repo")
			} else {
				s.PublishSessionEvent(sessionID, SessionEvent{
					Type:    "artifact",
					Payload: map[string]any{"artifacts": ev.Artifacts},
				})
			}
		}
		if ev.Message != "" {
			msgID := ev.MessageID
			if msgID == "" {
				msgID = fmt.Sprintf("wf-suspended-%s-%d", ev.NodeID, time.Now().UnixMilli())
			}
			// A re-suspension may reuse a MessageID whose ask bubble was
			// already replied (e.g. re-drive after restart). AppendMessage
			// replaces in place and inherits Replied=true, so the ask would
			// stay invisible with nothing for the user to click. Append under
			// a fresh ID instead: the reply routes back through
			// tryResumeWorkflow's unique-waiting-run fallback.
			if existing := findAskUserMessage(s.repo, sessionID, msgID); existing != nil && existing.Replied {
				msgID = fmt.Sprintf("%s-reask-%d", msgID, time.Now().UnixMilli())
			}
			msg := dbmodels.ChatMessage{
				ID:            msgID,
				Role:          "ask_user",
				Content:       ev.Message,
				AgentName:     ev.AgentName,
				Timestamp:     time.Now().UnixMilli(),
				ArtifactFiles: ev.Artifacts,
			}
			if err := s.repo.AppendMessage(sessionID, msg); err != nil {
				log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to append workflow suspended message to repo")
			} else {
				s.PublishSessionEvent(sessionID, SessionEvent{
					Type:    "message",
					Message: &msg,
				})
			}
		}
		return
	}
	if ev.Type == workflow.EventNodeStatusUpdate {
		if len(ev.Artifacts) > 0 {
			if err := s.repo.AppendArtifacts(sessionID, ev.Artifacts); err != nil {
				log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to append workflow status artifacts to repo")
			} else {
				s.PublishSessionEvent(sessionID, SessionEvent{
					Type:    "artifact",
					Payload: map[string]any{"artifacts": ev.Artifacts},
				})
			}
		}
		if ev.Message != "" && ev.EntryType != "agent_response" {
			role := ev.EntryType
			if role == "" || role == "other" {
				role = "activity"
			}
			stepIdx := 0
			if idx, ok := ev.Metadata["step_index"].(int); ok {
				stepIdx = idx
			}
			targetFiles := toStringSlice(ev.Metadata["target_files"])

			// Derived message ID: concurrent fan-out sub-items share the same
			// parent NodeID and step_index, so their bubbled status updates
			// must be keyed by item_index / sub_node_id to avoid overwriting
			// each other in the session transcript.
			msgID := fmt.Sprintf("wf-step-%s-%d", ev.NodeID, stepIdx)
			_, hasItemIndex := ev.Metadata["item_index"]
			_, hasSubNodeID := ev.Metadata["sub_node_id"]
			if hasItemIndex || hasSubNodeID {
				msgID = fmt.Sprintf("wf-step-%s-%v-%s-%d", ev.NodeID, ev.Metadata["item_index"], ev.Metadata["sub_node_id"], stepIdx)
			}

			msg := dbmodels.ChatMessage{
				ID:            msgID,
				Role:          role,
				Content:       ev.Message,
				AgentName:     ev.AgentName,
				Timestamp:     time.Now().UnixMilli(),
				ActivityType:  strings.ToUpper(role),
				StepIndex:     stepIdx,
				TargetFiles:   targetFiles,
				ArtifactFiles: ev.Artifacts,
			}
			// High-frequency fan-out progress events are broadcast via SSE
			// only; persisting them would flood the session transcript. Only
			// the aggregated node status update is persisted.
			if ev.EntryType == "fanout_progress" {
				s.PublishSessionEvent(sessionID, SessionEvent{
					Type:    "message",
					Message: &msg,
				})
				return
			}
			if err := s.repo.AppendMessage(sessionID, msg); err != nil {
				log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to append workflow step status message to repo")
			} else {
				s.PublishSessionEvent(sessionID, SessionEvent{
					Type:    "message",
					Message: &msg,
				})
			}
		}
		return
	}
	if len(ev.Artifacts) > 0 {
		if err := s.repo.AppendArtifacts(sessionID, ev.Artifacts); err != nil {
			log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to append workflow node artifacts to repo")
		} else {
			s.PublishSessionEvent(sessionID, SessionEvent{
				Type:    "artifact",
				Payload: map[string]any{"artifacts": ev.Artifacts},
			})
		}
	}
	// When an agent node finishes successfully, resolve any pending quota ask_user
	// messages that were created during temporary quota suspensions for this node.
	// This prevents stale quota warning prompts from lingering in the session transcript
	// and obscuring subsequent interaction prompts (such as final approval).
	if ev.Type == workflow.EventNodeFinished && ev.Status == workflowspec.StatusSucceeded {
		s.resolvePendingQuotaMessages(sessionID, ev.NodeID)
	}

	// Persist a successful node's final response as an assistant message so
	// node agents' conclusions survive reloads (streamed agent_response
	// updates are intentionally not persisted to avoid step-level churn).
	if ev.Type == workflow.EventNodeFinished && ev.Status == workflowspec.StatusSucceeded && ev.Output != "" {
		msg := dbmodels.ChatMessage{
			ID:        fmt.Sprintf("wf-node-%s-%d", ev.NodeID, time.Now().UnixMilli()),
			Role:      "assistant",
			Content:   ev.Output,
			AgentName: ev.AgentName,
			Timestamp: time.Now().UnixMilli(),
		}
		if err := s.repo.AppendMessage(sessionID, msg); err != nil {
			log.Warn().Err(err).Str("chat_id", sessionID).Str("node_id", ev.NodeID).Msg("failed to append workflow node response to repo")
		} else {
			s.PublishSessionEvent(sessionID, SessionEvent{
				Type:    "message",
				Message: &msg,
			})
		}
		return
	}
	if ev.Type == workflow.EventWorkflowFinished {
		s.releaseExecution(sessionID)
		agentKey := s.resolveWorkflowAgentKey(sessionID, ev.AgentName)
		if agentKey != "" {
			_ = s.repo.UpdateAgentStatus(sessionID, agentKey, dbmodels.AgentStatusCompleted)
		}
		s.PublishSessionEvent(sessionID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"agent": ev.AgentName, "isRunning": false},
		})
		s.PublishSessionEvent(sessionID, SessionEvent{
			Type:    "done",
			Payload: map[string]any{"agent": ev.AgentName},
		})
		if ev.Status == workflowspec.NodeStatus(workflow.RunStatusCompleted) && ev.Message != "" {
			msg := dbmodels.ChatMessage{
				ID:        fmt.Sprintf("wf-summary-%d", time.Now().UnixMilli()),
				Role:      "assistant",
				Content:   ev.Message,
				AgentName: ev.AgentName,
				Timestamp: time.Now().UnixMilli(),
			}
			if err := s.repo.AppendMessage(sessionID, msg); err != nil {
				log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to append workflow summary to repo")
			} else {
				s.PublishSessionEvent(sessionID, SessionEvent{
					Type:    "message",
					Message: &msg,
				})
			}
		}
		return
	}
	if ev.Status != workflowspec.StatusFailed {
		return
	}
	// A user stop cancels every in-flight node; their "context canceled"
	// failures are not user-facing errors. The cancellation activity message
	// persisted by stopSessionExecution already explains the outcome. The
	// stop marker makes this precise (a genuine node error whose message
	// happens to embed "context canceled" is still persisted).
	if s.sessionStopInProgress(sessionID) {
		return
	}
	nodeRef := ev.NodeID
	if nodeRef == "" {
		nodeRef = "workflow"
	}
	msg := dbmodels.ChatMessage{
		ID:        fmt.Sprintf("wf-error-%s-%d", nodeRef, ev.Timestamp.UnixMilli()),
		Role:      "error",
		Content:   ev.Message,
		AgentName: ev.AgentName,
		Timestamp: time.Now().UnixMilli(),
	}
	if err := s.repo.AppendMessage(sessionID, msg); err != nil {
		log.Warn().Err(err).Str("chat_id", sessionID).Str("node_id", ev.NodeID).Msg("failed to append workflow error message to repo")
	} else {
		s.PublishSessionEvent(sessionID, SessionEvent{
			Type:    "message",
			Message: &msg,
		})
	}
}

// tryResumeWorkflow routes an ask-user reply to a suspended workflow run.
// When messageID is provided, it uses ResumeByMessageID with session ownership validation (m9).
// When messageID is empty (or matches no waiting interaction — e.g. a reply to
// a stale ask bubble left over from an earlier suspension), it counts
// totalWaiting across all suspended runs in the chat; if totalWaiting == 1, it
// gracefully falls back to resuming that interaction; otherwise it Warns/no-ops.
func (s *Server) tryResumeWorkflow(chatID string, messageID string, replyText string) {
	engine := s.workflowEngine
	if engine == nil {
		return
	}

	var targetMessageID string

	if messageID != "" {
		// Branch 1: Precise routing mode.
		// Check snapshot first
		snap, err := engine.FindWaitingRunByMessageID(messageID)
		if err != nil {
			log.Error().Err(err).Str("message_id", messageID).Msg("looking up waiting workflow run by message id failed")
			return
		}
		if snap != nil {
			// Validate session ownership to prevent cross-session hijacking (m9)
			if snap.SessionID != chatID {
				log.Warn().
					Str("chat_id", chatID).
					Str("snapshot_session_id", snap.SessionID).
					Str("message_id", messageID).
					Msg("ask-user reply session does not match workflow session; skipping resume")
				return
			}
			targetMessageID = messageID
		} else {
			// If snapshot not found by messageID, query session's WAITING_HUMAN snapshots to check for matching node
			runs, err := engine.FindWaitingRuns(chatID)
			if err != nil {
				log.Error().Err(err).Str("chat_id", chatID).Msg("looking up waiting workflow runs failed")
				return
			}
			matched := false
			for _, r := range runs {
				if r.SuspendedMessageID == messageID {
					matched = true
					break
				}
				for _, n := range r.SuspendedNodes {
					if n.MessageID == messageID {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if matched {
				targetMessageID = messageID
			} else {
				// The reply targets a bubble no waiting run owns (stale or
				// missing ask). Fall through to the unique-waiting fallback
				// instead of silently dropping the user's reply.
				log.Warn().
					Str("chat_id", chatID).
					Str("message_id", messageID).
					Msg("ask-user reply does not match any suspended workflow interaction; trying unique waiting interaction fallback")
			}
		}
	}

	if targetMessageID == "" {
		// Branch 2: Fallback mode (messageID == "" or unmatched)
		runs, err := engine.FindWaitingRuns(chatID)
		if err != nil {
			log.Error().Err(err).Str("chat_id", chatID).Msg("looking up waiting workflow runs failed")
			return
		}
		if len(runs) == 0 {
			return
		}

		totalWaiting := 0
		for _, r := range runs {
			if len(r.SuspendedNodes) > 0 {
				totalWaiting += len(r.SuspendedNodes)
				for _, n := range r.SuspendedNodes {
					targetMessageID = n.MessageID
				}
			} else if r.SuspendedMessageID != "" {
				totalWaiting += 1
				targetMessageID = r.SuspendedMessageID
			}
		}

		if totalWaiting == 0 {
			return
		}
		if totalWaiting > 1 {
			log.Warn().
				Str("chat_id", chatID).
				Int("total_waiting", totalWaiting).
				Msg("multiple suspended workflow interactions in chat; cannot disambiguate reply without message_id")
			return
		}
		// totalWaiting == 1: targetMessageID is uniquely determined.
	}

	go func() {
		s.ensureExecutionGuard(chatID, "", true)

		agentName := s.resolveWorkflowAgentKey(chatID, "")
		if agentName != "" && s.repo != nil {
			_ = s.repo.UpdateAgentStatus(chatID, agentName, dbmodels.AgentStatusRunning)
			s.PublishSessionEvent(chatID, SessionEvent{
				Type:    "status",
				Payload: map[string]any{"agent": agentName, "isRunning": true},
			})
		}

		emit := func(ev workflow.WorkflowEvent) {
			sid := ev.SessionID
			if sid == "" {
				sid = chatID
			}
			s.handleWorkflowEvent(sid, ev)
		}

		outcome, _, err := engine.ResumeByMessageID(context.Background(), targetMessageID, replyText, emit)
		if err != nil || outcome == workflow.ResumeIgnored {
			if err != nil {
				log.Warn().Err(err).Str("message_id", targetMessageID).Msg("resuming workflow by message id failed")
			}
			// Defense fuse: skip rollback if engine is actively executing session
			if s.workflowEngine != nil && s.workflowEngine.IsSessionExecuting(chatID) {
				log.Warn().Str("chat_id", chatID).Msg("resume ignored or errored but engine is still executing session; skipping rollback")
			} else {
				s.releaseExecution(chatID)
				if agentName != "" && s.repo != nil {
					// The run may still be WAITING_HUMAN (e.g. the engine held
					// a live waiter): keep the waiting status instead of
					// flipping to completed.
					status := dbmodels.AgentStatusCompleted
					if runs, rerr := engine.FindWaitingRuns(chatID); rerr == nil && len(runs) > 0 {
						status = dbmodels.AgentStatusWaitingHuman
					}
					_ = s.repo.UpdateAgentStatus(chatID, agentName, status)
					s.PublishSessionEvent(chatID, SessionEvent{
						Type:    "status",
						Payload: map[string]any{"agent": agentName, "isRunning": false},
					})
				}
			}
		}
		// When outcome == ResumeDeliveredLive or ResumeReDriven: lifecycle is fully driven by handleWorkflowEvent
	}()
}

// resolvePendingQuotaMessages scans the session transcript for unreplied quota ask_user messages
// belonging to the completed node, marking them as replied with "Quota recovered".
// This cleans up any orphaned quota suspension prompts created by consecutive suspension retries.
func (s *Server) resolvePendingQuotaMessages(sessionID, nodeID string) {
	if s.repo == nil || sessionID == "" || nodeID == "" {
		return
	}
	session, err := s.repo.GetSession(sessionID)
	if err != nil || session == nil {
		return
	}

	quotaTarget := nodeID + "-quota"
	for _, m := range session.Messages {
		if m.Role != "ask_user" || m.Replied {
			continue
		}
		if matchesQuotaMessageID(m.ID, quotaTarget) {
			updatedMsg, err := s.repo.MarkAskUserReplied(sessionID, m.ID, "Quota recovered")
			if err != nil {
				log.Warn().Err(err).Str("chat_id", sessionID).Str("message_id", m.ID).Msg("failed to mark pending quota message replied")
			} else if updatedMsg != nil {
				s.PublishSessionEvent(sessionID, SessionEvent{
					Type:    "message",
					Message: updatedMsg,
				})
			}
		}
	}
}

// matchesQuotaMessageID reports whether msgID is a quota suspension message ID
// for quotaTarget (nodeID + "-quota"). Quota IDs look like
// wf-<runID>-<nodeID>-quota with at most two numeric suffix groups (loop
// iteration, suspension sequence), e.g. wf-r1-dev-quota-3-2. The leading "-"
// boundary prevents matching quota messages of nodes whose IDs merely end
// with nodeID (e.g. "xdev"), and the numeric-only tail prevents matching
// human-node IDs that merely start with "<nodeID>-quota-" (e.g.
// wf-r1-dev-quota-gate).
func matchesQuotaMessageID(msgID, quotaTarget string) bool {
	i := strings.LastIndex(msgID, "-"+quotaTarget)
	if i < 0 {
		return false
	}
	tail := msgID[i+1+len(quotaTarget):]
	if tail == "" {
		return true
	}
	for _, group := range strings.Split(strings.TrimPrefix(tail, "-"), "-") {
		if _, err := strconv.Atoi(group); err != nil {
			return false
		}
	}
	return true
}
