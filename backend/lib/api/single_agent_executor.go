package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"uuid"

	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/llm"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/backend/lib/worktree"
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
	// dispatcher executes the agent runs: the chat's runner in production, the
	// server's injected dispatcher otherwise.
	dispatcher dispatch.Dispatcher

	// suspendQuota requests a quota decision from the user and blocks until a
	// reply arrives or ctx is cancelled. It returns the raw reply text. When
	// nil, requestQuotaDecision falls back to the chat-session ask-user
	// transport. Tests inject a stub so quota decisions are deterministic.
	suspendQuota func(ctx context.Context, chatID, agentName, prompt string, options []string) (string, error)
}

// NewSingleAgentExecutor creates a SingleAgentExecutor for the given agent.
// llmClient may be nil; in that case a genai-backed client is created lazily
// for session title generation using the configured (or env) API key.
func NewSingleAgentExecutor(agent *agentspec.Agent, conf *config.Config, repo *dbmodels.SessionRepository, server *Server, llmClient llm.Client) *SingleAgentExecutor {
	var d dispatch.Dispatcher
	if server != nil {
		d = server.dispatcher
	}
	if d == nil {
		d = dispatch.NewLocal(conf)
	}
	return &SingleAgentExecutor{
		agent:      agent,
		conf:       conf,
		repo:       repo,
		server:     server,
		llmClient:  llmClient,
		dispatcher: d,
	}
}

// useWorktree reports whether this run happens in the chat's git worktree: the
// chat opted in and this agent offers worktrees (a peer of the chat that does
// not, runs in the plain directory).
func (e *SingleAgentExecutor) useWorktree(session *dbmodels.Session) bool {
	return e.worktreeChat(session) != ""
}

// worktreeChat is the chat that owns the worktree this run happens in, or ""
// when the run does not use one. A subagent of a worktree chat shares the
// worktree of the root chat of its spawn tree (recorded at spawn), so the
// owner is not necessarily the chat being run.
func (e *SingleAgentExecutor) worktreeChat(session *dbmodels.Session) string {
	if session == nil || !e.agent.Config.Worktree.Enabled {
		return ""
	}
	if session.WorktreeChatID != "" {
		return session.WorktreeChatID
	}
	if session.Worktree {
		return session.ChatID
	}
	return ""
}

// sharedChat is the root chat of the spawn tree whose scratch directory
// (/shared in the sandboxes) the run uses, or "" for none. Subagents always
// use it; a chat of a team agent (it may spawn) uses its own. Other chats keep
// their sandbox unchanged.
func (e *SingleAgentExecutor) sharedChat(session *dbmodels.Session) (string, error) {
	if session == nil || e.server == nil || e.server.repo == nil {
		return "", nil
	}
	if session.ParentChatID == "" {
		if strings.TrimSpace(e.agent.Config.Team) == "" {
			return "", nil
		}
		return session.ChatID, nil
	}
	return e.server.spawnRoot(session)
}

// checkSharedWorktree fails closed for a subagent whose recorded worktree this
// agent cannot honor now: the agent no longer offers worktrees, or its
// worktree root differs from the one the owner's worktree was created under
// (the runner derives the path from the agent's own root).
func (e *SingleAgentExecutor) checkSharedWorktree(session *dbmodels.Session) error {
	if session == nil || session.WorktreeChatID == "" {
		return nil
	}
	if !e.agent.Config.Worktree.Enabled {
		return fmt.Errorf("chat %s shares the git worktree of chat %s, but agent %s no longer offers worktrees; refusing to run in the main checkout", session.ChatID, session.WorktreeChatID, e.agent.Config.ID)
	}
	if session.WorktreeRoot != e.agent.Config.Worktree.Root {
		return fmt.Errorf("chat %s shares the git worktree of chat %s under root %q, but agent %s uses worktree root %q", session.ChatID, session.WorktreeChatID, session.WorktreeRoot, e.agent.Config.ID, e.agent.Config.Worktree.Root)
	}
	return nil
}

