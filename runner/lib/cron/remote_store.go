package cron

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// remoteRunStore adapts the engine's workflow.RunStore to report run progress
// to the backend over the AgentCallbackService gRPC connection.
type remoteRunStore struct {
	callbacks func() (runnerpb.AgentCallbackServiceClient, error)
}

func newRemoteRunStore(callbacks func() (runnerpb.AgentCallbackServiceClient, error)) workflow.RunStore {
	return &remoteRunStore{callbacks: callbacks}
}

func (s *remoteRunStore) StartRun(run *workflow.RunSnapshot) error {
	if s.callbacks == nil {
		return nil
	}
	cb, err := s.callbacks()
	if err != nil || cb == nil {
		log.Warn().Err(err).Str("run_id", run.RunID).Msg("remoteRunStore.StartRun: callbacks unavailable")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = cb.WorkflowRunStart(ctx, &runnerpb.WorkflowRunStartRequest{
		RunId:             run.RunID,
		SessionId:         run.SessionID,
		Status:            run.Status,
		DagSpec:           run.DAGSpec,
		RunDir:            run.RunDir,
		Input:             run.Input,
		ParentRunId:       run.ParentRunID,
		AllowCrossSession: run.AllowCrossSession,
	})
	if err != nil {
		log.Warn().Err(err).Str("run_id", run.RunID).Msg("remoteRunStore.StartRun failed to report to backend")
	}
	return nil
}

func (s *remoteRunStore) MarkWaitingHuman(run *workflow.RunSnapshot) error {
	// Cron workflows run headlessly (no_human: true).
	return nil
}

func (s *remoteRunStore) GetRun(runID string) (*workflow.RunSnapshot, error) {
	return nil, nil
}

func (s *remoteRunStore) FindWaitingHuman(sessionID string) (*workflow.RunSnapshot, error) {
	return nil, nil
}

func (s *remoteRunStore) FindWaitingHumans(sessionID string) ([]*workflow.RunSnapshot, error) {
	return nil, nil
}

func (s *remoteRunStore) FindWaitingHumanByMessageID(messageID string) (*workflow.RunSnapshot, error) {
	return nil, nil
}

func (s *remoteRunStore) RefreshSuspension(runID string, states map[string]workflow.PersistedNodeState, loopIterations, executionCounts map[string]int, suspendedNodes map[string]workflow.SuspendedNodeInfo) error {
	return nil
}

func (s *remoteRunStore) MarkRunning(runID string) error {
	return nil
}

func (s *remoteRunStore) SettleRun(runID string, status string, states map[string]workflow.PersistedNodeState) error {
	if s.callbacks == nil {
		return nil
	}
	cb, err := s.callbacks()
	if err != nil || cb == nil {
		log.Warn().Err(err).Str("run_id", runID).Msg("remoteRunStore.SettleRun: callbacks unavailable")
		return nil
	}
	rawStates, err := json.Marshal(states)
	if err != nil {
		log.Warn().Err(err).Str("run_id", runID).Msg("remoteRunStore.SettleRun: failed to marshal node states")
		rawStates = []byte("{}")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = cb.WorkflowRunSettle(ctx, &runnerpb.WorkflowRunSettleRequest{
		RunId:          runID,
		Status:         status,
		NodeStatesJson: rawStates,
	})
	if err != nil {
		log.Warn().Err(err).Str("run_id", runID).Msg("remoteRunStore.SettleRun failed to report to backend")
	}
	return nil
}
