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
	for _, artifact := range req.Artifacts {
		if err := s.repo.AppendArtifact(req.SessionID, artifact); err != nil {
			log.Warn().Err(err).Str("chat_id", req.SessionID).Str("artifact", artifact).Msg("failed to append workflow artifact to repo")
		}
	}
	if err := s.repo.AppendMessage(req.SessionID, dbmodels.ChatMessage{
		ID:            req.MessageID,
		Role:          "ask_user",
		Content:       req.Prompt,
		AgentName:     agentName,
		Timestamp:     time.Now().UnixMilli(),
		ArtifactFiles: req.Artifacts,
	}); err != nil {
		return err
	}
	s.SendPushNotification(req.SessionID, req.Prompt, agentName)
	return nil
}

// handleWorkflowEvent persists side effects of workflow node events. Node
// artifacts (e.g. command output_file results) are registered on the session
// so the frontend artifact viewer can list and open them. Node and workflow
// failures are appended as error messages so they remain visible in the chat
// after the stream closes or the page reloads.
func (s *Server) handleWorkflowEvent(sessionID string, ev workflow.WorkflowEvent) {
	if s.repo == nil || sessionID == "" {
		return
	}
	// Suspended human-node artifacts are registered by suspendWorkflowHuman.
	if ev.Type == workflow.EventWorkflowSuspended {
		return
	}
	if ev.Type == workflow.EventNodeStatusUpdate {
		for _, artifact := range ev.Artifacts {
			if err := s.repo.AppendArtifact(sessionID, artifact); err != nil {
				log.Warn().Err(err).Str("chat_id", sessionID).Str("artifact", artifact).Msg("failed to append workflow status artifact to repo")
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
			_ = s.repo.AppendMessage(sessionID, dbmodels.ChatMessage{
				ID:            fmt.Sprintf("wf-step-%s-%d-%d", ev.NodeID, time.Now().UnixMilli(), stepIdx),
				Role:          role,
				Content:       ev.Message,
				AgentName:     ev.AgentName,
				Timestamp:     time.Now().UnixMilli(),
				ActivityType:  strings.ToUpper(role),
				StepIndex:     stepIdx,
				TargetFiles:   targetFiles,
				ArtifactFiles: ev.Artifacts,
			})
		}
		return
	}
	for _, artifact := range ev.Artifacts {
		if err := s.repo.AppendArtifact(sessionID, artifact); err != nil {
			log.Warn().Err(err).Str("chat_id", sessionID).Str("artifact", artifact).Msg("failed to append workflow node artifact to repo")
		}
	}
	if ev.Status != workflow.StatusFailed {
		return
	}
	nodeRef := ev.NodeID
	if nodeRef == "" {
		nodeRef = "workflow"
	}
	if err := s.repo.AppendMessage(sessionID, dbmodels.ChatMessage{
		ID:        fmt.Sprintf("wf-error-%s-%d", nodeRef, ev.Timestamp.UnixMilli()),
		Role:      "error",
		Content:   ev.Message,
		AgentName: ev.AgentName,
		Timestamp: time.Now().UnixMilli(),
	}); err != nil {
		log.Warn().Err(err).Str("chat_id", sessionID).Str("node_id", ev.NodeID).Msg("failed to append workflow error message to repo")
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
		if _, err := engine.Resume(context.Background(), run.RunID, replyText); err != nil {
			log.Error().Err(err).Str("run_id", run.RunID).Msg("resuming workflow run failed")
		}
	}()
}
