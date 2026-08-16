package workflow

import (
	"context"
	"sync"
	"time"
)

// NodeType enumerates the kinds of workflow nodes supported by the engine.
type NodeType string

const (
	NodeTypeAgent   NodeType = "agent"
	NodeTypeLLM     NodeType = "llm"
	NodeTypeCommand NodeType = "command"
	NodeTypeHuman   NodeType = "human"
)

// NodeStatus is the lifecycle status of a single workflow node.
type NodeStatus string

const (
	StatusPending   NodeStatus = "PENDING"
	StatusRunning   NodeStatus = "RUNNING"
	StatusSucceeded NodeStatus = "SUCCEEDED"
	StatusSkipped   NodeStatus = "SKIPPED"
	StatusFailed    NodeStatus = "FAILED"
)

// SkipReason explains why a node was skipped.
type SkipReason string

const (
	// SkipReasonConditionFalse means the node (or an upstream edge) had a
	// `when` condition that evaluated to false. This is an intentional,
	// successful branch skip.
	SkipReasonConditionFalse SkipReason = "CONDITION_FALSE"
	// SkipReasonCascadedFailure means the node was forcibly skipped because
	// an upstream node failed and the failure was not absorbed by a `when`
	// edge. This propagates global workflow failure.
	SkipReasonCascadedFailure SkipReason = "CASCADED_FAILURE"
)

// NodeResult is the outcome of executing (or skipping) a single node.
type NodeResult struct {
	Status     NodeStatus
	SkipReason SkipReason
	ExitCode   int
	Output     string
	Artifacts  map[string]string
	Error      error
}

// NodeContext carries the pure runtime data for one node execution. External
// dependencies (llm.Client, agents.Loader, ...) are injected into the
// respective runners via their constructors, never here.
type NodeContext struct {
	SessionID string
	RunDir    string
	TmpDir    string
	// Input is the initial user prompt that triggered the workflow run.
	Input string
	// Defn is the workflow definition this node belongs to.
	Defn *WorkflowDefinition
	Node *NodeSpec
	// Upstreams holds the settled results of upstream nodes at the time this
	// node was evaluated (only dependency results are guaranteed present).
	Upstreams map[string]*NodeResult
	// EventEmitter streams node lifecycle events to the run subscriber.
	EventEmitter func(event WorkflowEvent)
	// Values is a shared per-run key/value store for cross-node runtime state
	// (e.g. CLI session IDs inherited between agent nodes).
	Values *RunValues
	// WorkflowRunDirs carries workflow/parent configured run directories.
	WorkflowRunDirs []string
	// WorkflowMountDirs carries workflow/parent configured mount directories.
	WorkflowMountDirs MountDirsConfig
}

// Interpolate expands ${...} placeholders in text using run-scoped variables
// (session_id, run_dir, tmp_dir, input) and node result fields
// (nodes.<id>.status / exit_code / output / output_file).
func (nctx *NodeContext) Interpolate(text string) string {
	return Interpolate(text, nctx.resolveVar)
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
		return nctx.Node.ID, true
	}
	if value, err := resolveNodeValue(key, nctx.Upstreams, nctx.Defn); err == nil {
		return value, true
	}
	return "", false
}

// NodeRunner executes one kind of node.
type NodeRunner interface {
	Supports(t NodeType) bool
	Run(ctx context.Context, nctx *NodeContext) (*NodeResult, error)
}

// NodeRunnerRegistry is the IoC container mapping node types to runners.
type NodeRunnerRegistry struct {
	mu      sync.RWMutex
	runners map[NodeType]NodeRunner
}

// NewNodeRunnerRegistry creates an empty registry.
func NewNodeRunnerRegistry() *NodeRunnerRegistry {
	return &NodeRunnerRegistry{runners: make(map[NodeType]NodeRunner)}
}

// Register maps every node type supported by runner to that runner,
// replacing any previous registration.
func (r *NodeRunnerRegistry) Register(runner NodeRunner) {
	if runner == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range []NodeType{NodeTypeAgent, NodeTypeLLM, NodeTypeCommand, NodeTypeHuman} {
		if runner.Supports(t) {
			r.runners[t] = runner
		}
	}
}

// Get returns the runner registered for a node type.
func (r *NodeRunnerRegistry) Get(t NodeType) (NodeRunner, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runner, ok := r.runners[t]
	return runner, ok
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
	NodeType   NodeType
	AgentID    string
	Status     NodeStatus
	SkipReason SkipReason
	Message    string
	// MessageID carries the deterministic ask_user MessageID
	// (wf-<run_id>-<node_id>) on EventWorkflowSuspended events.
	MessageID string
	// Artifacts lists viewer-facing artifact paths produced or referenced by
	// the node (e.g. human prompt file references, output_file results).
	Artifacts []string
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
	Nodes  map[string]*NodeResult
	Error  error
}
