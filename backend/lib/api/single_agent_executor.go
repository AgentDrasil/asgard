package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"uuid"

	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/llm"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// SingleAgentRunParams carries the parameters for a single agent execution.
type SingleAgentRunParams struct {
	ChatID      string
	Prompt      string
	RunDir      string
	Model       string
	Metadata    map[string]any
	Attachments []dbmodels.Attachment
}

// SingleAgentExecutor handles responding to and processing single-agent tasks.
type SingleAgentExecutor struct {
	agent     *agentspec.Agent
	conf      *config.Config
	repo      *dbmodels.SessionRepository
	server    *Server
	llmClient llm.Client
}

// NewSingleAgentExecutor creates a SingleAgentExecutor for the given agent.
// llmClient may be nil; in that case a genai-backed client is created lazily
// for session title generation using the configured (or env) API key.
func NewSingleAgentExecutor(agent *agentspec.Agent, conf *config.Config, repo *dbmodels.SessionRepository, server *Server, llmClient llm.Client) *SingleAgentExecutor {
	return &SingleAgentExecutor{
		agent:     agent,
		conf:      conf,
		repo:      repo,
		server:    server,
		llmClient: llmClient,
	}
}

// Execute handles the agent execution.
func (e *SingleAgentExecutor) Execute(ctx context.Context, params SingleAgentRunParams) (string, error) {
	chatID := params.ChatID
	if chatID == "" {
		chatID = uuid.NewV7().String()
	} else if !IsValidChatID(chatID) {
		return "", fmt.Errorf("invalid chatID format")
	}

	// Resolve session mode from agent config (empty string = default).
	sessionMode := e.agent.Config.SessionMode // "" or "resume" → resume; "fresh" → fresh

	var session *dbmodels.Session
	if e.repo != nil {
		var err error
		session, err = e.repo.GetSession(chatID)
		if err != nil {
			return "", fmt.Errorf("failed to get session: %w", err)
		}
	}

	prompt := params.Prompt

	rawRunDir := ""
	// Always lock to established session.RunDir if session already exists
	if session != nil && session.RunDir != "" {
		rawRunDir = session.RunDir
	} else if params.RunDir != "" {
		rawRunDir = params.RunDir
	} else if params.Metadata != nil {
		if rd, ok := params.Metadata["run_dir"].(string); ok && rd != "" {
			rawRunDir = rd
		}
	}
	if rawRunDir == "" && len(e.agent.Config.RunDirs) > 0 && e.agent.Config.RunDirs[0] != "" {
		rawRunDir = e.agent.Config.RunDirs[0]
	}

	normalizedRunDir := NormalizeSessionRunDir(rawRunDir, chatID)
	runDirOpt := optional.None[string]()
	if normalizedRunDir != "" {
		runDirOpt = optional.Some(normalizedRunDir)
	}

	modelOpt := optional.None[string]()
	if params.Model != "" {
		modelOpt = optional.Some(params.Model)
	} else if params.Metadata != nil {
		if m, ok := params.Metadata["model"].(string); ok && m != "" {
			modelOpt = optional.Some(m)
		}
	}

	// Validate run_dir allowlist and existence BEFORE any DB writes or title generation
	if runDirOpt.IsSome() {
		rd := runDirOpt.Unwrap()
		if rd != "" {
			if len(e.agent.Config.RunDirs) > 0 && !run.IsAllowedDir(rd, e.agent.Config.RunDirs) {
				return "", fmt.Errorf("run directory %q is not allowed by agent configuration", rd)
			}
			baseTmp := GetSessionTmpBaseDir(chatID)
			if rd == baseTmp || strings.HasPrefix(rd, baseTmp+string(os.PathSeparator)) {
				_ = os.MkdirAll(rd, 0755)
			}
			info, err := os.Stat(rd)
			if err != nil {
				return "", fmt.Errorf("run_dir %q does not exist: %w", rd, err)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("run_dir %q is not a directory", rd)
			}
		}
	}

	if e.repo != nil {
		if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.ID, "", "", runDirOpt); err != nil {
			return "", fmt.Errorf("failed to pre-update agent session: %w", err)
		}
		// Save incoming message to session in DB
		if prompt != "" {
			userMsgID := ""
			isInternal := false
			callerName := ""
			if params.Metadata != nil {
				if v, ok := params.Metadata["internal"].(bool); ok && v {
					isInternal = true
				}
				if cn, ok := params.Metadata["caller_agent_name"].(string); ok {
					callerName = cn
				}
				if mid, ok := params.Metadata["message_id"].(string); ok && mid != "" {
					userMsgID = mid
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
					agentName = e.agent.Config.Name
				}
			}
			userMsg := dbmodels.ChatMessage{
				ID:           userMsgID,
				Role:         role,
				ActivityType: activityType,
				Content:      prompt,
				AgentName:    agentName,
				Timestamp:    time.Now().UnixMilli(),
				Attachments:  params.Attachments,
			}
			if err := e.repo.AppendMessage(chatID, userMsg); err != nil {
				log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append incoming message to repo")
			} else if e.server != nil {
				e.server.PublishSessionEvent(chatID, SessionEvent{
					Type:    "message",
					Message: &userMsg,
				})
			}
		}

		// Only update status if this is the primary/entry agent for the session
		if session == nil || session.CurrentAgent == "" || session.CurrentAgent == e.agent.Config.Name || session.CurrentAgent == e.agent.Config.ID {
			if err := e.repo.UpdateAgentStatus(chatID, e.agent.Config.ID, dbmodels.AgentStatusRunning); err != nil {
				return "", fmt.Errorf("failed to update agent status to running: %w", err)
			}
			if e.server != nil {
				e.server.PublishSessionEvent(chatID, SessionEvent{
					Type:    "status",
					Payload: map[string]any{"agent": e.agent.Config.ID, "isRunning": true},
				})
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
		statusCh, cancelListener = e.server.AddStatusListener(chatID, nil)
		defer cancelListener()
	}

	// Always mark the agent as completed once the sandbox execution
	// finishes, regardless of success, error, client disconnect, or
	// context cancellation. This guarantees the session's isRunning
	// state is cleared after the agent sandbox exits.
	defer e.markAgentCompleted(chatID)
	augmentedPrompt := formatPromptWithAttachments(prompt, params.Attachments)
	return e.executeSequential(ctx, augmentedPrompt, chatID, runDirOpt, modelOpt, sessionMode, session, statusCh)
}

// goGenerateTitle spawns a background goroutine that generates and persists a
// session title for the given prompt on first contact.
func (e *SingleAgentExecutor) goGenerateTitle(ctx context.Context, chatID string, prompt string) {
	goGenerateSessionTitle(ctx, e.server, e.llmClient, e.repo, chatID, prompt, e.agent.Config.ID, e.agent.Config.Description)
}

// goGenerateSessionTitle spawns a background goroutine that generates a session
// title for the given prompt via the Gemini API and persists it to the session
// repository. If LLM generation is unconfigured, fails, or produces an empty title,
// it falls back to a timestamped title ("2006-01-02 15:04:05").
func goGenerateSessionTitle(ctx context.Context, server *Server, client llm.Client, repo *dbmodels.SessionRepository, chatID string, prompt string, agentID string, agentDesc string) {
	if repo == nil || chatID == "" {
		return
	}

	applyTitle := func(title string) {
		if title == "" {
			return
		}
		if err := repo.UpdateSessionTitle(chatID, title); err != nil {
			log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to update session title in repo")
		} else if server != nil {
			server.PublishSessionEvent(chatID, SessionEvent{
				Type:    "title",
				Payload: map[string]any{"title": title},
			})
		}
	}

	fallbackTitle := time.Now().Format("2006-01-02 15:04:05")

	if strings.TrimSpace(prompt) == "" {
		applyTitle(fallbackTitle)
		return
	}

	apiKey := ""
	model := ""
	if server != nil && server.conf != nil {
		apiKey = server.conf.GeminiAPIKey
		model = server.conf.GeminiModelForChatTitle
	}

	go func() {
		titleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		if client == nil {
			if apiKey == "" {
				apiKey = os.Getenv("GEMINI_API_KEY")
			}
			if apiKey == "" {
				log.Warn().Msg("gemini api key not configured; falling back to timestamp session title")
				applyTitle(fallbackTitle)
				return
			}
			var err error
			client, err = llm.NewClient(titleCtx, apiKey)
			if err != nil {
				log.Warn().Err(err).Msg("failed to create gemini client for session title; falling back to timestamp session title")
				applyTitle(fallbackTitle)
				return
			}
		}

		title, err := generateSessionTitle(titleCtx, client, model, prompt, agentID, agentDesc)
		if err != nil {
			log.Warn().Err(err).Msg("failed to generate session title via gemini; falling back to timestamp session title")
			applyTitle(fallbackTitle)
			return
		}
		if strings.TrimSpace(title) == "" {
			log.Warn().Msg("gemini returned empty session title; falling back to timestamp session title")
			applyTitle(fallbackTitle)
			return
		}

		applyTitle(title)
	}()
}

// markAgentCompleted unconditionally sets the agent status to completed for the
// given chat. It is intended to be deferred from Execute so the session's
// isRunning flag is always cleared after the agent sandbox exits.
func (e *SingleAgentExecutor) markAgentCompleted(chatID string) {
	if e.repo != nil {
		if err := e.repo.UpdateAgentStatus(chatID, e.agent.Config.ID, dbmodels.AgentStatusCompleted); err != nil {
			log.Error().Err(err).Str("chat_id", chatID).Str("agent", e.agent.Config.ID).Msg("failed to mark agent status completed after run")
		}
	}
	if e.server != nil {
		e.server.PublishSessionEvent(chatID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"agent": e.agent.Config.ID, "isRunning": false},
		})
		e.server.PublishSessionEvent(chatID, SessionEvent{
			Type:    "done",
			Payload: map[string]any{"agent": e.agent.Config.ID},
		})
	}
}

