package dbmodels

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// Persisted workflow run statuses (WorkflowRun.Status).
const (
	WorkflowStatusRunning      = "RUNNING"
	WorkflowStatusWaitingHuman = "WAITING_HUMAN"
	WorkflowStatusCompleted    = "COMPLETED"
	WorkflowStatusFailed       = "FAILED"
	WorkflowStatusCancelled    = "CANCELLED"
)

// WorkflowRun is the persistent snapshot of one workflow execution. It backs
// pause/resume (type: human nodes) and crash recovery: the DAG spec plus the
// settled node states are enough to re-drive the scheduler after a server
// restart.
type WorkflowRun struct {
	RunID string `gorm:"primaryKey;column:run_id;size:64"`
	// SessionID is the chat/context ID the run belongs to.
	SessionID string `gorm:"column:session_id;size:64;index"`
	// Status is one of RUNNING, WAITING_HUMAN, COMPLETED, FAILED, CANCELLED.
	Status string `gorm:"column:status;size:32"`
	// DAGSpec is the raw YAML snapshot the run was started from.
	DAGSpec string `gorm:"column:dag_spec;type:text"`
	// NodeStates is a JSON map[node_id]NodeState (status, exit_code, output_path).
	NodeStates string `gorm:"column:node_states;type:text"`
	// LoopIterations is a JSON map[loop_id]iteration_count captured at
	// suspension time; it re-seeds loop circuit breakers on resume.
	LoopIterations string `gorm:"column:loop_iterations;type:text"`
	// ExecutionCounts is a JSON map[node_id]execution_count captured at
	// suspension time; it keeps quota caps and human MessageIDs stable
	// across restarts.
	ExecutionCounts string `gorm:"column:execution_counts;type:text"`
	// SuspendedNodeID is the human node currently suspending the run
	// (single active human node per run in Phase 3).
	SuspendedNodeID string `gorm:"column:suspended_node_id;size:64"`
	// SuspendedMessageID is the deterministic ask_user MessageID
	// (wf-<run_id>-<node_id>) used for precise reply routing.
	SuspendedMessageID string `gorm:"column:suspended_message_id;size:64"`
	// RunDir / Input preserve the original run context for resume.
	RunDir string `gorm:"column:run_dir"`
	Input  string `gorm:"column:input;type:text"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// NodeState is the serializable settled state of a single workflow node.
type NodeState struct {
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Output     string `json:"output,omitempty"`
	OutputPath string `json:"output_path,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
	Error      string `json:"error,omitempty"`
}

// EncodeNodeStates serializes a node state map into the NodeStates column.
func EncodeNodeStates(states map[string]NodeState) (string, error) {
	if states == nil {
		return "{}", nil
	}
	data, err := json.Marshal(states)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DecodeNodeStates parses the NodeStates column back into a map.
func DecodeNodeStates(raw string) (map[string]NodeState, error) {
	if raw == "" {
		return map[string]NodeState{}, nil
	}
	var states map[string]NodeState
	if err := json.Unmarshal([]byte(raw), &states); err != nil {
		return nil, err
	}
	return states, nil
}

// EncodeIntMap serializes a string→int map into a JSON text column.
func EncodeIntMap(m map[string]int) (string, error) {
	if m == nil {
		return "{}", nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DecodeIntMap parses a JSON text column back into a string→int map.
func DecodeIntMap(raw string) (map[string]int, error) {
	if raw == "" {
		return map[string]int{}, nil
	}
	var m map[string]int
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return m, nil
}

type WorkflowRunRepository struct {
	db *gorm.DB
}

func NewWorkflowRunRepository(db *gorm.DB) *WorkflowRunRepository {
	return &WorkflowRunRepository{db: db}
}

// SaveRun upserts the workflow run snapshot by run_id.
func (r *WorkflowRunRepository) SaveRun(run *WorkflowRun) error {
	return r.db.Save(run).Error
}

// GetRun retrieves a workflow run by run ID. Returns (nil, nil) when not found.
func (r *WorkflowRunRepository) GetRun(runID string) (*WorkflowRun, error) {
	var run WorkflowRun
	err := r.db.First(&run, "run_id = ?", runID).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

// FindWaitingHumanBySession returns the WAITING_HUMAN run for a session, or
// (nil, nil) when none is suspended.
func (r *WorkflowRunRepository) FindWaitingHumanBySession(sessionID string) (*WorkflowRun, error) {
	var run WorkflowRun
	err := r.db.
		Where("session_id = ? AND status = ?", sessionID, WorkflowStatusWaitingHuman).
		Order("updated_at desc").
		First(&run).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}
