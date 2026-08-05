package api

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"
	"google.golang.org/genai"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/agents/run"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

type agentExecutor struct {
	agent  *agents.Agent
	conf   *config.Config
	repo   *dbmodels.SessionRepository
	server *Server
}

// Execute handles the agent execution.
func (e *agentExecutor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
				return
			}
		}

		chatID := execCtx.ContextID
		if chatID != "" && !IsValidChatID(chatID) {
			yield(nil, fmt.Errorf("invalid chatID format"))
			return
		}

		// Resolve session and run modes from agent config (empty string = default).
		sessionMode := e.agent.Config.SessionMode // "" or "resume" → resume; "fresh" → fresh
		runMode := e.agent.Config.RunMode         // "" or "sequential" → sequential; "parallel" → parallel

		var session *dbmodels.Session
		if e.repo != nil {
			var err error
			session, err = e.repo.GetSession(chatID)
			if err != nil {
				yield(nil, fmt.Errorf("failed to get session: %w", err))
				return
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

		runDirOpt := optional.None[string]()
		if execCtx.Metadata != nil {
			if rd, ok := execCtx.Metadata["run_dir"].(string); ok && rd != "" {
				runDirOpt = optional.Some(rd)
			}
		}
		if runDirOpt.IsNone() && session != nil && session.RunDir != "" {
			runDirOpt = optional.Some(session.RunDir)
		}
		if runDirOpt.IsNone() && len(e.agent.Config.RunDirs) > 0 && e.agent.Config.RunDirs[0] != "" {
			runDirOpt = optional.Some(e.agent.Config.RunDirs[0])
		}

		if runDirOpt.IsSome() {
			rd, _ := runDirOpt.Take()
			if rd != "" {
				info, err := os.Stat(rd)
				if err != nil {
					yield(nil, fmt.Errorf("run_dir %q does not exist: %w", rd, err))
					return
				}
				if !info.IsDir() {
					yield(nil, fmt.Errorf("run_dir %q is not a directory", rd))
					return
				}
			}
		}

		if e.repo != nil {
			if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.Name, "", "", runDirOpt); err != nil {
				yield(nil, fmt.Errorf("failed to pre-update agent session: %w", err))
				return
			}
			// Save incoming user message to session in DB
			if prompt != "" {
				userMsgID := ""
				if execCtx.Message != nil {
					userMsgID = execCtx.Message.ID
				}
				_ = e.repo.AppendMessage(chatID, dbmodels.ChatMessage{
					ID:        userMsgID,
					Role:      "user",
					Content:   prompt,
					Timestamp: time.Now().UnixMilli(),
				})
			}

			// Only update status if this is the primary/entry agent for the session
			if session == nil || session.CurrentAgent == "" || session.CurrentAgent == e.agent.Config.Name {
				if err := e.repo.UpdateAgentStatus(chatID, e.agent.Config.Name, dbmodels.AgentStatusRunning); err != nil {
					yield(nil, fmt.Errorf("failed to update agent status to running: %w", err))
					return
				}
			}

			// Generate title on first request if session has no title
			if session == nil || session.Title == "" {
				apiKey := ""
				model := ""
				if e.server != nil && e.server.conf != nil {
					apiKey = e.server.conf.GeminiAPIKey
					model = e.server.conf.GeminiModelForChatTitle
				}
				agentID := e.agent.Config.ID
				agentDesc := e.agent.Config.Description
				repo := e.repo

				go func() {
					titleCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					title, err := generateSessionTitle(titleCtx, apiKey, model, prompt, agentID, agentDesc)
					if err != nil {
						log.Warn().Err(err).Msg("failed to generate session title via gemini")
						return
					}
					if title != "" {
						if err := repo.UpdateSessionTitle(chatID, title); err != nil {
							log.Warn().Err(err).Msg("failed to update session title in repo")
						}
					}
				}()
			}
		}

		// ── Subscribe to status updates for this chat ─────────────────────────
		// statusCh receives incremental AgentStatusUpdate events while run.Run executes.
		var statusCh <-chan AgentStatusUpdate
		var cancelListener func()
		if e.server != nil && e.conf != nil {
			statusCh, cancelListener = e.server.AddStatusListener(chatID)
			defer cancelListener()
		}

		if runMode == "parallel" {
			e.executeParallel(ctx, yield, execCtx, prompt, chatID, runDirOpt, sessionMode, statusCh)
		} else {
			e.executeSequential(ctx, yield, execCtx, prompt, chatID, runDirOpt, sessionMode, session, statusCh)
		}
	}
}

// seqRunResult carries the output of a sequential run.Run call.
type seqRunResult struct {
	out []byte
	err error
}

