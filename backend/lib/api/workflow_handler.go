package api

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/llm"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
)

// newWorkflowEngine builds the shared workflow engine with all node runners
// registered via the IoC registry. funcRegistry backs the `function` node
// runner (nil falls back to the process-wide default registry); extraRunners
// replace the default runner for the node types they support.
func newWorkflowEngine(conf *config.Config, statusListener workflow.AgentStatusListener, funcRegistry *workflow.FunctionRegistry, extraRunners ...workflow.NodeRunner) (*workflow.Engine, error) {
	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(true))
	registry.Register(workflow.NewFunctionRunner(funcRegistry))
	if conf != nil {
		registry.Register(workflow.NewAgentRunnerWithListener(agents.NewLoader(conf.AgentDir), conf, statusListener))
		if conf.GeminiAPIKey != "" {
			client, err := llm.NewClient(context.Background(), conf.GeminiAPIKey)
			if err != nil {
				return nil, fmt.Errorf("creating llm client for workflow engine: %w", err)
			}
			registry.Register(workflow.NewLLMRunner(client))
		}
	}
	for _, runner := range extraRunners {
		registry.Register(runner)
	}
	return workflow.NewEngine(registry), nil
}

// runWorkflow executes a workflow agent synchronously, returning its settled status and summary output.
func (s *Server) runWorkflow(ctx context.Context, agent *agents.Agent, chatID string, req TriggerMessageRequest) (status string, output string, err error) {
	s.persistIncomingWorkflowMessage(agent, chatID, req)
	s.maybeGenerateWorkflowTitle(ctx, agent, chatID, req.Prompt)

	agentID := agent.Config.ID
	if s.repo != nil && chatID != "" && agentID != "" {
		if err := s.repo.UpdateAgentStatus(chatID, agentID, dbmodels.AgentStatusRunning); err != nil {
			log.Warn().Err(err).Str("chat_id", chatID).Str("agent", agentID).Msg("failed to update workflow agent status to running")
		} else {
			s.PublishSessionEvent(chatID, SessionEvent{
				Type:    "status",
				Payload: map[string]any{"agent": agentID, "isRunning": true},
			})
		}
		defer func() {
			if err := s.repo.UpdateAgentStatus(chatID, agentID, dbmodels.AgentStatusCompleted); err != nil {
				log.Warn().Err(err).Str("chat_id", chatID).Str("agent", agentID).Msg("failed to mark workflow agent status completed")
			} else {
				s.PublishSessionEvent(chatID, SessionEvent{
					Type:    "status",
					Payload: map[string]any{"agent": agentID, "isRunning": false},
				})
				s.PublishSessionEvent(chatID, SessionEvent{
					Type:    "done",
					Payload: map[string]any{"agent": agentID},
				})
			}
		}()
	}

	defn, err := workflow.LoadDefinition(agent.WorkflowPath)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.Config.ID).Msg("failed to load workflow definition")
		return "failed", "", fmt.Errorf("failed to load workflow definition: %w", err)
	}

	engine := s.workflowEngine
	if engine == nil {
		var err error
		engine, err = newWorkflowEngine(s.conf, s, s.funcRegistry, s.customRunners...)
		if err != nil {
			log.Error().Err(err).Str("agent", agent.Config.ID).Msg("failed to create workflow engine")
			return "failed", "", fmt.Errorf("failed to create workflow engine: %w", err)
		}
		s.mu.RLock()
		if len(s.agents) > 0 {
			agentsSnapshot := make([]*agents.Agent, len(s.agents))
			copy(agentsSnapshot, s.agents)
			engine.SetAgents(agentsSnapshot)
		}
		s.mu.RUnlock()
	}

	executor := workflow.NewWorkflowExecutor(engine, defn)
	executor.AgentName = agent.Config.Name
	executor.WorkflowRunDirs = agent.Config.RunDirs
	executor.WorkflowMountDirs = workflow.MountDirsConfig{
		ReadOnly:  agent.Config.MountDirs.ReadOnly,
		ReadWrite: agent.Config.MountDirs.ReadWrite,
	}
	suspendCh := make(chan struct{}, 1)
	executor.OnEvent = func(sessionID string, ev workflow.WorkflowEvent) {
		s.handleWorkflowEvent(sessionID, ev)
		if ev.Type == workflow.EventWorkflowSuspended {
			select {
			case suspendCh <- struct{}{}:
			default:
			}
		}
	}

	type runResult struct {
		result *workflow.WorkflowRunResult
		err    error
	}
	resultCh := make(chan runResult, 1)
	go func() {
		res, err := executor.Execute(ctx, workflow.WorkflowRunParams{
			SessionID: chatID,
			Prompt:    req.Prompt,
			RunDir:    req.RunDir,
			Metadata:  req.Metadata,
		})
		resultCh <- runResult{result: res, err: err}
	}()

	select {
	case <-suspendCh:
		return "waiting_human", "", nil
	case res := <-resultCh:
		if res.err != nil {
			return "failed", "", res.err
		}
		summary := workflow.SummarizeRun(res.result)
		switch res.result.Status {
		case workflow.RunStatusWaitingHuman:
			return "waiting_human", "", nil
		case workflow.RunStatusCompleted:
			return "completed", summary, nil
		case workflow.RunStatusCanceled:
			return "cancelled", summary, nil
		default:
			return "failed", summary, fmt.Errorf("workflow execution failed with status %s", res.result.Status)
		}
	}
}

