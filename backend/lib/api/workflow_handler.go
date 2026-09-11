package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"uuid"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/llm"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// newWorkflowEngine builds the shared workflow engine with all node runners
// registered via the IoC registry. funcRegistry backs the `function` node
// runner (nil falls back to the process-wide default registry); resolveDefn
// resolves sub-workflow definitions by name for the `workflow` node runner;
// extraRunners replace the default runner for the node types they support.
func newWorkflowEngine(conf *config.Config, statusListener workflow.AgentStatusListener, funcRegistry *workflow.FunctionRegistry, resolveDefn workflow.ResolveDefnFunc, extraRunners ...workflow.NodeRunner) (*workflow.Engine, error) {
	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunnerWithConfig(true, conf))
	registry.Register(workflow.NewFunctionRunner(funcRegistry))
	if conf != nil {
		registry.Register(workflow.NewAgentRunnerWithListener(agentspec.NewLoader(conf.AgentDir), conf, statusListener))
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
	subRunner := workflow.NewSubWorkflowRunner(resolveDefn)
	registry.Register(subRunner)
	engine := workflow.NewEngine(registry)
	subRunner.SetEngine(engine)
	return engine, nil
}

// resolveWorkflowDefinition resolves a sub-workflow definition by name against
// the server's registered agentspec. Agents are matched first by config ID or
// display name; if none matches, all workflow agents' definitions are loaded
// and compared by definition name.
func (s *Server) resolveWorkflowDefinition(name string) (*workflowspec.WorkflowDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var fallbacks []*agentspec.Agent
	for _, agent := range s.agents {
		if agent.Config.Type != "workflow" || agent.WorkflowPath == "" {
			continue
		}
		if agent.Config.ID == name || agent.Config.Name == name {
			return workflowspec.LoadDefinition(agent.WorkflowPath)
		}
		fallbacks = append(fallbacks, agent)
	}
	for _, agent := range fallbacks {
		defn, err := workflowspec.LoadDefinition(agent.WorkflowPath)
		if err != nil {
			log.Warn().Err(err).Str("agent", agent.Config.ID).Msg("failed to load candidate sub-workflow definition")
			continue
		}
		if defn.Name == name {
			return defn, nil
		}
	}
	return nil, fmt.Errorf("sub-workflow %q not found among registered workflow agents", name)
}

