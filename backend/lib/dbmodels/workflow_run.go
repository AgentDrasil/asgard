package dbmodels

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ErrOffloadedFileMissing is returned when an offloaded file (DAG spec, input, or node output) is missing.
var ErrOffloadedFileMissing = errors.New("offloaded workflow run file missing")

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
	// DAGSpec is the raw YAML snapshot the run was started from (in-memory, offloaded to file).
	DAGSpec string `gorm:"-" json:"dag_spec,omitempty"`
	// DAGSpecPath is the absolute path to the offloaded DAG spec YAML file.
	DAGSpecPath string `gorm:"column:dag_spec_path;size:512" json:"dag_spec_path,omitempty"`
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
	// RunDir preserves the original working directory context for resume.
	RunDir string `gorm:"column:run_dir"`
	// Input preserves the original run input prompt (in-memory, offloaded to file).
	Input string `gorm:"-" json:"input,omitempty"`
	// InputPath is the absolute path to the offloaded input file.
	InputPath string `gorm:"column:input_path;size:512" json:"input_path,omitempty"`

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

// workflowRunDir returns the absolute directory for a workflow run under sessionDir.
func workflowRunDir(sessionDir, runID string) string {
	return filepath.Join(sessionDir, "workflows", runID)
}

// atomicWriteFile writes data to targetPath via a unique temporary file with fsync and rename.
// If targetPath already exists and its content is identical to data, it skips writing to avoid write amplification.
func atomicWriteFile(targetPath string, data []byte) error {
	if existing, err := os.ReadFile(targetPath); err == nil && bytes.Equal(existing, data) {
		return nil
	}

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	base := filepath.Base(targetPath)
	tmp, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return fmt.Errorf("open tmp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	writeErr := func() error {
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("write data to %s: %w", tmpPath, err)
		}
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("sync %s: %w", tmpPath, err)
		}
		return tmp.Close()
	}()

	if writeErr != nil {
		_ = os.Remove(tmpPath)
		return writeErr
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s to %s: %w", tmpPath, targetPath, err)
	}

	// fsync parent directory to ensure directory entry persistence across crashes
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}

// CleanOrphanTmpFiles walks rootDir and removes any dangling temporary files created by atomicWriteFile.
func CleanOrphanTmpFiles(rootDir string) error {
	if _, err := os.Stat(rootDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".") && strings.HasSuffix(d.Name(), ".tmp") {
			_ = os.Remove(path)
		}
		return nil
	})
}

// WriteOffloadedFiles offloads DAGSpec, Input, and node outputs to the filesystem under sessionDir/workflows/runID/.
// It writes files atomically (.tmp + Sync + Rename) and returns the paths and pruned NodeStates (Output cleared).
func WriteOffloadedFiles(sessionDir, runID string, dagSpec, input string, states map[string]NodeState) (dagPath, inPath string, offloadedStates map[string]NodeState, err error) {
	safeRunID := filepath.Clean(runID)
	if safeRunID == "." || safeRunID == ".." || strings.Contains(safeRunID, "/") || strings.Contains(safeRunID, "\\") {
		return "", "", nil, fmt.Errorf("invalid run id %q for offloaded path", runID)
	}

	runDir := workflowRunDir(sessionDir, safeRunID)
	_ = CleanOrphanTmpFiles(runDir)
	nodesDir := filepath.Join(runDir, "nodes")
	if err := os.MkdirAll(nodesDir, 0755); err != nil {
		return "", "", nil, fmt.Errorf("create run nodes dir: %w", err)
	}

	// 1. Offload DAGSpec
	if dagSpec != "" {
		dagPath = filepath.Join(runDir, "dag_spec.yaml")
		if err := atomicWriteFile(dagPath, []byte(dagSpec)); err != nil {
			return "", "", nil, fmt.Errorf("write offloaded dag_spec: %w", err)
		}
	}

	// 2. Offload Input
	if input != "" {
		inPath = filepath.Join(runDir, "input.txt")
		if err := atomicWriteFile(inPath, []byte(input)); err != nil {
			return "", "", nil, fmt.Errorf("write offloaded input: %w", err)
		}
	}

	// 3. Offload node outputs
	offloadedStates = make(map[string]NodeState, len(states))
	for nodeID, state := range states {
		cloned := state
		if cloned.Output != "" {
			// Defensive path validation on nodeID (P2-F)
			safeNodeID := filepath.Clean(nodeID)
			if safeNodeID == "." || safeNodeID == ".." || strings.Contains(safeNodeID, "/") || strings.Contains(safeNodeID, "\\") {
				return "", "", nil, fmt.Errorf("invalid node id %q for offloaded path", nodeID)
			}

			outPath := filepath.Join(nodesDir, safeNodeID+".log")
			if err := atomicWriteFile(outPath, []byte(cloned.Output)); err != nil {
				return "", "", nil, fmt.Errorf("write offloaded node output %s: %w", safeNodeID, err)
			}
			cloned.OutputPath = outPath
			cloned.Output = ""
		}
		offloadedStates[nodeID] = cloned
	}

	return dagPath, inPath, offloadedStates, nil
}

