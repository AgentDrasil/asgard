package workflow

import (
	"errors"
	"fmt"
	"time"
)

// Persisted run statuses used by RunStore implementations. Note CANCELLED (two
// Ls) is the persisted spelling, matching the WorkflowRun DB model.
const (
	PersistStatusRunning      = "RUNNING"
	PersistStatusWaitingHuman = "WAITING_HUMAN"
	PersistStatusCompleted    = "COMPLETED"
	PersistStatusFailed       = "FAILED"
	PersistStatusCancelled    = "CANCELLED"
)

// HumanMessageID derives the deterministic ask_user MessageID for a suspended
// human node: wf-<run_id>-<node_id> (or wf-<run_id>-<node_id>-<iteration> if iteration > 1).
func HumanMessageID(runID, nodeID string, iteration ...int) string {
	if len(iteration) > 0 && iteration[0] > 1 {
		return fmt.Sprintf("wf-%s-%s-%d", runID, nodeID, iteration[0])
	}
	return fmt.Sprintf("wf-%s-%s", runID, nodeID)
}

// PersistedNodeState is the serializable settled state of one node.
type PersistedNodeState struct {
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Output     string `json:"output,omitempty"`
	OutputPath string `json:"output_path,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
	Error      string `json:"error,omitempty"`
}

// RunSnapshot is the persisted state of one workflow run.
type RunSnapshot struct {
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	DAGSpec   string `json:"dag_spec"`
	RunDir    string `json:"run_dir"`
	Input     string `json:"input"`
	// NodeStates holds the settled node results at snapshot time.
	NodeStates map[string]PersistedNodeState `json:"node_states"`
	// SuspendedNodeID / SuspendedMessageID identify the active human node
	// while Status is WAITING_HUMAN.
	SuspendedNodeID    string `json:"suspended_node_id,omitempty"`
	SuspendedMessageID string `json:"suspended_message_id,omitempty"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// RunStore persists workflow run snapshots for pause/resume and crash
// recovery. Implementations must be safe for concurrent use.
type RunStore interface {
	// StartRun records (or resets) a run in RUNNING state with its DAG snapshot.
	StartRun(run *RunSnapshot) error
	// MarkWaitingHuman persists the WAITING_HUMAN suspension point.
	MarkWaitingHuman(run *RunSnapshot) error
	// SettleRun records the terminal status of a run.
	SettleRun(runID string, status string, states map[string]PersistedNodeState) error
	// GetRun loads a run snapshot by run ID (nil, nil when not found).
	GetRun(runID string) (*RunSnapshot, error)
	// FindWaitingHuman returns the WAITING_HUMAN run for a session (nil, nil
	// when none is suspended).
	FindWaitingHuman(sessionID string) (*RunSnapshot, error)
}

// SuspendRequest describes a human-node suspension delivered to the host
// application (chat message append, push notification, ...).
type SuspendRequest struct {
	RunID     string
	SessionID string
	NodeID    string
	// MessageID is the deterministic ask_user MessageID (wf-<run_id>-<node_id>).
	MessageID string
	// Prompt is the rendered prompt text (options appended when configured).
	Prompt  string
	Options []string
	// AgentName names the workflow agent for chat routing / notifications.
	AgentName string
	// Artifacts lists viewer-facing artifact file paths referenced by the
	// prompt (e.g. the plan/review documents the user is asked to inspect).
	Artifacts []string
}

// SuspendHumanFunc delivers the suspension to the user. Returning an error
// fails the suspended node.
type SuspendHumanFunc func(req SuspendRequest) error

// toPersistedStates converts settled engine results into persistable states.
func toPersistedStates(results map[string]*NodeResult) map[string]PersistedNodeState {
	states := make(map[string]PersistedNodeState, len(results))
	for id, res := range results {
		if res == nil {
			continue
		}
		state := PersistedNodeState{
			Status:     string(res.Status),
			ExitCode:   res.ExitCode,
			Output:     res.Output,
			SkipReason: string(res.SkipReason),
		}
		if res.Error != nil {
			state.Error = res.Error.Error()
		}
		for _, path := range res.Artifacts {
			state.OutputPath = path
			break
		}
		states[id] = state
	}
	return states
}

// fromPersistedStates rebuilds seeded node results from persisted states.
func fromPersistedStates(states map[string]PersistedNodeState) map[string]*NodeResult {
	results := make(map[string]*NodeResult, len(states))
	for id, state := range states {
		res := &NodeResult{
			Status:     NodeStatus(state.Status),
			SkipReason: SkipReason(state.SkipReason),
			ExitCode:   state.ExitCode,
			Output:     state.Output,
		}
		if state.Error != "" {
			res.Error = errors.New(state.Error)
		}
		results[id] = res
	}
	return results
}

// persistStatus maps an engine run status to its persisted spelling.
func persistStatus(status WorkflowRunStatus) string {
	if status == RunStatusCanceled {
		return PersistStatusCancelled
	}
	return string(status)
}
