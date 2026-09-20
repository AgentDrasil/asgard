package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// seqRunResult carries the output of a sequential run.Run call.
type seqRunResult struct {
	out    []byte
	target agentspec.CLITarget
	err    error
}

type agentRunError struct {
	Err error
}

func (e *agentRunError) Error() string { return e.Err.Error() }
func (e *agentRunError) Unwrap() error { return e.Err }

// quotaAskMessageID derives the ask_user MessageID for a single-agent quota
// suspension. The sequence suffix keeps re-suspensions (after a "wait" reply
// that found quota still exhausted) distinct from the first prompt.
func quotaAskMessageID(chatID, agentID string, seq int) string {
	id := fmt.Sprintf("%s%s-%s", dbmodels.QuotaAskMessagePrefix, chatID, agentID)
	if seq > 1 {
		return fmt.Sprintf("%s-%d", id, seq)
	}
	return id
}

// cliForModel returns the CLI of the configured target carrying model, or ""
// when model is empty or not configured for the agent.
func cliForModel(cfg agentspec.AgentConfig, model string) string {
	if model == "" {
		return ""
	}
	for _, t := range cfg.CLI {
		if t.Model == model {
			return t.CLI
		}
	}
	return ""
}

// targetsForCLI filters the configured CLI targets down to those running on
// cli. An empty cli yields nil so the agent's full list stays in use.
func targetsForCLI(cfg agentspec.AgentConfig, cli string) []agentspec.CLITarget {
	if cli == "" {
		return nil
	}
	var out []agentspec.CLITarget
	for _, t := range cfg.CLI {
		if t.CLI == cli {
			out = append(out, t)
		}
	}
	return out
}

// executeSequential runs the first available CLI target (by quota) and streams results.
// When no target has usable quota it asks the user for a decision (reusing the
// workflow quota decision surface: wait for recovery, force a specific target,
// or cancel) and retries accordingly.
func (e *SingleAgentExecutor) executeSequential(
	ctx context.Context,
	prompt string,
	chatID string,
	runDirOpt optional.Option[string],
	modelOpt optional.Option[string],
	sessionMode string,
	session *dbmodels.Session,
	statusCh <-chan AgentStatusUpdate,
) (string, error) {
	// Collect the per-CLI session IDs previously opened by this agent.
	// Automatic target selection may switch CLIs between messages (quota
	// recovery), and a session ID is only resumable by the CLI that created
	// it, so run.Run resolves the session after the target is chosen.
	sessions := run.SessionMap{}
	if sessionMode != "fresh" && e.repo != nil && session != nil {
		for _, dbAgent := range session.Agents {
			if dbAgent.Name == e.agent.Config.Name || dbAgent.Name == e.agent.Config.ID {
				for cliKey, sid := range dbAgent.Sessions {
					if sid != "" {
						sessions[cliKey] = sid
					}
				}
				break
			}
		}
	}

	allowCrossSession := false
	if session != nil {
		allowCrossSession = session.AllowCrossSession
	}

	// forcedModel pins the target across retries: modelOpt by default, or the
	// model the user explicitly picked after a quota suspension.
	forcedModel := modelOpt
	suspended := false
	quotaSeq := 0

	// Determine the target CLI to prevent cross-CLI fallback.
	// Memory/sessions are not portable across different CLIs (e.g. AGY -> Simplest).
	targetCLI := cliForModel(e.agent.Config, forcedModel.TakeOr(""))
	if targetCLI == "" && session != nil {
		targetCLI = cliForModel(e.agent.Config, storedAgentModel(session, e.agent.Config))
	}
	if targetCLI == "" && len(e.agent.Config.CLI) > 0 {
		targetCLI = e.agent.Config.CLI[0].CLI
	}
	cliCandidates := targetsForCLI(e.agent.Config, targetCLI)

	for {
		// Reset back to Running only when returning from a suspension so the
		// initial Running status written by Execute is not churned.
		if suspended {
			e.setAgentRunning(chatID)
		}

		runToken := uuid.NewV7().String()
		resultCh := make(chan seqRunResult, 1)
		go func(mOpt optional.Option[string]) {
			out, target, err := run.RunWithCandidates(ctx, e.agent, cliCandidates, prompt, sessions, runDirOpt, mOpt, chatID, run.StatusScope{RunToken: runToken, AllowCrossSession: allowCrossSession}, e.conf)
			resultCh <- seqRunResult{out: out, target: target, err: err}
		}(forcedModel)

		result, err := e.drainUntilResult(ctx, chatID, runDirOpt, statusCh, resultCh)
		if err != nil {
			return "", err
		}

		if result.err == nil {
			return e.handleFinalResult(result.out, result.target, chatID, runDirOpt, forcedModel, sessionMode)
		}

		var nq *run.NoQuotaError
		if !errors.As(result.err, &nq) {
			return "", &agentRunError{Err: result.err}
		}

		// Quota exhausted. Without any way to reach the user, fail fast with
		// the informative NoQuotaError instead of blocking forever.
		if e.suspendQuota == nil && e.server == nil {
			return "", &agentRunError{Err: nq}
		}

		quotaSeq++
		reply, suspErr := e.requestQuotaDecision(
			ctx, chatID,
			workflow.BuildQuotaPrompt(nq, e.agent),
			workflow.QuotaOptions(nq),
			quotaSeq,
		)
		if suspErr != nil {
			return "", suspErr
		}

		decision, targetModel := workflow.ClassifyQuotaReply(reply, e.agent.Config.CLI)
		switch decision {
		case workflow.QuotaDecisionCancel:
			e.recordQuotaCancellation(chatID)
			return "", fmt.Errorf("execution cancelled by user")
		case workflow.QuotaDecisionTarget:
			forcedModel = optional.Some(targetModel)
			// A forced cross-CLI pick re-anchors the candidate list to the
			// chosen CLI: deliberate user intent, unlike automatic fallback.
			if cli := cliForModel(e.agent.Config, targetModel); cli != "" && cli != targetCLI {
				targetCLI = cli
				cliCandidates = targetsForCLI(e.agent.Config, targetCLI)
			}
		default:
			// Wait/continue: re-check quota with the original selection policy.
			forcedModel = modelOpt
		}
		suspended = true
	}
}

