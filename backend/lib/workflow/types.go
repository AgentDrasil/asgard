package workflow

import (
	"context"
	"sync"

	"github.com/AgentDrasil/asgard/pkg/pluginsdk"
)

// NodeContext is re-exported from pluginsdk.NodeContext.
type NodeContext = pluginsdk.NodeContext

// RunValues is re-exported from pluginsdk.RunValues.
type RunValues = pluginsdk.RunValues

// WorkflowEventType is re-exported from pluginsdk.WorkflowEventType.
type WorkflowEventType = pluginsdk.WorkflowEventType

const (
	EventWorkflowStarted   WorkflowEventType = pluginsdk.EventWorkflowStarted
	EventNodeStarted       WorkflowEventType = pluginsdk.EventNodeStarted
	EventNodeStatusUpdate  WorkflowEventType = pluginsdk.EventNodeStatusUpdate
	EventNodeFinished      WorkflowEventType = pluginsdk.EventNodeFinished
	EventNodeSkipped       WorkflowEventType = pluginsdk.EventNodeSkipped
	EventWorkflowSuspended WorkflowEventType = pluginsdk.EventWorkflowSuspended
	EventWorkflowResumed   WorkflowEventType = pluginsdk.EventWorkflowResumed
	EventWorkflowFinished  WorkflowEventType = pluginsdk.EventWorkflowFinished
)

// WorkflowEvent is re-exported from pluginsdk.WorkflowEvent.
type WorkflowEvent = pluginsdk.WorkflowEvent

// WorkflowRunStatus is re-exported from pluginsdk.WorkflowRunStatus.
type WorkflowRunStatus = pluginsdk.WorkflowRunStatus

const (
	RunStatusWaitingHuman WorkflowRunStatus = pluginsdk.RunStatusWaitingHuman
	RunStatusCompleted    WorkflowRunStatus = pluginsdk.RunStatusCompleted
	RunStatusFailed       WorkflowRunStatus = pluginsdk.RunStatusFailed
	RunStatusCanceled     WorkflowRunStatus = pluginsdk.RunStatusCanceled
)

// WorkflowRunResult is re-exported from pluginsdk.WorkflowRunResult.
type WorkflowRunResult = pluginsdk.WorkflowRunResult

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
	for _, t := range []NodeType{NodeTypeAgent, NodeTypeLLM, NodeTypeCommand, NodeTypeHuman, NodeTypeFunction, NodeTypeWorkflow} {
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
