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
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

type agentExecutor struct {
	agent     *agents.Agent
	repo      *dbmodels.SessionRepository
	server    *Server
	statusURL string
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

		var session *dbmodels.Session
		agentSessionID := optional.None[string]()
		if e.repo != nil {
			var err error
			session, err = e.repo.GetSession(chatID)
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

		runDirOpt := optional.None[string]()
		if execCtx.Metadata != nil {
			if rd, ok := execCtx.Metadata["run_dir"].(string); ok && rd != "" {
				runDirOpt = optional.Some(rd)
			}
		}
		if runDirOpt.IsNone() && session != nil && session.RunDir != "" {
			runDirOpt = optional.Some(session.RunDir)
		}

		if e.repo != nil {
			if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.Name, "", runDirOpt); err != nil {
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
		if e.server != nil && e.statusURL != "" {
			statusCh, cancelListener = e.server.AddStatusListener(chatID)
			defer cancelListener()
		}

		// ── Run the agent in a goroutine, collect result on resultCh ──────────
		type runResult struct {
			out []byte
			err error
		}
		resultCh := make(chan runResult, 1)
		go func() {
			out, err := run.Run(ctx, e.agent, prompt, agentSessionID, runDirOpt, chatID, e.statusURL)
			resultCh <- runResult{out, err}
		}()

		// ── Stream intermediate status events until run.Run completes ─────────
		for {
			if statusCh == nil {
				// No listener configured — just wait for result.
				result := <-resultCh
				if result.err != nil {
					yield(nil, fmt.Errorf("failed to run agent: %w", result.err))
					return
				}
				e.handleFinalResult(yield, execCtx, result.out, chatID, runDirOpt)
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
					_ = e.repo.AppendMessage(chatID, dbmodels.ChatMessage{
						ID:           fmt.Sprintf("step-%s-%d", chatID, update.StepIndex),
						Role:         role,
						Content:      update.Content,
						AgentName:    e.agent.Config.Name,
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
				e.handleFinalResult(yield, execCtx, result.out, chatID, runDirOpt)
				return

			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			}
		}
	}
}

// handleFinalResult parses the agent output and emits the final TaskStatusUpdateEvent.
func (e *agentExecutor) handleFinalResult(
	yield func(a2a.Event, error) bool,
	execCtx *a2asrv.ExecutorContext,
	out []byte,
	chatID string,
	runDirOpt optional.Option[string],
) {
	type promptResult struct {
		SessionID   string  `json:"session_id"`
		InputTokens int     `json:"input_tokens"`
		MaxTokens   int     `json:"max_tokens"`
		Remaining   float64 `json:"remaining"`
		LastContent string  `json:"last_content"`
	}

	var result promptResult
	var respText string
	if err := json.Unmarshal(out, &result); err == nil {
		respText = result.LastContent
		if e.repo != nil && (result.SessionID != "" || runDirOpt.IsSome()) {
			if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.Name, result.SessionID, runDirOpt); err != nil {
				yield(nil, fmt.Errorf("failed to update agent session: %w", err))
				return
			}
		}
	} else {
		respText = string(out)
		if e.repo != nil && runDirOpt.IsSome() {
			if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.Name, "", runDirOpt); err != nil {
				yield(nil, fmt.Errorf("failed to update agent session: %w", err))
				return
			}
		}
	}

	if e.repo != nil {
		sess, err := e.repo.GetSession(chatID)
		if err == nil && sess != nil && (sess.CurrentAgent == "" || sess.CurrentAgent == e.agent.Config.Name) {
			_ = e.repo.UpdateAgentStatus(chatID, e.agent.Config.Name, dbmodels.AgentStatusCompleted)
		}
		// Save final assistant response to DB session
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
func NewAgentHandler(agent *agents.Agent, host string, repo *dbmodels.SessionRepository, server *Server, statusURL string) (http.Handler, *a2a.AgentCard) {
	executor := &agentExecutor{
		agent:     agent,
		repo:      repo,
		server:    server,
		statusURL: statusURL,
	}
	handler := a2asrv.NewHandler(executor)
	restHandler := a2asrv.NewRESTHandler(handler)

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
	RunDirs     []string `json:"run_dirs"`
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
			RunDirs:     agent.Config.RunDirs,
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