// persistIncomingWorkflowMessage appends the user's prompt to the chat session.
func (s *Server) persistIncomingWorkflowMessage(agent *agents.Agent, chatID string, req TriggerMessageRequest) {
	if s == nil || s.repo == nil || chatID == "" || !IsValidChatID(chatID) || req.Prompt == "" {
		return
	}
	userMsgID := ""
	isInternal := false
	callerName := ""
	if req.Metadata != nil {
		if mid, ok := req.Metadata["message_id"].(string); ok && mid != "" {
			userMsgID = mid
		}
		if v, ok := req.Metadata["internal"].(bool); ok && v {
			isInternal = true
		}
		if cn, ok := req.Metadata["caller_agent_name"].(string); ok {
			callerName = cn
		}
	}
	if userMsgID == "" {
		userMsgID = fmt.Sprintf("msg-%s", uuid.Must(uuid.NewV7()).String())
	}
	role := "user"
	activityType := ""
	agentName := ""
	if isInternal {
		role = "activity"
		activityType = "CALL_PEER"
		if callerName != "" {
			agentName = callerName
		} else {
			agentName = agent.Config.Name
		}
	}
	msg := dbmodels.ChatMessage{
		ID:           userMsgID,
		Role:         role,
		ActivityType: activityType,
		Content:      req.Prompt,
		AgentName:    agentName,
		Timestamp:    time.Now().UnixMilli(),
	}
	if err := s.repo.AppendMessage(chatID, msg); err != nil {
		log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append workflow user message to repo")
	} else {
		s.PublishSessionEvent(chatID, SessionEvent{
			Type:    "message",
			Message: &msg,
		})
	}
}

// maybeGenerateWorkflowTitle spawns title-generation if session has no title yet.
func (s *Server) maybeGenerateWorkflowTitle(ctx context.Context, agent *agents.Agent, chatID string, prompt string) {
	if s == nil || s.repo == nil || chatID == "" || !IsValidChatID(chatID) || prompt == "" {
		return
	}
	session, err := s.repo.GetSession(chatID)
	if err != nil {
		log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to get session for workflow title generation")
		return
	}
	if session != nil && session.Title != "" {
		return
	}
	goGenerateSessionTitle(ctx, s, nil, s.repo, chatID, prompt, agent.Config.ID, agent.Config.Description)
}
