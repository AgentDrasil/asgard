package api

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/google/uuid"
	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/agents/run"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
	"github.com/AgentDrasil/asgard/lib/llm"
)

// SingleAgentExecutor 负责单体 Agent 任务的响应与处理，直接满足 a2asrv 处理器契约。
type SingleAgentExecutor struct {
	agent     *agents.Agent
	conf      *config.Config
	repo      *dbmodels.SessionRepository
	server    *Server
	llmClient llm.Client
}

// NewSingleAgentExecutor creates a SingleAgentExecutor for the given agent.
// llmClient may be nil; in that case a genai-backed client is created lazily
// for session title generation using the configured (or env) API key.
func NewSingleAgentExecutor(agent *agents.Agent, conf *config.Config, repo *dbmodels.SessionRepository, server *Server, llmClient llm.Client) *SingleAgentExecutor {
	return &SingleAgentExecutor{
		agent:     agent,
		conf:      conf,
		repo:      repo,
		server:    server,
		llmClient: llmClient,
	}
}

// Execute handles the agent execution.
func (e *SingleAgentExecutor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
				return
			}
		}

		chatID := execCtx.ContextID
		if chatID == "" {
			chatID = uuid.Must(uuid.NewV7()).String()
		} else if !IsValidChatID(chatID) {
			yield(nil, fmt.Errorf("invalid chatID format"))
			return
		}

		// Resolve session mode from agent config (empty string = default).
		sessionMode := e.agent.Config.SessionMode // "" or "resume" → resume; "fresh" → fresh

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
		// Always lock to established session.RunDir if session already exists
		if session != nil && session.RunDir != "" {
			runDirOpt = optional.Some(session.RunDir)
		} else if execCtx.Metadata != nil {
			if rd, ok := execCtx.Metadata["run_dir"].(string); ok && rd != "" {
				runDirOpt = optional.Some(rd)
			}
		}
		if runDirOpt.IsNone() && len(e.agent.Config.RunDirs) > 0 && e.agent.Config.RunDirs[0] != "" {
			runDirOpt = optional.Some(e.agent.Config.RunDirs[0])
		}

		modelOpt := optional.None[string]()
		if execCtx.Metadata != nil {
			if m, ok := execCtx.Metadata["model"].(string); ok && m != "" {
				modelOpt = optional.Some(m)
			}
		}

		// Validate run_dir allowlist and existence BEFORE any DB writes or title generation
		if runDirOpt.IsSome() {
			rd := runDirOpt.Unwrap()
			if rd != "" {
				if len(e.agent.Config.RunDirs) > 0 && !run.IsAllowedDir(rd, e.agent.Config.RunDirs) {
					yield(nil, fmt.Errorf("run directory %q is not allowed by agent configuration", rd))
					return
				}
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
			if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.ID, "", "", runDirOpt); err != nil {
				yield(nil, fmt.Errorf("failed to pre-update agent session: %w", err))
				return
			}
			// Save incoming message to session in DB
			if prompt != "" {
				userMsgID := ""
				isInternal := false
				callerName := ""
				if execCtx.Message != nil {
					userMsgID = execCtx.Message.ID
					if execCtx.Message.Metadata != nil {
						if v, ok := execCtx.Message.Metadata["internal"].(bool); ok && v {
							isInternal = true
						}
						if cn, ok := execCtx.Message.Metadata["caller_agent_name"].(string); ok {
							callerName = cn
						}
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
						agentName = e.agent.Config.Name
					}
				}
				if err := e.repo.AppendMessage(chatID, dbmodels.ChatMessage{
					ID:           userMsgID,
					Role:         role,
					ActivityType: activityType,
					Content:      prompt,
					AgentName:    agentName,
					Timestamp:    time.Now().UnixMilli(),
				}); err != nil {
					log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append incoming message to repo")
				}
			}

			// Only update status if this is the primary/entry agent for the session
			if session == nil || session.CurrentAgent == "" || session.CurrentAgent == e.agent.Config.Name || session.CurrentAgent == e.agent.Config.ID {
				if err := e.repo.UpdateAgentStatus(chatID, e.agent.Config.ID, dbmodels.AgentStatusRunning); err != nil {
					yield(nil, fmt.Errorf("failed to update agent status to running: %w", err))
					return
				}
			}

			// Generate title on first request if session has no title
			if session == nil || session.Title == "" {
				e.goGenerateTitle(ctx, chatID, prompt)
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

		// Always mark the agent as completed once the sandbox execution
		// finishes, regardless of success, error, client disconnect, or
		// context cancellation. This guarantees the session's isRunning
		// state is cleared after the agent sandbox exits.
		defer e.markAgentCompleted(chatID)
		e.executeSequential(ctx, yield, execCtx, prompt, chatID, runDirOpt, modelOpt, sessionMode, session, statusCh)
	}
}

