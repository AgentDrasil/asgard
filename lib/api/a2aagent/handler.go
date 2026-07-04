package a2aagent

import (
	"context"
	"iter"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/AgentDrasil/asgard/lib/agents"
)

type todoExecutor struct {
	agent *agents.Agent
}

// Execute handles the agent execution.
func (e *todoExecutor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(execCtx, nil), nil) {
				return
			}
		}

		// TODO: Implement agent execution logic here.

		if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil) {
			return
		}
	}
}

// Cancel handles canceling an execution.
func (e *todoExecutor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		// Emit TaskStatusUpdateEvent with TaskStateCanceled.
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

// NewAgentHandler creates the A2A HTTP REST handler and the AgentCard for the given agent.
func NewAgentHandler(agent *agents.Agent) (http.Handler, *a2a.AgentCard) {
	executor := &todoExecutor{agent: agent}
	handler := a2asrv.NewHandler(executor)
	restHandler := a2asrv.NewRESTHandler(handler)

	card := &a2a.AgentCard{
		Name:        agent.Config.Name,
		Description: agent.Config.Description,
		Version:     "1.0.0",
	}

	return restHandler, card
}
