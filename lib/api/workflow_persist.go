package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/dbmodels"
	"github.com/AgentDrasil/asgard/lib/workflow"
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
		RunID:      run.RunID,
		SessionID:  run.SessionID,
		Status:     dbmodels.WorkflowStatusRunning,
		DAGSpec:    run.DAGSpec,
		NodeStates: "{}",
		RunDir:     run.RunDir,
		Input:      run.Input,
	})
}

func (s *workflowRunStore) MarkWaitingHuman(run *workflow.RunSnapshot) error {
	states, err := dbmodels.EncodeNodeStates(toDBNodeStates(run.NodeStates))
	if err != nil {
		return err
	}
	return s.repo.SaveRun(&dbmodels.WorkflowRun{
		RunID:              run.RunID,
		SessionID:          run.SessionID,
		Status:             dbmodels.WorkflowStatusWaitingHuman,
		DAGSpec:            run.DAGSpec,
		NodeStates:         states,
		SuspendedNodeID:    run.SuspendedNodeID,
		SuspendedMessageID: run.SuspendedMessageID,
		RunDir:             run.RunDir,
		Input:              run.Input,
	})
}

func (s *workflowRunStore) SettleRun(runID string, status string, states map[string]workflow.PersistedNodeState) error {
	run, err := s.repo.GetRun(runID)
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

func dbRunToSnapshot(run *dbmodels.WorkflowRun) (*workflow.RunSnapshot, error) {
	states, err := dbmodels.DecodeNodeStates(run.NodeStates)
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
		SuspendedNodeID:    run.SuspendedNodeID,
		SuspendedMessageID: run.SuspendedMessageID,
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
	s.SendPushNotification(req.SessionID, req.Prompt, agentName)
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
	if ev.Type == workflow.EventWorkflowSuspended {
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
			msg := dbmodels.ChatMessage{
				ID:            fmt.Sprintf("wf-step-%s-%d-%d", ev.NodeID, time.Now().UnixMilli(), stepIdx),
				Role:          role,
				Content:       ev.Message,
				AgentName:     ev.AgentName,
				Timestamp:     time.Now().UnixMilli(),
				ActivityType:  strings.ToUpper(role),
				StepIndex:     stepIdx,
				TargetFiles:   targetFiles,
				ArtifactFiles: ev.Artifacts,
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
	// Persist a successful node's final response as an assistant message so
	// node agents' conclusions survive reloads (streamed agent_response
	// updates are intentionally not persisted to avoid step-level churn).
	if ev.Type == workflow.EventNodeFinished && ev.Status == workflow.StatusSucceeded && ev.Output != "" {
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
	if ev.Type == workflow.EventWorkflowFinished && ev.Status == workflow.NodeStatus(workflow.RunStatusCompleted) && ev.Message != "" {
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
		return
	}
	if ev.Status != workflow.StatusFailed {
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

// tryResumeWorkflow routes an ask-user reply to a suspended workflow run for
// the chat. The reply's message_id must match the run's deterministic
// suspended_message_id; an empty message_id falls back to the single waiting
// run for the chat.
func (s *Server) tryResumeWorkflow(chatID string, messageID string, replyText string) {
	engine := s.workflowEngine
	if engine == nil {
		return
	}
	run, err := engine.FindWaitingRun(chatID)
	if err != nil {
		log.Error().Err(err).Str("chat_id", chatID).Msg("looking up waiting workflow run failed")
		return
	}
	if run == nil {
		return
	}
	if messageID != "" && messageID != run.SuspendedMessageID {
		log.Info().
			Str("chat_id", chatID).
			Str("message_id", messageID).
			Str("suspended_message_id", run.SuspendedMessageID).
			Msg("ask-user reply does not match suspended workflow message; skipping workflow resume")
		return
	}
	go func() {
		if s.repo != nil {
			if sess, err := s.repo.GetSession(chatID); err == nil && sess != nil && sess.CurrentAgent != "" {
				if err := s.repo.UpdateAgentStatus(chatID, sess.CurrentAgent, dbmodels.AgentStatusRunning); err != nil {
					log.Warn().Err(err).Str("chat_id", chatID).Str("agent", sess.CurrentAgent).Msg("failed to update agent status to running on workflow resume")
				} else {
					s.PublishSessionEvent(chatID, SessionEvent{
						Type:    "status",
						Payload: map[string]any{"agent": sess.CurrentAgent, "isRunning": true},
					})
				}
				defer func() {
					if err := s.repo.UpdateAgentStatus(chatID, sess.CurrentAgent, dbmodels.AgentStatusCompleted); err != nil {
						log.Warn().Err(err).Str("chat_id", chatID).Str("agent", sess.CurrentAgent).Msg("failed to mark agent status completed on workflow resume finish")
					} else {
						s.PublishSessionEvent(chatID, SessionEvent{
							Type:    "status",
							Payload: map[string]any{"agent": sess.CurrentAgent, "isRunning": false},
						})
						s.PublishSessionEvent(chatID, SessionEvent{
							Type:    "done",
							Payload: map[string]any{"agent": sess.CurrentAgent},
						})
					}
				}()
			}
		}
		// Re-driven runs have no live A2A stream, so route their events into
		// the persistence handler: node outputs, errors, summary and any
		// follow-up human suspension land in the session transcript instead
		// of vanishing (only visible after a refresh at worst, lost forever
		// at best).
		emit := func(ev workflow.WorkflowEvent) {
			sid := ev.SessionID
			if sid == "" {
				sid = chatID
			}
			s.handleWorkflowEvent(sid, ev)
		}
		if _, err := engine.ResumeWithEmitter(context.Background(), run.RunID, replyText, emit); err != nil {
			log.Error().Err(err).Str("run_id", run.RunID).Msg("resuming workflow run failed")
		}
	}()
}