// requestQuotaDecision obtains the user's quota decision. A test- or
// host-injected suspendQuota wins; otherwise the chat-session transport is
// used.
func (e *SingleAgentExecutor) requestQuotaDecision(ctx context.Context, chatID, prompt string, options []string, seq int) (string, error) {
	if e.suspendQuota != nil {
		return e.suspendQuota(ctx, chatID, e.agent.Config.Name, prompt, options)
	}
	return e.askUserQuota(ctx, chatID, prompt, options, seq)
}

// askUserQuota delivers a quota-decision prompt through the chat session and
// blocks until the user replies or ctx is cancelled. The prompt is appended
// with the same "Options: a / b" convention the workflow quota surface uses,
// so the WebUI renders option buttons automatically.
func (e *SingleAgentExecutor) askUserQuota(ctx context.Context, chatID, prompt string, options []string, seq int) (string, error) {
	text := prompt
	if len(options) > 0 {
		text = text + "\n\nOptions: " + strings.Join(options, " / ")
	}

	e.setAgentWaitingHuman(chatID)

	msgID := quotaAskMessageID(chatID, e.agent.Config.ID, seq)
	askMsg := dbmodels.ChatMessage{
		ID:        msgID,
		Role:      "ask_user",
		Content:   text,
		AgentName: e.agent.Config.Name,
		Timestamp: time.Now().UnixMilli(),
	}
	if e.repo != nil {
		if appendErr := e.repo.AppendMessage(chatID, askMsg); appendErr != nil {
			log.Error().Err(appendErr).Str("chat_id", chatID).Msg("failed to append quota ask_user message")
		}
	}
	if e.server != nil {
		e.server.PublishSessionEvent(chatID, SessionEvent{Type: "message", Message: &askMsg})
		e.server.SendPushNotification(chatID, text, e.agent.Config.Name)
	}

	replyCh, unregister := RegisterAskWaiter(chatID, msgID)
	defer unregister()

	select {
	case reply := <-replyCh:
		return reply, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// setAgentRunning marks the agent running and broadcasts the status transition.
func (e *SingleAgentExecutor) setAgentRunning(chatID string) {
	if e.repo != nil {
		_ = e.repo.UpdateAgentStatus(chatID, e.agent.Config.ID, dbmodels.AgentStatusRunning)
	}
	if e.server != nil {
		e.server.PublishSessionEvent(chatID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"agent": e.agent.Config.ID, "isRunning": true},
		})
	}
}

// setAgentWaitingHuman marks the agent as parked on a quota decision.
func (e *SingleAgentExecutor) setAgentWaitingHuman(chatID string) {
	if e.repo != nil {
		_ = e.repo.UpdateAgentStatus(chatID, e.agent.Config.ID, dbmodels.AgentStatusWaitingHuman)
	}
	if e.server != nil {
		e.server.PublishSessionEvent(chatID, SessionEvent{
			Type:    "status",
			Payload: map[string]any{"agent": e.agent.Config.ID, "isRunning": false},
		})
	}
}