// goGenerateTitle spawns a background goroutine that generates and persists a
// session title for the given prompt on first contact.
func (e *SingleAgentExecutor) goGenerateTitle(ctx context.Context, chatID string, prompt string) {
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
		titleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		client := e.llmClient
		if client == nil {
			if apiKey == "" {
				apiKey = os.Getenv("GEMINI_API_KEY")
			}
			if apiKey == "" {
				log.Warn().Msg("failed to generate session title via gemini: gemini api key not configured")
				return
			}
			var err error
			client, err = llm.NewClient(titleCtx, apiKey)
			if err != nil {
				log.Warn().Err(err).Msg("failed to generate session title via gemini")
				return
			}
		}

		title, err := generateSessionTitle(titleCtx, client, model, prompt, agentID, agentDesc)
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

// markAgentCompleted unconditionally sets the agent status to completed for the
// given chat. It is intended to be deferred from Execute so the session's
// isRunning flag is always cleared after the agent sandbox exits.
func (e *SingleAgentExecutor) markAgentCompleted(chatID string) {
	if e.repo == nil {
		return
	}
	if err := e.repo.UpdateAgentStatus(chatID, e.agent.Config.ID, dbmodels.AgentStatusCompleted); err != nil {
		log.Error().Err(err).Str("chat_id", chatID).Str("agent", e.agent.Config.ID).Msg("failed to mark agent status completed after run")
	}
}

// recordStatusUpdate processes an incremental status update from an agent run,
// saving artifacts and messages to the session database, and logging warnings on error.
func recordStatusUpdate(repo *dbmodels.SessionRepository, chatID string, update AgentStatusUpdate, agentConfig *agents.AgentConfig, workspaceDir string) {
	if repo == nil || update.Content == "" || update.EntryType == "agent_response" {
		return
	}
	role := update.EntryType
	if role == "" || role == "other" {
		role = "activity"
	}
	agentName := ""
	if agentConfig != nil {
		agentName = agentConfig.Name
	}
	if name, ok := update.Metadata["agent_name"].(string); ok && name != "" {
		agentName = name
	}
	targetFiles := toStringSlice(update.Metadata["target_files"])
	var artifactFiles []string
	for _, tf := range targetFiles {
		if agents.IsArtifact(tf, agentConfig, workspaceDir) {
			artifactFiles = append(artifactFiles, tf)
			if err := repo.AppendArtifact(chatID, tf); err != nil {
				log.Warn().Err(err).Str("chat_id", chatID).Str("target_file", tf).Msg("failed to append artifact to repo")
			}
		}
	}
	if update.Metadata == nil {
		update.Metadata = make(map[string]any)
	}
	if len(artifactFiles) > 0 {
		update.Metadata["artifact_files"] = artifactFiles
	}
	if err := repo.AppendMessage(chatID, dbmodels.ChatMessage{
		ID:            fmt.Sprintf("step-%s-%d", chatID, update.StepIndex),
		Role:          role,
		Content:       update.Content,
		AgentName:     agentName,
		Timestamp:     time.Now().UnixMilli(),
		ActivityType:  strings.ToUpper(role),
		StepIndex:     update.StepIndex,
		TargetFiles:   targetFiles,
		ArtifactFiles: artifactFiles,
	}); err != nil {
		log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append step status message to repo")
	}
}

// toStringSlice coerces a JSON-decoded value into a []string. It accepts both
// []string and []any (the shape produced by encoding/json for JSON arrays),
// dropping any non-string or empty entries.
func toStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
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

// Cancel handles canceling an execution.
func (e *SingleAgentExecutor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		// Emit TaskStatusUpdateEvent with TaskStateCanceled.
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

func generateSessionTitle(ctx context.Context, client llm.Client, model string, req string, agentID string, agentDesc string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("llm client not configured")
	}

	if model == "" {
		model = "gemini-3.1-flash-lite"
	}

	prompt := fmt.Sprintf(
		"User is sending a request %q to agent %q (%s). Generate a short, clear, and descriptive title (3 to 8 words) for this chat session. Keep it short. Do not use quotation marks, markdown, or prefixes. Output only the title text.",
		req, agentID, agentDesc,
	)

	title, err := client.GenerateText(ctx, llm.GenerateOptions{
		Model:  model,
		Prompt: prompt,
	})
	if err != nil {
		return "", fmt.Errorf("gemini generate content failed: %w", err)
	}

	title = strings.TrimSpace(title)
	title = strings.Trim(title, "\"`")
	return title, nil
}
