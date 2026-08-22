package workflow

import (
	"context"
	"sync"

	"github.com/AgentDrasil/asgard/pkg/pluginsdk"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
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
	Supports(t workflowspec.NodeType) bool
	Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error)
}

// NodeRunnerRegistry is the IoC container mapping node types to runners.
type NodeRunnerRegistry struct {
	mu      sync.RWMutex
	runners map[workflowspec.NodeType]NodeRunner
}

// NewNodeRunnerRegistry creates an empty registry.
func NewNodeRunnerRegistry() *NodeRunnerRegistry {
	return &NodeRunnerRegistry{runners: make(map[workflowspec.NodeType]NodeRunner)}
}

// Register maps every node type supported by runner to that runner,
// replacing any previous registration.
func (r *NodeRunnerRegistry) Register(runner NodeRunner) {
	if runner == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range []workflowspec.NodeType{
		workflowspec.NodeTypeAgent,
		workflowspec.NodeTypeLLM,
		workflowspec.NodeTypeCommand,
		workflowspec.NodeTypeHuman,
		workflowspec.NodeTypeFunction,
		workflowspec.NodeTypeWorkflow,
	} {
		if runner.Supports(t) {
			r.runners[t] = runner
		}
	}
}

// Get returns the runner registered for a node type.
func (r *NodeRunnerRegistry) Get(t workflowspec.NodeType) (NodeRunner, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runner, ok := r.runners[t]
	return runner, ok
}