// executeSequential runs the first available CLI target (by quota) and streams results.
func (e *agentExecutor) executeSequential(
	ctx context.Context,
	yield func(a2a.Event, error) bool,
	execCtx *a2asrv.ExecutorContext,
	prompt string,
	chatID string,
	runDirOpt optional.Option[string],
	sessionMode string,
	session *dbmodels.Session,
	statusCh <-chan AgentStatusUpdate,
) {
	// Resolve which session ID to resume (if any).
	agentSessionID := optional.None[string]()
	if sessionMode != "fresh" && e.repo != nil && session != nil {
		for _, dbAgent := range session.Agents {
			if dbAgent.Name == e.agent.Config.Name {
				// For sequential mode, pick the first session from the map (if any).
				for _, sid := range dbAgent.Sessions {
					if sid != "" {
						agentSessionID = optional.Some(sid)
					}
					break
				}
				break
			}
		}
	}

	// ── Run the agent in a goroutine, collect result on resultCh ──────────
	resultCh := make(chan seqRunResult, 1)
	go func() {
		out, err := run.Run(ctx, e.agent, prompt, agentSessionID, runDirOpt, chatID, e.conf)
		resultCh <- seqRunResult{out, err}
	}()

	e.streamAndFinish(ctx, yield, execCtx, chatID, runDirOpt, sessionMode, statusCh, resultCh)
}

// executeParallel runs ALL CLI targets concurrently and combines their results into a single response.
func (e *agentExecutor) executeParallel(
	ctx context.Context,
	yield func(a2a.Event, error) bool,
	execCtx *a2asrv.ExecutorContext,
	prompt string,
	chatID string,
	runDirOpt optional.Option[string],
	sessionMode string,
	statusCh <-chan AgentStatusUpdate,
) {
	// Resolve sessions map for resume mode.
	sessions := map[string]string{}
	if sessionMode != "fresh" && e.repo != nil {
		if s, err := e.repo.GetAgentSessions(chatID, e.agent.Config.Name); err == nil && s != nil {
			sessions = s
		}
	}

	// ── Run all targets concurrently ──────────────────────────────────────
	type runAllResult struct {
		results []run.RunResult
	}
	resultCh := make(chan runAllResult, 1)
	go func() {
		results := run.RunAll(ctx, e.agent, prompt, sessions, runDirOpt, chatID, e.conf)
		resultCh <- runAllResult{results}
	}()

	// Drain status events while waiting for all targets to finish.
	for {
		if statusCh == nil {
			result := <-resultCh
			e.handleParallelResult(yield, execCtx, result.results, chatID, runDirOpt, sessionMode)
			return
		}

		select {
		case update, ok := <-statusCh:
			if !ok {
				statusCh = nil
				continue
			}
			if e.repo != nil && update.Content != "" && update.EntryType != "agent_response" {
				role := update.EntryType
				if role == "" || role == "other" {
					role = "activity"
				}
				agentName := e.agent.Config.Name
				if name, ok := update.Metadata["agent_name"].(string); ok && name != "" {
					agentName = name
				}
				_ = e.repo.AppendMessage(chatID, dbmodels.ChatMessage{
					ID:           fmt.Sprintf("step-%s-%d", chatID, update.StepIndex),
					Role:         role,
					Content:      update.Content,
					AgentName:    agentName,
					Timestamp:    time.Now().UnixMilli(),
					ActivityType: strings.ToUpper(role),
					StepIndex:    update.StepIndex,
				})
			}
			updateMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(update.Content))
			metadata := map[string]any{
				"entry_type": update.EntryType,
				"source":     update.Source,
				"step_index": update.StepIndex,
			}
			for k, v := range update.Metadata {
				metadata[k] = v
			}
			updateMsg.Metadata = metadata
			evt := a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, updateMsg)
			if !yield(evt, nil) {
				return
			}

		case result := <-resultCh:
			e.handleParallelResult(yield, execCtx, result.results, chatID, runDirOpt, sessionMode)
			return

		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
	}
}