// recordStatusUpdate processes an incremental status update from an agent run,
// saving artifacts and messages to the session database, and logging warnings on error.
func recordStatusUpdate(server *Server, repo *dbmodels.SessionRepository, chatID string, update AgentStatusUpdate, agentConfig *agentspec.AgentConfig, workspaceDir string) {
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
	baseTmp := GetSessionTmpBaseDir(chatID)
	isRunDirSessionTmp := (workspaceDir != "" && (workspaceDir == baseTmp || NormalizeSessionRunDir(workspaceDir, chatID) == baseTmp))

	var artifactFiles []string
	for _, tf := range targetFiles {
		processedPath := tf

		if isRelTmp, sub := isRelativeTmpPrefixedPath(tf, chatID); isRelTmp {
			sub = filepath.Clean(sub)
			if sub == "." {
				sub = ""
			}
			canonicalTmpPath := "/tmp"
			if sub != "" {
				canonicalTmpPath = "/tmp/" + sub
			}

			if isRunDirSessionTmp {
				processedPath = canonicalTmpPath
			} else {
				// workspaceDir is regular project workspace
				wsFilePath := tf
				if workspaceDir != "" && !filepath.IsAbs(tf) {
					wsFilePath = filepath.Join(workspaceDir, tf)
				}
				if _, err := os.Stat(wsFilePath); err == nil {
					// Exists in workspace -> keep original workspace relative path
					processedPath = tf
				} else {
					// Does not exist in workspace, check session tmp
					tmpFilePath := filepath.Join(baseTmp, sub)
					if _, err := os.Stat(tmpFilePath); err == nil {
						processedPath = canonicalTmpPath
					} else {
						processedPath = tf
					}
				}
			}
		}

		if agents.IsArtifact(processedPath, agentConfig, workspaceDir) {
			normalizedViewerPath := workflow.ViewerArtifactPath(processedPath, baseTmp)
			artifactFiles = append(artifactFiles, normalizedViewerPath)
		}
	}
	if len(artifactFiles) > 0 {
		if err := repo.AppendArtifacts(chatID, artifactFiles); err != nil {
			log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to append artifacts to repo")
		} else if server != nil {
			server.PublishSessionEvent(chatID, SessionEvent{
				Type:    "artifact",
				Payload: map[string]any{"artifacts": artifactFiles},
			})
		}
	}
	if update.Metadata == nil {
		update.Metadata = make(map[string]any)
	}
	if len(artifactFiles) > 0 {
		update.Metadata["artifact_files"] = workflow.ToAnySlice(artifactFiles)
	}
	stepID := fmt.Sprintf("step-%s-%d", chatID, update.StepIndex)
	if update.RunToken != "" {
		stepID = fmt.Sprintf("step-%s-%s-%d", chatID, update.RunToken, update.StepIndex)
	} else if update.NodeID != "" {
		stepID = fmt.Sprintf("step-%s-%s-%d", chatID, update.NodeID, update.StepIndex)
	}
	msg := dbmodels.ChatMessage{
		ID:            stepID,
		Role:          role,
		Content:       update.Content,
		AgentName:     agentName,
		Timestamp:     time.Now().UnixMilli(),
		ActivityType:  strings.ToUpper(role),
		StepIndex:     update.StepIndex,
		TargetFiles:   targetFiles,
		ArtifactFiles: artifactFiles,
	}
	if err := repo.AppendMessage(chatID, msg); err != nil {
		log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append step status message to repo")
	} else if server != nil {
		server.PublishSessionEvent(chatID, SessionEvent{
			Type:    "message",
			Message: &msg,
		})
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