// recordQuotaCancellation appends and broadcasts a CANCELLED activity marker.
func (e *SingleAgentExecutor) recordQuotaCancellation(chatID string) {
	if e.repo == nil {
		return
	}
	cancelMsg := dbmodels.ChatMessage{
		ID:           fmt.Sprintf("cancelled-%s-%s", chatID, uuid.NewV7().String()),
		Role:         "activity",
		ActivityType: "CANCELLED",
		Content:      "execution cancelled due to quota exhaustion",
		Timestamp:    time.Now().UnixMilli(),
	}
	if err := e.repo.AppendMessage(chatID, cancelMsg); err != nil {
		log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append quota cancellation activity")
		return
	}
	if e.server != nil {
		e.server.PublishSessionEvent(chatID, SessionEvent{Type: "message", Message: &cancelMsg})
	}
}

// drainUntilResult drains status events from statusCh until resultCh delivers the final output.
func (e *SingleAgentExecutor) drainUntilResult(
	ctx context.Context,
	chatID string,
	runDirOpt optional.Option[string],
	statusCh <-chan AgentStatusUpdate,
	resultCh <-chan seqRunResult,
) (seqRunResult, error) {
	workspaceDir := ""
	if runDirOpt.IsSome() {
		workspaceDir = runDirOpt.Unwrap()
	}

	for {
		if statusCh == nil {
			select {
			case result := <-resultCh:
				return result, nil
			case <-ctx.Done():
				return seqRunResult{}, ctx.Err()
			}
		}

		select {
		case update, ok := <-statusCh:
			if !ok {
				statusCh = nil
				continue
			}
			recordStatusUpdate(e.server, e.repo, chatID, update, &e.agent.Config, workspaceDir)

		case result := <-resultCh:
			if statusCh != nil {
				draining := true
				for draining {
					select {
					case update, ok := <-statusCh:
						if ok {
							recordStatusUpdate(e.server, e.repo, chatID, update, &e.agent.Config, workspaceDir)
						} else {
							draining = false
						}
					default:
						draining = false
					}
				}
			}
			return result, nil

		case <-ctx.Done():
			return seqRunResult{}, ctx.Err()
		}
	}
}

// handleFinalResult parses the agent output and records final message to DB.
// sessionMode controls whether the returned session ID is persisted to DB.
// The session is stored under the CLI that actually executed, so a later CLI
// switch (quota fallback) resumes only that CLI's own session.
func (e *SingleAgentExecutor) handleFinalResult(
	out []byte,
	target agentspec.CLITarget,
	chatID string,
	runDirOpt optional.Option[string],
	modelOpt optional.Option[string],
	sessionMode string,
) (string, error) {
	respText, sessionID, inputTokens, maxTokens := parseOutput(out)

	if maxTokens <= 0 {
		modelName := ""
		cliName := target.CLI
		if cliName == "" && len(e.agent.Config.CLI) > 0 {
			cliName = e.agent.Config.CLI[0].CLI
		}
		if modelOpt.IsSome() && modelOpt.Unwrap() != "" {
			modelName = modelOpt.Unwrap()
		} else if target.Model != "" {
			modelName = target.Model
		} else if len(e.agent.Config.CLI) > 0 && e.agent.Config.CLI[0].Model != "" {
			modelName = e.agent.Config.CLI[0].Model
		}
		if modelName != "" {
			maxTokens = agentwrapper.GetModelContextWindow(cliName, modelName)
		}
	}

	if e.repo != nil {
		// Always update runDir; only persist sessionID in resume mode, keyed
		// by the executing CLI so later runs on a different CLI never receive
		// a session ID they cannot resume.
		cliKey := ""
		persistSessionID := ""
		if sessionMode != "fresh" && sessionID != "" && target.CLI != "" {
			cliKey = target.CLI
			persistSessionID = sessionID
		}
		if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.ID, cliKey, persistSessionID, runDirOpt); err != nil {
			return "", fmt.Errorf("failed to update agent session: %w", err)
		}
		if target.Model != "" {
			if err := e.repo.UpdateAgentModel(chatID, e.agent.Config.ID, target.Model); err != nil {
				return "", fmt.Errorf("failed to update agent model: %w", err)
			}
		}

		// Save final assistant response to DB session
		if respText != "" {
			finalMsg := dbmodels.ChatMessage{
				ID:          fmt.Sprintf("assistant-%s-%s", chatID, uuid.NewV7().String()),
				Role:        "assistant",
				Content:     respText,
				AgentName:   e.agent.Config.Name,
				Timestamp:   time.Now().UnixMilli(),
				InputTokens: inputTokens,
				MaxTokens:   maxTokens,
				Model:       target.Model,
			}
			if err := e.repo.AppendMessage(chatID, finalMsg); err != nil {
				log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append final assistant response to repo")
			} else if e.server != nil {
				e.server.PublishSessionEvent(chatID, SessionEvent{
					Type:    "message",
					Message: &finalMsg,
				})
			}
		}
	}

	return respText, nil
}