// streamAndFinish drains status events from statusCh until resultCh delivers the final output.
func (e *agentExecutor) streamAndFinish(
	ctx context.Context,
	yield func(a2a.Event, error) bool,
	execCtx *a2asrv.ExecutorContext,
	chatID string,
	runDirOpt optional.Option[string],
	sessionMode string,
	statusCh <-chan AgentStatusUpdate,
	resultCh <-chan seqRunResult,
) {
	for {
		if statusCh == nil {
			// No listener configured — just wait for result.
			result := <-resultCh
			if result.err != nil {
				yield(nil, fmt.Errorf("failed to run agent: %w", result.err))
				return
			}
			e.handleFinalResult(yield, execCtx, result.out, chatID, runDirOpt, sessionMode)
			return
		}

		select {
		case update, ok := <-statusCh:
			if !ok {
				// Channel closed unexpectedly; wait for result.
				statusCh = nil
				continue
			}
			// Save status update to session DB if content is present and not agent_response (which is saved as final result)
			if e.repo != nil && update.Content != "" && update.EntryType != "agent_response" {
				role := update.EntryType
				if role == "" || role == "other" {
					role = "activity"
				}
				agentName := e.agent.Config.Name
				if name, ok := update.Metadata["agent_name"].(string); ok && name != "" {
					agentName = name
				}
				_ = e.repo.AppendMessage(chatID, dbmodels.ChatMessage{
					ID:           fmt.Sprintf("step-%s-%d", chatID, update.StepIndex),
					Role:         role,
					Content:      update.Content,
					AgentName:    agentName,
					Timestamp:    time.Now().UnixMilli(),
					ActivityType: strings.ToUpper(role),
					StepIndex:    update.StepIndex,
				})
			}

			// Emit an intermediate TaskStatusUpdateEvent.
			updateMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(update.Content))
			metadata := map[string]any{
				"entry_type": update.EntryType,
				"source":     update.Source,
				"step_index": update.StepIndex,
			}
			for k, v := range update.Metadata {
				metadata[k] = v
			}
			updateMsg.Metadata = metadata
			evt := a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, updateMsg)
			if !yield(evt, nil) {
				return
			}

		case result := <-resultCh:
			if result.err != nil {
				yield(nil, fmt.Errorf("failed to run agent: %w", result.err))
				return
			}
			e.handleFinalResult(yield, execCtx, result.out, chatID, runDirOpt, sessionMode)
			return

		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
	}
}

// promptResult is the JSON structure returned by CLI agents.
type promptResult struct {
	SessionID   string  `json:"session_id"`
	InputTokens int     `json:"input_tokens"`
	MaxTokens   int     `json:"max_tokens"`
	Remaining   float64 `json:"remaining"`
	LastContent string  `json:"last_content"`
}

// parseOutput attempts to JSON-decode a CLI output, falling back to raw string.
func parseOutput(out []byte) (lastContent string, sessionID string, inputTokens int, maxTokens int) {
	var result promptResult
	if err := json.Unmarshal(out, &result); err == nil {
		return result.LastContent, result.SessionID, result.InputTokens, result.MaxTokens
	}
	return strings.TrimSpace(string(out)), "", 0, 0
}

// handleFinalResult parses the agent output and emits the final TaskStatusUpdateEvent.
// sessionMode controls whether the returned session ID is persisted to DB.
func (e *agentExecutor) handleFinalResult(
	yield func(a2a.Event, error) bool,
	execCtx *a2asrv.ExecutorContext,
	out []byte,
	chatID string,
	runDirOpt optional.Option[string],
	sessionMode string,
) {
	respText, sessionID, inputTokens, maxTokens := parseOutput(out)

	if e.repo != nil {
		// Always update runDir; only persist sessionID in resume mode.
		cliKey := ""
		persistSessionID := ""
		if sessionMode != "fresh" && sessionID != "" {
			cliKey = "sequential"
			persistSessionID = sessionID
		}
		if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.Name, cliKey, persistSessionID, runDirOpt); err != nil {
			yield(nil, fmt.Errorf("failed to update agent session: %w", err))
			return
		}

		sess, err := e.repo.GetSession(chatID)
		if err == nil && sess != nil && (sess.CurrentAgent == "" || sess.CurrentAgent == e.agent.Config.Name) {
			_ = e.repo.UpdateAgentStatus(chatID, e.agent.Config.Name, dbmodels.AgentStatusCompleted)
		}
		// Save final assistant response to DB session
		if respText != "" {
			_ = e.repo.AppendMessage(chatID, dbmodels.ChatMessage{
				ID:          fmt.Sprintf("assistant-%s-%d", chatID, time.Now().UnixNano()),
				Role:        "assistant",
				Content:     respText,
				AgentName:   e.agent.Config.Name,
				Timestamp:   time.Now().UnixMilli(),
				InputTokens: inputTokens,
				MaxTokens:   maxTokens,
			})
		}
	}

	respMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(respText))
	if inputTokens > 0 || maxTokens > 0 {
		respMsg.Metadata = map[string]any{
			"input_tokens": inputTokens,
			"max_tokens":   maxTokens,
		}
	}
	yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, respMsg), nil)
}

