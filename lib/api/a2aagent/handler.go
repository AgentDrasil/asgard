package a2aagent

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/agents/run"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

type agentExecutor struct {
	agent *agents.Agent
	repo  *dbmodels.SessionRepository
}

// Execute handles the agent execution.
func (e *agentExecutor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(execCtx, nil), nil) {
				return
			}
		}

		chatID := execCtx.ContextID

		agentSessionID := optional.None[string]()
		if e.repo != nil {
			session, err := e.repo.GetSession(chatID)
			if err != nil {
				yield(nil, fmt.Errorf("failed to get session: %w", err))
				return
			}

			if session != nil {
				for _, dbAgent := range session.Agents {
					if dbAgent.Name == e.agent.Config.Name {
						if dbAgent.SessionID != "" {
							agentSessionID = optional.Some(dbAgent.SessionID)
						}
						break
					}
				}
			}
		}

		var promptBuilder strings.Builder
		if execCtx.Message != nil {
			for _, part := range execCtx.Message.Parts {
				if part != nil && part.Text() != "" {
					if promptBuilder.Len() > 0 {
						promptBuilder.WriteString("\n")
					}
					promptBuilder.WriteString(part.Text())
				}
			}
		}
		prompt := promptBuilder.String()

		out, err := run.Run(ctx, e.agent, prompt, agentSessionID)
		if err != nil {
			yield(nil, fmt.Errorf("failed to run agent: %w", err))
			return
		}

		respMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(string(out)))
		if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, respMsg), nil) {
			return
		}
	}
}

// Cancel handles canceling an execution.
func (e *agentExecutor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		// Emit TaskStatusUpdateEvent with TaskStateCanceled.
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

// NewAgentHandler creates the A2A HTTP REST handler and the AgentCard for the given agent.
func NewAgentHandler(agent *agents.Agent, repo *dbmodels.SessionRepository) (http.Handler, *a2a.AgentCard) {
	executor := &agentExecutor{agent: agent, repo: repo}
	handler := a2asrv.NewHandler(executor)
	restHandler := a2asrv.NewRESTHandler(handler)

	card := &a2a.AgentCard{
		Name:        agent.Config.Name,
		Description: agent.Config.Description,
		Version:     "1.0.0",
	}

	return restHandler, card
}