// subagentAncestors lists the chats above a subagent chat, parent first. A
// lookup that fails or loops is an error: running without the ancestors would
// deadlock a writable child against the lock its parent holds, and a silently
// wrong answer would hide that.
func (s *Server) subagentAncestors(session *dbmodels.Session) ([]string, error) {
	var out []string
	for cur := session; cur != nil && cur.ParentChatID != ""; {
		if len(out) >= maxAgentNestingDepth+1 {
			return nil, fmt.Errorf("subagent ancestry of chat %s is deeper than %d", session.ChatID, maxAgentNestingDepth)
		}
		out = append(out, cur.ParentChatID)
		parent, err := s.repo.GetSessionMeta(cur.ParentChatID)
		if err != nil {
			return nil, fmt.Errorf("read parent chat %s: %w", cur.ParentChatID, err)
		}
		cur = parent
	}
	return out, nil
}

// Execute handles the agent execution.
func (e *SingleAgentExecutor) Execute(ctx context.Context, params SingleAgentRunParams) (resp string, err error) {
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
		var metaErr error
		session, metaErr = e.repo.GetSessionMeta(chatID)
		if metaErr != nil {
			return "", fmt.Errorf("failed to get session: %w", metaErr)
		}
	}

	// A subagent of a worktree chat must work in that worktree. The agent's
	// config is hot-reloaded, so re-check here instead of falling back to the
	// main checkout.
	if err := e.checkSharedWorktree(session); err != nil {
		return "", err
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
	if rawRunDir == "" && e.useWorktree(session) {
		// A worktree is made from one concrete directory, derived the same way
		// the runner and the merge lock derive it.
		rawRunDir = e.agent.Config.DefaultRunDir()
	}
	if rawRunDir == "" && len(e.agent.Config.RunDirs) > 0 && e.agent.Config.RunDirs[0] != "" {
		rawRunDir = e.agent.Config.RunDirs[0]
	}

	normalizedRunDir := NormalizeSessionRunDir(rawRunDir, chatID)
	runDirOpt := optional.None[string]()
	if normalizedRunDir != "" {
		runDirOpt = optional.Some(normalizedRunDir)
	}

	modelOpt := resolveModel(params, session, e.agent.Config)
	// An existing session's stored model overrides an explicitly requested one
	// to keep the underlying CLI session valid. Surface the override instead of
	// silently ignoring the request, so it shows up when debugging a session.
	// A missing session has no stored model, so the check is skipped entirely.
	if session != nil {
		if stored := storedAgentModel(session, e.agent.Config); stored != "" && configuredModel(e.agent.Config, stored) {
			if requested := requestedModel(params); requested != "" && requested != stored {
				e.recordModelOverride(chatID, requested, stored)
			}
		}
	}

	// Validate run_dir allowlist and existence BEFORE any DB writes or title generation
	if runDirOpt.IsSome() {
		rd := runDirOpt.Unwrap()
		if rd != "" {
			if len(e.agent.Config.RunDirs) > 0 && !run.IsAllowedDir(rd, e.agent.Config.RunDirs) {
				return "", fmt.Errorf("run directory %q is not allowed by agent configuration", rd)
			}
			// The directory lives on the runner, which creates and checks it;
			// the backend only checks its own filesystem for in-process runs.
			if _, local := e.dispatcher.(*dispatch.Local); local {
				baseTmp := GetSessionTmpBaseDir(chatID)
				baseSession := GetSessionScopedBaseDir("session", chatID)
				if (rd == baseTmp || strings.HasPrefix(rd, baseTmp+string(os.PathSeparator))) ||
					(rd == baseSession || strings.HasPrefix(rd, baseSession+string(os.PathSeparator))) {
					if mkdirErr := os.MkdirAll(rd, 0755); mkdirErr != nil {
						return "", fmt.Errorf("failed to create session temporary run_dir %q: %w", rd, mkdirErr)
					}
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
	}

	if e.repo != nil {
		agentIconOpt := dbmodels.SessionAgentIconOption(e.agent.Config.Icon)
		if preErr := e.repo.UpdateAgentSession(chatID, e.agent.Config.ID, "", "", runDirOpt, agentIconOpt); preErr != nil {
			return "", fmt.Errorf("failed to pre-update agent session: %w", preErr)
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
			if appendErr := e.repo.AppendMessage(chatID, userMsg); appendErr != nil {
				log.Error().Err(appendErr).Str("chat_id", chatID).Msg("failed to append incoming message to repo")
			} else if e.server != nil {
				e.server.PublishSessionEvent(chatID, SessionEvent{
					Type:    "message",
					Message: &userMsg,
				})
			}
		}

		// Only update status if this is the primary/entry agent for the session
		if session == nil || session.CurrentAgent == "" || session.CurrentAgent == e.agent.Config.Name || session.CurrentAgent == e.agent.Config.ID {
			if statusErr := e.repo.UpdateAgentStatus(chatID, e.agent.Config.ID, dbmodels.AgentStatusRunning, agentIconOpt); statusErr != nil {
				return "", fmt.Errorf("failed to update agent status to running: %w", statusErr)
			}
			// A chat that runs again is no longer failed or killed.
			if outErr := e.repo.SetSpawnOutcome(chatID, ""); outErr != nil {
				log.Warn().Err(outErr).Str("chat_id", chatID).Msg("failed to clear subagent outcome")
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

	// A worktree chat locks its own worktree directory instead of the shared
	// checkout, so it runs alongside chats working in the main checkout. The
	// path is derived exactly as the runner derives it when it creates it.
	lockDir := normalizedRunDir
	useWorktree := e.useWorktree(session)
	if useWorktree {
		if normalizedRunDir == "" {
			return "", errors.New("a worktree chat needs a run directory")
		}
		wtPath, wtErr := worktree.PathFor(e.agent.Config.Worktree.Root, normalizedRunDir, e.worktreeChat(session))
		if wtErr != nil {
			return "", fmt.Errorf("worktree for run directory %q: %w", normalizedRunDir, wtErr)
		}
		lockDir = wtPath
	}

	// A read-only subagent only reads the checkout (it is mounted read-only),
	// so it takes no lock: many of them run in parallel next to a writer. A
	// writable subagent locks like any chat, serializing with other writers; the
	// chats that spawned it do not block it, since they wait for it.
	var releaseLock func(bool)
	if e.server != nil && e.server.dirLocks != nil && (session == nil || !session.SpawnReadonly) {
		var ancestors []string
		if session != nil && session.ParentChatID != "" && e.server.repo != nil {
			var ancErr error
			if ancestors, ancErr = e.server.subagentAncestors(session); ancErr != nil {
				return "", ancErr
			}
		}
		var lockErr error
		releaseLock, lockErr = e.server.acquireChatDirLock(ctx, chatID, e.agent, lockDir, ancestors...)
		if lockErr != nil {
			return "", lockErr
		}
	}
	if releaseLock != nil {
		defer func() {
			keepUncommitted := false
			if !e.agent.Config.IsReadOnly() {
				hasQueued := false
				if err == nil && e.repo != nil {
					if queued, qErr := e.repo.GetQueuedMessages(chatID); qErr == nil && len(queued) > 0 {
						hasQueued = true
					}
				}
				// A worktree is private to the chat: nobody else waits on its
				// uncommitted changes, and the main checkout's are not ours.
				// A subagent never parks its lock for uncommitted changes: they
				// belong to the work of the chat that spawned it.
				isChild := session != nil && session.ParentChatID != ""
				if hasQueued || (!useWorktree && !isChild && e.server != nil && e.server.hasUncommittedChanges(chatID, normalizedRunDir)) {
					keepUncommitted = true
				}
			}
			releaseLock(keepUncommitted)
		}()
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
	defer func() {
		e.markAgentCompleted(ctx, chatID, err)
	}()
	augmentedPrompt := formatPromptWithAttachments(prompt, params.Attachments)
	resp, err = e.executeSequential(ctx, augmentedPrompt, chatID, runDirOpt, modelOpt, sessionMode, session, statusCh)
	return resp, err
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
		if server != nil && server.dirLocks != nil {
			server.dirLocks.UpdateChatTitle(chatID, title)
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

// markAgentCompleted sets the agent status to completed and broadcasts done/isRunning=false.
// If err == nil and there are still queued messages in repo, it skips marking completed
// and skips broadcasting done/isRunning=false to avoid status flickering between queued tasks.
func (e *SingleAgentExecutor) markAgentCompleted(ctx context.Context, chatID string, runErr error) {
	if runErr == nil && e.repo != nil {
		if queued, err := e.repo.GetQueuedMessages(chatID); err == nil && len(queued) > 0 {
			return
		}
	}

	if e.repo != nil {
		if err := e.repo.UpdateAgentStatus(chatID, e.agent.Config.ID, dbmodels.AgentStatusCompleted); err != nil {
			log.Error().Err(err).Str("chat_id", chatID).Str("agent", e.agent.Config.ID).Msg("failed to mark agent status completed after run")
		}
	}
	// A nested call-peer run ends while the caller still holds the chat: the
	// chat is not done, so no done/isRunning=false is announced for it.
	if e.server != nil && !isNestedPeerRun(ctx) {
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
	// A runner splits more than 1000 artifacts across events, so an event may
	// carry artifacts and no content.
	hasArtifacts := update.ArtifactsKnown && len(update.Artifacts) > 0
	if repo == nil || (update.Content == "" && !hasArtifacts) || update.EntryType == "agent_response" {
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
	if update.ArtifactsKnown {
		// Computed on the runner, which owns the files.
		artifactFiles = append(artifactFiles, update.Artifacts...)
	} else {
		for _, a := range agents.StatusArtifacts(chatID, targetFiles, agentConfig, workspaceDir) {
			artifactFiles = append(artifactFiles, a.ViewerPath)
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
	if update.Content == "" {
		return
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
	model, _ := update.Metadata["model"].(string)
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
		Model:         model,
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

// storedAgentModel returns the model persisted for the given agent config in a
// session, matched by the agent's name or ID.
func storedAgentModel(session *dbmodels.Session, cfg agentspec.AgentConfig) string {
	for _, a := range session.Agents {
		if a.Name == cfg.Name || a.Name == cfg.ID {
			return a.Model
		}
	}
	return ""
}

// configuredModel reports whether model is one of the agent's configured CLI
// targets.
func configuredModel(cfg agentspec.AgentConfig, model string) bool {
	for _, t := range cfg.CLI {
		if t.Model == model {
			return true
		}
	}
	return false
}

// requestedModel returns the model explicitly requested for this run: the
// run parameter first, then metadata.
func requestedModel(params SingleAgentRunParams) string {
	if params.Model != "" {
		return params.Model
	}
	if params.Metadata != nil {
		if m, ok := params.Metadata["model"].(string); ok {
			return m
		}
	}
	return ""
}

// recordModelOverride appends and broadcasts an activity marker when a valid
// stored session model overrides an explicitly requested model.
func (e *SingleAgentExecutor) recordModelOverride(chatID, requested, stored string) {
	log.Info().
		Str("chat_id", chatID).
		Str("agent", e.agent.Config.ID).
		Str("requested_model", requested).
		Str("effective_model", stored).
		Msg("requested model ignored; session keeps stored model")
	if e.repo == nil {
		return
	}
	msg := dbmodels.ChatMessage{
		ID:           fmt.Sprintf("model-override-%s-%s", chatID, uuid.NewV7().String()),
		Role:         "activity",
		ActivityType: "MODEL_OVERRIDE",
		Content:      fmt.Sprintf("requested model %q ignored; session pinned to %q", requested, stored),
		AgentName:    e.agent.Config.Name,
		Model:        stored,
		Timestamp:    time.Now().UnixMilli(),
	}
	if err := e.repo.AppendMessage(chatID, msg); err != nil {
		log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append model override activity")
		return
	}
	if e.server != nil {
		e.server.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &msg})
	}
}

// resolveModel determines the model to run. When an existing session already has
// a valid model configured and used for this agent, that model is preserved to
// maintain conversation context and avoid cross-CLI session invalidation.
// Otherwise, an explicit request (the run parameter, then metadata) is used.
// The stored model is only honoured while it is still configured: a model removed
// or renamed in config must not pin the session to a target run.RunWithCandidates
// would reject as unknown (a hard error the quota-decision loop cannot recover from).
// Other agents' stored models and assistant-message models are never inherited.
func resolveModel(params SingleAgentRunParams, session *dbmodels.Session, cfg agentspec.AgentConfig) optional.Option[string] {
	if session != nil {
		if m := storedAgentModel(session, cfg); m != "" && configuredModel(cfg, m) {
			return optional.Some(m)
		}
	}
	if params.Model != "" {
		return optional.Some(params.Model)
	}
	if params.Metadata != nil {
		if m, ok := params.Metadata["model"].(string); ok && m != "" {
			return optional.Some(m)
		}
	}
	return optional.None[string]()
}