// handleParallelResult combines all RunResult outputs into a single final message.
// Session IDs are persisted per-target (keyed by CLIKey) unless sessionMode is "fresh".
func (e *agentExecutor) handleParallelResult(
	yield func(a2a.Event, error) bool,
	execCtx *a2asrv.ExecutorContext,
	results []run.RunResult,
	chatID string,
	runDirOpt optional.Option[string],
	sessionMode string,
) {
	var combined strings.Builder
	for _, r := range results {
		lastContent, sessionID, _, _ := parseOutput(r.Output)

		// Persist session ID per CLI target (resume mode only).
		if e.repo != nil && sessionMode != "fresh" && sessionID != "" && r.CLIKey != "" {
			if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.Name, r.CLIKey, sessionID, runDirOpt); err != nil {
				log.Warn().Err(err).Str("cliKey", r.CLIKey).Msg("failed to update agent session for parallel target")
			}
		} else if e.repo != nil && combined.Len() == 0 {
			// Still update runDir on the first target even in fresh mode.
			_ = e.repo.UpdateAgentSession(chatID, e.agent.Config.Name, "", "", runDirOpt)
		}

		if combined.Len() > 0 {
			combined.WriteString("\n\n")
		}
		if r.Err != nil {
			fmt.Fprintf(&combined, "--- %s (error) ---\n%s", r.CLIKey, r.Err.Error())
		} else {
			fmt.Fprintf(&combined, "--- %s ---\n%s", r.CLIKey, lastContent)
		}
	}

	respText := combined.String()

	if e.repo != nil {
		sess, err := e.repo.GetSession(chatID)
		if err == nil && sess != nil && (sess.CurrentAgent == "" || sess.CurrentAgent == e.agent.Config.Name) {
			_ = e.repo.UpdateAgentStatus(chatID, e.agent.Config.Name, dbmodels.AgentStatusCompleted)
		}
		if respText != "" {
			_ = e.repo.AppendMessage(chatID, dbmodels.ChatMessage{
				ID:        fmt.Sprintf("assistant-%s-%d", chatID, time.Now().UnixNano()),
				Role:      "assistant",
				Content:   respText,
				AgentName: e.agent.Config.Name,
				Timestamp: time.Now().UnixMilli(),
			})
		}
	}

	respMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(respText))
	yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, respMsg), nil)
}

// Cancel handles canceling an execution.
func (e *agentExecutor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		// Emit TaskStatusUpdateEvent with TaskStateCanceled.
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

// NewAgentHandler creates the A2A HTTP REST handler and the AgentCard for the given agent.
func NewAgentHandler(agent *agents.Agent, conf *config.Config, repo *dbmodels.SessionRepository, server *Server) (http.Handler, *a2a.AgentCard) {
	executor := &agentExecutor{
		agent:  agent,
		conf:   conf,
		repo:   repo,
		server: server,
	}
	handler := a2asrv.NewHandler(executor)
	restHandler := a2asrv.NewRESTHandler(handler)

	host := "http://localhost:8080"
	if conf != nil && conf.Host != "" {
		host = conf.Host
	}

	card := &a2a.AgentCard{
		Name:        agent.Config.Name,
		Description: agent.Config.Description,
		Version:     "1.0.0",
		Capabilities: a2a.AgentCapabilities{
			Streaming: true,
		},
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(fmt.Sprintf("%s/agents/%s", host, agent.Config.ID), a2a.TransportProtocolHTTPJSON),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}

	return restHandler, card
}

// AgentInfo holds details about an agent for the frontend UI.
type AgentInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	RunDirs     []string `json:"run_dirs"`
	MainAgent   bool     `json:"main_agent"`
}

// handleAgents handles GET /agents to list loaded agent names.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	richAgents := make([]AgentInfo, 0, len(s.agents))
	for _, agent := range s.agents {
		richAgents = append(richAgents, AgentInfo{
			ID:          agent.Config.ID,
			Name:        agent.Config.Name,
			Description: agent.Config.Description,
			Icon:        agent.Config.Icon,
			RunDirs:     agent.Config.RunDirs,
			MainAgent:   agent.Config.IsMainAgent(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(richAgents)
}

func generateSessionTitle(ctx context.Context, apiKey string, model string, req string, agentID string, agentDesc string) (string, error) {
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		return "", fmt.Errorf("gemini api key not configured")
	}

	if model == "" {
		model = "gemini-3.1-flash-lite"
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: apiKey,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create genai client: %w", err)
	}

	prompt := fmt.Sprintf(
		"User is sending a request %q to agent %q (%s). Generate a short, clear, and descriptive title (3 to 8 words) for this chat session. Keep it short. Do not use quotation marks, markdown, or prefixes. Output only the title text.",
		req, agentID, agentDesc,
	)

	resp, err := client.Models.GenerateContent(ctx, model, genai.Text(prompt), nil)
	if err != nil {
		return "", fmt.Errorf("gemini generate content failed: %w", err)
	}

	title := strings.TrimSpace(resp.Text())
	title = strings.Trim(title, "\"`")
	return title, nil
}
