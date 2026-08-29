package workflow

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
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
	Status         string         `json:"status"`
	ExitCode       int            `json:"exit_code,omitempty"`
	Output         string         `json:"output,omitempty"`
	OutputPath     string         `json:"output_path,omitempty"`
	SkipReason     string         `json:"skip_reason,omitempty"`
	Error          string         `json:"error,omitempty"`
	LoopIterations map[string]int `json:"loop_iterations,omitempty"`
}

// SuspendedNodeInfo describes one concurrently suspended human node. The JSON
// tags must stay byte-for-byte identical to dbmodels.SuspendedNodeInfo.
type SuspendedNodeInfo struct {
	MessageID string `json:"message_id"`
	Iteration int    `json:"iteration"`
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
	// LoopIterations persists the per-loop iteration counters so loop
	// circuit breakers survive restarts (resume seeds them back).
	LoopIterations map[string]int `json:"loop_iterations,omitempty"`
	// ExecutionCounts persists the per-node execution counts so quota caps
	// and deterministic human MessageIDs stay stable across restarts.
	ExecutionCounts map[string]int `json:"execution_counts,omitempty"`
	// SuspendedNodeID / SuspendedMessageID identify the most recently
	// suspended human node while Status is WAITING_HUMAN (compatibility with
	// single-suspension snapshots; the full set lives in SuspendedNodes).
	SuspendedNodeID    string `json:"suspended_node_id,omitempty"`
	SuspendedMessageID string `json:"suspended_message_id,omitempty"`
	// SuspendedNodes holds every concurrently suspended human node of the
	// run, keyed by node ID.
	SuspendedNodes map[string]SuspendedNodeInfo `json:"suspended_nodes,omitempty"`
	// ParentRunID links a sub-workflow (inline) run to its parent run.
	ParentRunID string `json:"parent_run_id,omitempty"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// snapshotCapture bundles the state MarkWaitingHuman persists: settled node
// results plus the loop iteration / execution counters, deep-copied under the
// engine lock.
type snapshotCapture struct {
	nodeStates      map[string]PersistedNodeState
	loopIterations  map[string]int
	executionCounts map[string]int
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
	//
	// Deprecated: use FindWaitingHumans instead, which returns every
	// concurrently suspended run of a session.
	FindWaitingHuman(sessionID string) (*RunSnapshot, error)
	// FindWaitingHumans returns all WAITING_HUMAN runs of a session, most
	// recently updated first.
	FindWaitingHumans(sessionID string) ([]*RunSnapshot, error)
	// FindWaitingHumanByMessageID returns the WAITING_HUMAN run owning the
	// given ask_user MessageID (nil, nil when not found).
	FindWaitingHumanByMessageID(messageID string) (*RunSnapshot, error)
	// RefreshSuspension atomically overwrites a suspended run's settled node
	// states, counters and (possibly pruned) suspended node set.
	RefreshSuspension(runID string, states map[string]PersistedNodeState, loopIterations, executionCounts map[string]int, suspendedNodes map[string]SuspendedNodeInfo) error
	// MarkRunning updates the persisted status of a run to RUNNING when it resumes execution.
	MarkRunning(runID string) error
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
func toPersistedStates(results map[string]*workflowspec.NodeResult) map[string]PersistedNodeState {
	states := make(map[string]PersistedNodeState, len(results))
	for id, res := range results {
		if res == nil {
			continue
		}
		state := PersistedNodeState{
			Status:         string(res.Status),
			ExitCode:       res.ExitCode,
			Output:         res.Output,
			SkipReason:     string(res.SkipReason),
			LoopIterations: copyIntMap(res.LoopIterations),
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
func fromPersistedStates(states map[string]PersistedNodeState) map[string]*workflowspec.NodeResult {
	results := make(map[string]*workflowspec.NodeResult, len(states))
	for id, state := range states {
		res := &workflowspec.NodeResult{
			Status:         workflowspec.NodeStatus(state.Status),
			SkipReason:     workflowspec.SkipReason(state.SkipReason),
			ExitCode:       state.ExitCode,
			Output:         state.Output,
			LoopIterations: copyIntMap(state.LoopIterations),
		}
		if state.Error != "" {
			res.Error = errors.New(state.Error)
		}
		results[id] = res
	}
	return results
}

// copyIntMap deep-copies a string→int map (nil-safe, preserves nil).
func copyIntMap(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// compatSuspendedColumns picks the legacy single-suspension compatibility
// values for a suspended node set: the previous node when still present,
// otherwise the lexicographically first node of the set.
func compatSuspendedColumns(previousNodeID string, suspendedNodes map[string]SuspendedNodeInfo) (string, string) {
	if previousNodeID != "" {
		if info, ok := suspendedNodes[previousNodeID]; ok {
			return previousNodeID, info.MessageID
		}
	}
	keys := make([]string, 0, len(suspendedNodes))
	for id := range suspendedNodes {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		return id, suspendedNodes[id].MessageID
	}
	return "", ""
}

// persistStatus maps an engine run status to its persisted spelling.
func persistStatus(status WorkflowRunStatus) string {
	if status == RunStatusCanceled {
		return PersistStatusCancelled
	}
	return string(status)
}