// HydrateRun populates offloaded fields (DAGSpec, Input, and optionally node Outputs) from files.
// Returns ErrOffloadedFileMissing if an expected file is missing.
func HydrateRun(run *WorkflowRun, hydrateNodeOutput bool) error {
	if run == nil {
		return nil
	}

	// 1. Hydrate DAGSpec
	if run.DAGSpecPath != "" {
		data, err := os.ReadFile(run.DAGSpecPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: %s", ErrOffloadedFileMissing, run.DAGSpecPath)
			}
			return fmt.Errorf("read offloaded dag spec: %w", err)
		}
		run.DAGSpec = string(data)
	}

	// 2. Hydrate Input
	if run.InputPath != "" {
		data, err := os.ReadFile(run.InputPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: %s", ErrOffloadedFileMissing, run.InputPath)
			}
			return fmt.Errorf("read offloaded input: %w", err)
		}
		run.Input = string(data)
	}

	// 3. Hydrate NodeOutputs if requested
	if hydrateNodeOutput && run.NodeStates != "" {
		states, err := DecodeNodeStates(run.NodeStates)
		if err != nil {
			return fmt.Errorf("decode node states during hydrate: %w", err)
		}
		updated := false
		for nodeID, state := range states {
			if state.OutputPath != "" {
				data, err := os.ReadFile(state.OutputPath)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						return fmt.Errorf("%w: node %s: %s", ErrOffloadedFileMissing, nodeID, state.OutputPath)
					}
					return fmt.Errorf("read offloaded node output %s: %w", nodeID, err)
				}
				state.Output = string(data)
				states[nodeID] = state
				updated = true
			}
		}
		if updated {
			encoded, err := EncodeNodeStates(states)
			if err != nil {
				return fmt.Errorf("encode hydrated node states: %w", err)
			}
			run.NodeStates = encoded
		}
	}

	return nil
}

type WorkflowRunRepository struct {
	db             *gorm.DB
	sessionDirFunc func(sessionID string) string
}

func NewWorkflowRunRepository(db *gorm.DB) *WorkflowRunRepository {
	return &WorkflowRunRepository{
		db:             db,
		sessionDirFunc: defaultSessionDir,
	}
}

// SetSessionDirFunc overrides the directory resolver function (used for testing isolation).
func (r *WorkflowRunRepository) SetSessionDirFunc(fn func(sessionID string) string) {
	r.sessionDirFunc = fn
}

func (r *WorkflowRunRepository) sessionDir(sessionID string) string {
	if r.sessionDirFunc != nil {
		return r.sessionDirFunc(sessionID)
	}
	return defaultSessionDir(sessionID)
}

// SaveRun offloads large content to files and saves the metadata and paths to the DB.
func (r *WorkflowRunRepository) SaveRun(run *WorkflowRun) error {
	if run == nil {
		return nil
	}

	sessionDir := r.sessionDir(run.SessionID)

	// Decode existing states if any
	states, err := DecodeNodeStates(run.NodeStates)
	if err != nil {
		return fmt.Errorf("decode node states for offload: %w", err)
	}

	// Offload to files
	dagPath, inPath, offloadedStates, err := WriteOffloadedFiles(sessionDir, run.RunID, run.DAGSpec, run.Input, states)
	if err != nil {
		return fmt.Errorf("offload workflow run files: %w", err)
	}

	if dagPath != "" {
		run.DAGSpecPath = dagPath
	}
	if inPath != "" {
		run.InputPath = inPath
	}

	encodedStates, err := EncodeNodeStates(offloadedStates)
	if err != nil {
		return fmt.Errorf("encode offloaded node states: %w", err)
	}
	run.NodeStates = encodedStates

	return r.db.Save(run).Error
}

