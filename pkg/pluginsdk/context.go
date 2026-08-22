package pluginsdk

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// NodeContext carries the pure runtime data for one node execution. External
// dependencies (llm.Client, agentspec.Loader, ...) are injected into the
// respective runners via their constructors, never here.
type NodeContext struct {
	SessionID string
	RunID     string
	RunDir    string
	TmpDir    string
	// Input is the initial user prompt that triggered the workflow run.
	Input string
	// Defn is the workflow definition this node belongs to.
	Defn *workflowspec.WorkflowDefinition
	Node *workflowspec.NodeSpec
	// Upstreams holds the settled results of upstream nodes at the time this
	// node was evaluated (only dependency results are guaranteed present).
	Upstreams map[string]*workflowspec.NodeResult
	// EventEmitter streams node lifecycle events to the run subscriber.
	EventEmitter func(event WorkflowEvent)
	// Values is a shared per-run key/value store for cross-node runtime state
	// (e.g. CLI session IDs inherited between agent nodes).
	Values *RunValues
	// Iteration records the 1-based execution count for this node (useful for dynamic loops).
	Iteration int
	// LoopIterations carries the run's loop iteration counters at launch time
	// (interpolated as ${loops.<id>.iteration} in prompts and commands).
	LoopIterations map[string]int
	// WorkflowRunDirs carries workflow/parent configured run directories.
	WorkflowRunDirs []string
	// WorkflowMountDirs carries workflow/parent configured mount directories.
	WorkflowMountDirs workflowspec.MountDirsConfig
	// Headless marks no-interaction execution; node runners use it to
	// suppress interactive behavior (e.g. human nodes).
	Headless bool
}

// Interpolate expands ${...} placeholders in text using run-scoped variables
// (session_id, run_dir, tmp_dir, input) and node result fields
// (nodes.<id>.status / exit_code / output / output_file).
func (nctx *NodeContext) Interpolate(text string) string {
	return workflowspec.Interpolate(text, nctx.resolveVar)
}

func (nctx *NodeContext) resolveVar(key string) (string, bool) {
	switch key {
	case "session_id":
		return nctx.SessionID, true
	case "run_dir":
		return nctx.RunDir, true
	case "tmp_dir":
		return nctx.TmpDir, true
	case "input", "prompt":
		return nctx.Input, true
	case "node.id":
		if nctx.Node != nil {
			return nctx.Node.ID, true
		}
		return "", false
	}
	if loopID, ok := strings.CutPrefix(key, "loops."); ok {
		if id, ok := strings.CutSuffix(loopID, ".iteration"); ok {
			if n, ok := nctx.LoopIterations[id]; ok {
				return strconv.Itoa(n), true
			}
			return "", false
		}
	}
	if value, err := workflowspec.ResolveNodeValue(key, nctx.Upstreams, nctx.Defn); err == nil {
		return value, true
	}
	return "", false
}

// RunValues is a concurrency-safe key/value store scoped to one workflow run.
type RunValues struct {
	m sync.Map
}

// Get returns the value stored under key.
func (v *RunValues) Get(key string) (any, bool) {
	if v == nil {
		return nil, false
	}
	return v.m.Load(key)
}

// Set stores a value under key.
func (v *RunValues) Set(key string, value any) {
	if v == nil {
		return
	}
	v.m.Store(key, value)
}

// WorkflowEventType enumerates engine lifecycle events.
type WorkflowEventType string

const (
	EventWorkflowStarted   WorkflowEventType = "workflow_started"
	EventNodeStarted       WorkflowEventType = "node_started"
	EventNodeStatusUpdate  WorkflowEventType = "node_status_update"
	EventNodeFinished      WorkflowEventType = "node_finished"
	EventNodeSkipped       WorkflowEventType = "node_skipped"
	EventWorkflowSuspended WorkflowEventType = "workflow_suspended"
	EventWorkflowResumed   WorkflowEventType = "workflow_resumed"
	EventWorkflowFinished  WorkflowEventType = "workflow_finished"
)

// WorkflowEvent is emitted by the engine as the run progresses.
type WorkflowEvent struct {
	Type       WorkflowEventType
	Workflow   string
	SessionID  string
	NodeID     string
	NodeType   workflowspec.NodeType
	AgentID    string
	Status     workflowspec.NodeStatus
	SkipReason workflowspec.SkipReason
	Message    string
	EntryType  string
	Metadata   map[string]any
	// MessageID carries the deterministic ask_user MessageID
	// (wf-<run_id>-<node_id>) on EventWorkflowSuspended events.
	MessageID string
	// Artifacts lists viewer-facing artifact paths produced or referenced by
	// the node (e.g. human prompt file references, output_file results).
	Artifacts []string
	// Output carries the node's final response text (agent / llm nodes) on
	// EventNodeFinished so hosts can render and persist it as a chat
	// message instead of only the terse lifecycle status line.
	Output string
	// AgentName names the workflow agent for chat routing.
	AgentName string
	Timestamp time.Time
}

// WorkflowRunStatus is the settled status of a whole workflow run.
type WorkflowRunStatus string

const (
	RunStatusWaitingHuman WorkflowRunStatus = "WAITING_HUMAN"
	RunStatusCompleted    WorkflowRunStatus = "COMPLETED"
	RunStatusFailed       WorkflowRunStatus = "FAILED"
	RunStatusCanceled     WorkflowRunStatus = "CANCELED"
)

// WorkflowRunResult is the settled outcome of a workflow run.
type WorkflowRunResult struct {
	Status WorkflowRunStatus
	Nodes  map[string]*workflowspec.NodeResult
	Error  error
}
