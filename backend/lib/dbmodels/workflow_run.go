package dbmodels

import (
	"encoding/json"
	"sort"
	"strings"
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
	SessionID string `gorm:"column:session_id;size:64;index:idx_session_status,priority:1"`
	// Status is one of RUNNING, WAITING_HUMAN, COMPLETED, FAILED, CANCELLED.
	Status string `gorm:"column:status;size:32;index:idx_session_status,priority:2"`
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
	// SuspendedNodeID is the most recently suspended human node (kept in
	// sync with SuspendedNodes for backward compatibility with old
	// snapshots that model a single active human node per run).
	SuspendedNodeID string `gorm:"column:suspended_node_id;size:64"`
	// SuspendedMessageID is the deterministic ask_user MessageID
	// (wf-<run_id>-<node_id>) of the most recently suspended node; it is
	// indexed for precise reply routing.
	SuspendedMessageID string `gorm:"column:suspended_message_id;size:64;index"`
	// SuspendedNodes is a JSON map[node_id]SuspendedNodeInfo describing all
	// concurrently suspended human nodes of the run.
	SuspendedNodes string `gorm:"column:suspended_nodes;type:text"`
	// ParentRunID records the parent run of a sub-workflow (inline) run.
	ParentRunID string `gorm:"column:parent_run_id;size:64;index"`
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

// SuspendedNodeInfo describes one concurrently suspended human node. The JSON
// tags must stay byte-for-byte identical to workflow.SuspendedNodeInfo.
type SuspendedNodeInfo struct {
	MessageID string `json:"message_id"`
	Iteration int    `json:"iteration"`
}

// EncodeSuspendedNodes serializes a suspended node map into the
// SuspendedNodes column.
func EncodeSuspendedNodes(nodes map[string]SuspendedNodeInfo) (string, error) {
	if nodes == nil {
		return "{}", nil
	}
	data, err := json.Marshal(nodes)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DecodeSuspendedNodes parses the SuspendedNodes column back into a map.
func DecodeSuspendedNodes(raw string) (map[string]SuspendedNodeInfo, error) {
	if raw == "" {
		return map[string]SuspendedNodeInfo{}, nil
	}
	var nodes map[string]SuspendedNodeInfo
	if err := json.Unmarshal([]byte(raw), &nodes); err != nil {
		return nil, err
	}
	return nodes, nil
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

// FindWaitingHumansBySession returns all WAITING_HUMAN runs of a session,
// most recently updated first. It supports multiple concurrent suspensions
// within one session.
func (r *WorkflowRunRepository) FindWaitingHumansBySession(sessionID string) ([]*WorkflowRun, error) {
	var runs []*WorkflowRun
	err := r.db.
		Where("session_id = ? AND status = ?", sessionID, WorkflowStatusWaitingHuman).
		Order("updated_at desc").
		Find(&runs).Error
	if err != nil {
		return nil, err
	}
	return runs, nil
}

// escapeLike escapes SQL LIKE wildcards so the caller-provided value is
// matched literally under the '\' escape character.
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// FindWaitingHumanByMessageID returns the WAITING_HUMAN run that owns the
// given ask_user MessageID, or (nil, nil) when no run matches. It matches the
// dedicated suspended_message_id column first, then falls back to a
// quote-anchored, wildcard-escaped search inside the SuspendedNodes JSON so
// prefix collisions (e.g. wf-run-review vs wf-run-review-2) cannot produce
// false positives.
func (r *WorkflowRunRepository) FindWaitingHumanByMessageID(messageID string) (*WorkflowRun, error) {
	var run WorkflowRun
	err := r.db.
		Where("suspended_message_id = ? AND status = ?", messageID, WorkflowStatusWaitingHuman).
		First(&run).Error
	if err == nil {
		return &run, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}
	pattern := `%"message_id":"` + escapeLike(messageID) + `"%`
	err = r.db.
		Where("status = ? AND suspended_nodes LIKE ? ESCAPE '\\'", WorkflowStatusWaitingHuman, pattern).
		First(&run).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

// RefreshSuspension atomically overwrites the suspension-related columns of a
// run: settled node states, loop iteration / execution counters and the
// (possibly pruned) suspended node set. The most recently suspended node is
// kept in the legacy SuspendedNodeID / SuspendedMessageID compatibility
// columns when it is still part of the new set; otherwise the set's first
// node (sorted by node ID) is promoted.
func (r *WorkflowRunRepository) RefreshSuspension(runID string, states map[string]NodeState, loopIterations, executionCounts map[string]int, suspendedNodes map[string]SuspendedNodeInfo) error {
	run, err := r.GetRun(runID)
	if err != nil {
		return err
	}
	if run == nil {
		return gorm.ErrRecordNotFound
	}
	nodeStates, err := EncodeNodeStates(states)
	if err != nil {
		return err
	}
	loopIters, err := EncodeIntMap(loopIterations)
	if err != nil {
		return err
	}
	execCounts, err := EncodeIntMap(executionCounts)
	if err != nil {
		return err
	}
	suspended, err := EncodeSuspendedNodes(suspendedNodes)
	if err != nil {
		return err
	}
	suspendedNodeID, suspendedMessageID := compatSuspendedColumns(run.SuspendedNodeID, suspendedNodes)
	return r.db.Model(&WorkflowRun{}).
		Where("run_id = ?", runID).
		Updates(map[string]any{
			"node_states":          nodeStates,
			"loop_iterations":      loopIters,
			"execution_counts":     execCounts,
			"suspended_nodes":      suspended,
			"suspended_node_id":    suspendedNodeID,
			"suspended_message_id": suspendedMessageID,
		}).Error
}

// ResetAllRunningWorkflows sets all workflow runs with status WorkflowStatusRunning
// to WorkflowStatusFailed across all sessions.
// This is called at server startup to clear stale running states from crashes or unexpected restarts.
func (r *WorkflowRunRepository) ResetAllRunningWorkflows() error {
	return r.db.Model(&WorkflowRun{}).
		Where("status = ?", WorkflowStatusRunning).
		Update("status", WorkflowStatusFailed).Error
}

// compatSuspendedColumns picks the legacy single-suspension compatibility
// values for a suspended node set: the previous node when still present,
// otherwise the lexicographically first node of the set.
func compatSuspendedColumns(previousNodeID string, suspendedNodes map[string]SuspendedNodeInfo) (string, string) {
	if info, ok := suspendedNodes[previousNodeID]; ok && previousNodeID != "" {
		return previousNodeID, info.MessageID
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