// UpdateRunStatus updates the status column of a workflow run by run_id.
func (r *WorkflowRunRepository) UpdateRunStatus(runID string, status string) error {
	return r.db.Model(&WorkflowRun{}).
		Where("run_id = ?", runID).
		Update("status", status).Error
}

// GetRunRow retrieves a workflow run by run ID without hydrating offloaded files from disk.
// Offloaded fields (DAGSpec, Input, and NodeState.Output) stay empty.
// Returns (nil, nil) when not found.
func (r *WorkflowRunRepository) GetRunRow(runID string) (*WorkflowRun, error) {
	var run WorkflowRun
	err := r.db.First(&run, "run_id = ?", runID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

// GetRun retrieves a workflow run by run ID and hydrates offloaded fields.
// Returns (nil, nil) when not found.
func (r *WorkflowRunRepository) GetRun(runID string) (*WorkflowRun, error) {
	run, err := r.GetRunRow(runID)
	if err != nil || run == nil {
		return nil, err
	}
	if err := HydrateRun(run, true); err != nil {
		return nil, err
	}
	return run, nil
}

// FindWaitingHumanBySession returns the WAITING_HUMAN run for a session (hydrated), or
// (nil, nil) when none is suspended.
func (r *WorkflowRunRepository) FindWaitingHumanBySession(sessionID string) (*WorkflowRun, error) {
	var run WorkflowRun
	err := r.db.
		Where("session_id = ? AND status = ?", sessionID, WorkflowStatusWaitingHuman).
		Order("updated_at desc").
		First(&run).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if err := HydrateRun(&run, true); err != nil {
		return nil, err
	}
	return &run, nil
}

// FindWaitingHumansBySession returns all WAITING_HUMAN runs of a session (hydrated),
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
	for _, run := range runs {
		if err := HydrateRun(run, true); err != nil {
			return nil, err
		}
	}
	return runs, nil
}

// HasRunningRunBySession returns true if there is any workflow run with status RUNNING for the session.
func (r *WorkflowRunRepository) HasRunningRunBySession(sessionID string) (bool, error) {
	var count int64
	err := r.db.Model(&WorkflowRun{}).
		Where("session_id = ? AND status = ?", sessionID, WorkflowStatusRunning).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// escapeLike escapes SQL LIKE wildcards so the caller-provided value is
// matched literally under the '\' escape character.
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// FindWaitingHumanByMessageID returns the WAITING_HUMAN run that owns the
// given ask_user MessageID (hydrated), or (nil, nil) when no run matches. It matches the
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
		if err := HydrateRun(&run, true); err != nil {
			return nil, err
		}
		return &run, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	pattern := `%"message_id":"` + escapeLike(messageID) + `"%`
	err = r.db.
		Where("status = ? AND suspended_nodes LIKE ? ESCAPE '\\'", WorkflowStatusWaitingHuman, pattern).
		First(&run).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if err := HydrateRun(&run, true); err != nil {
		return nil, err
	}
	return &run, nil
}

// RefreshSuspension atomically overwrites the suspension-related columns of a
// run: settled node states, loop iteration / execution counters and the
// (possibly pruned) suspended node set. Node outputs are offloaded to session filesystem.
// The most recently suspended node is kept in the legacy SuspendedNodeID / SuspendedMessageID compatibility
// columns when it is still part of the new set; otherwise the set's first
// node (sorted by node ID) is promoted.
func (r *WorkflowRunRepository) RefreshSuspension(runID string, states map[string]NodeState, loopIterations, executionCounts map[string]int, suspendedNodes map[string]SuspendedNodeInfo) error {
	run, err := r.GetRunRow(runID)
	if err != nil {
		return err
	}
	if run == nil {
		return gorm.ErrRecordNotFound
	}

	sessionDir := r.sessionDir(run.SessionID)
	_, _, offloadedStates, err := WriteOffloadedFiles(sessionDir, runID, "", "", states)
	if err != nil {
		return fmt.Errorf("offload states during refresh suspension: %w", err)
	}

	nodeStates, err := EncodeNodeStates(offloadedStates)
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