// runWorkflow executes a workflow agent synchronously, returning its settled status and summary output.
func (s *Server) runWorkflow(ctx context.Context, agent *agentspec.Agent, chatID string, req TriggerMessageRequest) (status string, output string, err error) {
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
	}

	defn, err := workflowspec.LoadDefinition(agent.WorkflowPath)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.Config.ID).Msg("failed to load workflow definition")
		s.emitWorkflowPreExecutionCleanup(chatID, agentID)
		return "failed", "", fmt.Errorf("failed to load workflow definition: %w", err)
	}

	engine := s.workflowEngine
	if engine == nil {
		var err error
		engine, err = newWorkflowEngine(s.conf, s, s.funcRegistry, s.resolveWorkflowDefinition, s.customRunners...)
		if err != nil {
			log.Error().Err(err).Str("agent", agent.Config.ID).Msg("failed to create workflow engine")
			s.emitWorkflowPreExecutionCleanup(chatID, agentID)
			return "failed", "", fmt.Errorf("failed to create workflow engine: %w", err)
		}
		s.mu.RLock()
		if len(s.agents) > 0 {
			agentsSnapshot := make([]*agentspec.Agent, len(s.agents))
			copy(agentsSnapshot, s.agents)
			engine.SetAgents(agentsSnapshot)
		}
		s.mu.RUnlock()
	}

	executor := workflow.NewWorkflowExecutor(engine, defn)
	executor.AgentName = agent.Config.Name
	executor.WorkflowRunDirs = agent.Config.RunDirs
	executor.WorkflowMountDirs = workflowspec.MountDirsConfig{
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
	augmentedPrompt := formatPromptWithAttachments(req.Prompt, req.Attachments)
	go func() {
		res, err := executor.Execute(ctx, workflow.WorkflowRunParams{
			SessionID: chatID,
			Prompt:    augmentedPrompt,
			RunDir:    req.RunDir,
			Headless:  req.Headless,
			Metadata:  req.Metadata,
		})
		resultCh <- runResult{result: res, err: err}
	}()

	select {
	case <-suspendCh:
		return "waiting_human", "", nil
	case res := <-resultCh:
		if res.err != nil {
			s.emitWorkflowPreExecutionCleanup(chatID, agentID)
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
func (s *Server) persistIncomingWorkflowMessage(agent *agentspec.Agent, chatID string, req TriggerMessageRequest) {
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
		userMsgID = fmt.Sprintf("msg-%s", uuid.NewV7().String())
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
		Attachments:  req.Attachments,
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
func (s *Server) maybeGenerateWorkflowTitle(ctx context.Context, agent *agentspec.Agent, chatID string, prompt string) {
	if s == nil || s.repo == nil || chatID == "" || !IsValidChatID(chatID) {
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

func (s *Server) emitWorkflowPreExecutionCleanup(chatID, agentID string) {
	if s.repo != nil && chatID != "" && agentID != "" {
		_ = s.repo.UpdateAgentStatus(chatID, agentID, dbmodels.AgentStatusCompleted)
		s.PublishSessionEvent(chatID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"agent": agentID, "isRunning": false},
		})
		s.PublishSessionEvent(chatID, SessionEvent{
			Type:    "done",
			Payload: map[string]any{"agent": agentID},
		})
	}
}

// handleWorkflowRedrive re-drives a FAILED workflow run from its persisted
// snapshot: SUCCEEDED nodes are seeded as settled history while the failed
// node and everything downstream execute again. The long-running re-drive
// happens in the background; lifecycle events flow into the chat via the
// normal workflow event pipeline.
func (s *Server) handleWorkflowRedrive(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	engine := s.workflowEngine
	if engine == nil || s.workflowRunRepo == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "workflow engine not available")
		return
	}

	row, err := s.workflowRunRepo.GetRunRow(runID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query workflow run: "+err.Error())
		return
	}
	if row == nil {
		writeJSONError(w, http.StatusNotFound, "workflow run not found")
		return
	}
	if row.Status != workflow.PersistStatusFailed {
		writeJSONError(w, http.StatusConflict, fmt.Sprintf("workflow run status is %s; only FAILED runs can be re-driven", row.Status))
		return
	}
	if engine.IsSessionExecuting(row.SessionID) {
		writeJSONError(w, http.StatusConflict, "session is already executing a workflow run")
		return
	}

	chatID := row.SessionID
	agentKey := s.resolveWorkflowAgentKey(chatID, "")
	s.activeExecutions.Store(chatID, &executionHandle{
		agentID:    agentKey,
		isWorkflow: true,
		startTime:  time.Now(),
	})
	if agentKey != "" && s.repo != nil {
		_ = s.repo.UpdateAgentStatus(chatID, agentKey, dbmodels.AgentStatusRunning)
		s.PublishSessionEvent(chatID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"agent": agentKey, "isRunning": true},
		})
	}

	go func() {
		emit := func(ev workflow.WorkflowEvent) {
			sid := ev.SessionID
			if sid == "" {
				sid = chatID
			}
			s.handleWorkflowEvent(sid, ev)
		}
		if _, err := engine.RedriveFailed(context.Background(), runID, emit); err != nil {
			log.Warn().Err(err).Str("run_id", runID).Msg("re-driving failed workflow run failed")
			s.releaseExecution(chatID)
			if agentKey != "" && s.repo != nil {
				_ = s.repo.UpdateAgentStatus(chatID, agentKey, dbmodels.AgentStatusCompleted)
			}
			errMsg := dbmodels.ChatMessage{
				ID:        fmt.Sprintf("error-%s-%s", chatID, uuid.NewV7().String()),
				Role:      "error",
				Content:   fmt.Sprintf("重新执行工作流失败：%v", err),
				Timestamp: time.Now().UnixMilli(),
			}
			_ = s.repo.AppendMessage(chatID, errMsg)
			s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &errMsg})
			s.PublishSessionEvent(chatID, SessionEvent{Type: "status", Payload: map[string]any{"agent": agentKey, "isRunning": false}})
			return
		}
		// Terminal lifecycle (finished/failed/suspended) is driven by
		// handleWorkflowEvent, including the activeExecutions cleanup.
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "accepted",
		"runId":  runID,
		"chatId": chatID,
	})
}

// workflowRunSummary is the client-facing projection of one workflow run of a
// session. It deliberately carries no node states or DAG spec — the UI only
// needs run identity, lifecycle status and timestamps.
type workflowRunSummary struct {
	RunID     string `json:"runId"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// handleSessionWorkflowRuns lists the workflow runs of a session, most
// recently updated first. GET /api/sessions/{id}/workflows
func (s *Server) handleSessionWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if !IsValidChatID(sessionID) {
		writeJSONError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	if s.workflowRunRepo == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "workflow runs are not available")
		return
	}

	runs, err := s.workflowRunRepo.ListRunsBySession(sessionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list workflow runs: "+err.Error())
		return
	}

	out := make([]workflowRunSummary, 0, len(runs))
	for _, run := range runs {
		sum := workflowRunSummary{
			RunID:     run.RunID,
			Status:    run.Status,
			CreatedAt: run.CreatedAt.Format(time.RFC3339),
			UpdatedAt: run.UpdatedAt.Format(time.RFC3339),
		}
		out = append(out, sum)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"runs": out})
}
