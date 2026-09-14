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

// executeSequential runs the first available CLI target (by quota) and streams results.
// If the selected target runs out of quota, it asks the user via ask_user whether to
// wait for quota recovery or match downwards starting from the current target.
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

	var candidates []agentspec.CLITarget
	currModelOpt := modelOpt

	for {
		runToken := uuid.NewV7().String()
		resultCh := make(chan seqRunResult, 1)
		go func(cands []agentspec.CLITarget, mOpt optional.Option[string]) {
			out, target, err := run.RunWithCandidates(ctx, e.agent, cands, prompt, sessions, runDirOpt, mOpt, chatID, run.StatusScope{RunToken: runToken, AllowCrossSession: allowCrossSession}, e.conf)
			resultCh <- seqRunResult{out: out, target: target, err: err}
		}(candidates, currModelOpt)

		result, err := e.drainUntilResult(ctx, chatID, runDirOpt, statusCh, resultCh)
		if err != nil {
			return "", err
		}

		if result.err == nil {
			return e.handleFinalResult(result.out, result.target, chatID, runDirOpt, currModelOpt, sessionMode)
		}

		var nq *run.NoQuotaError
		if !errors.As(result.err, &nq) {
			return "", &agentRunError{Err: result.err}
		}

		// Quota exhausted. Determine which target ran out of quota.
		targetModel := ""
		if currModelOpt.IsSome() {
			targetModel = currModelOpt.Unwrap()
		} else if nq.ExplicitModel != "" {
			targetModel = nq.ExplicitModel
		} else if result.target.Model != "" {
			targetModel = result.target.Model
		}

		targetIdx := -1
		for i, t := range e.agent.Config.CLI {
			if targetModel != "" && t.Model == targetModel {
				targetIdx = i
				break
			}
		}

		var downwardsCandidates []agentspec.CLITarget
		if targetIdx >= 0 && targetIdx+1 < len(e.agent.Config.CLI) {
			downwardsCandidates = e.agent.Config.CLI[targetIdx+1:]
		}
		hasDownwards := len(downwardsCandidates) > 0

		var promptText string
		if targetModel != "" {
			promptText = fmt.Sprintf("模型 %s 配额不足，无法继续执行。", targetModel)
		} else {
			promptText = "当前模型配额不足，无法继续执行。"
		}

		if hasDownwards {
			promptText += "\n\n您可以等待配额恢复后重试，或从当前模型开始往下匹配其他可用模型，也可以取消执行。\n\nOptions: 等待配额恢复后重试 / 从当前模型往下匹配 / 取消执行"
		} else {
			promptText += "\n\n后续已无更多可用模型。您可以等待配额恢复后重试，或取消执行。\n\nOptions: 等待配额恢复后重试 / 取消执行"
		}

		if e.repo != nil {
			_ = e.repo.UpdateAgentStatus(chatID, e.agent.Config.ID, dbmodels.AgentStatusWaitingHuman)
		}
		if e.server != nil {
			e.server.PublishSessionEvent(chatID, SessionEvent{
				Type:    "status",
				Payload: map[string]any{"agent": e.agent.Config.ID, "isRunning": false},
			})
		}

		msgID := fmt.Sprintf("ask-%s", uuid.NewV7().String())
		askMsg := dbmodels.ChatMessage{
			ID:        msgID,
			Role:      "ask_user",
			Content:   promptText,
			AgentName: e.agent.Config.Name,
			Timestamp: time.Now().UnixMilli(),
			Model:     targetModel,
		}
		if e.repo != nil {
			if appendErr := e.repo.AppendMessage(chatID, askMsg); appendErr != nil {
				log.Error().Err(appendErr).Str("chat_id", chatID).Msg("failed to append ask_user message")
			}
		}
		if e.server != nil {
			e.server.PublishSessionEvent(chatID, SessionEvent{
				Type:    "message",
				Message: &askMsg,
			})
			e.server.SendPushNotification(chatID, promptText, e.agent.Config.Name)
		}

		replyCh, unregister := RegisterAskWaiter(chatID, msgID)
		var userReply string
		select {
		case reply := <-replyCh:
			userReply = reply
			unregister()
		case <-ctx.Done():
			unregister()
			return "", ctx.Err()
		}

		cleanReply := strings.TrimSpace(userReply)
		lowerReply := strings.ToLower(cleanReply)

		if strings.Contains(cleanReply, "取消") || strings.Contains(lowerReply, "cancel") || strings.Contains(lowerReply, "abort") {
			if e.repo != nil {
				cancelMsg := dbmodels.ChatMessage{
					ID:           fmt.Sprintf("cancelled-%s-%s", chatID, uuid.NewV7().String()),
					Role:         "activity",
					ActivityType: "CANCELLED",
					Content:      "execution cancelled due to quota exhaustion",
					Timestamp:    time.Now().UnixMilli(),
				}
				if appendErr := e.repo.AppendMessage(chatID, cancelMsg); appendErr == nil && e.server != nil {
					e.server.PublishSessionEvent(chatID, SessionEvent{
						Type:    "message",
						Message: &cancelMsg,
					})
				}
			}
			return "", fmt.Errorf("execution cancelled by user")
		}

		if hasDownwards && (strings.Contains(cleanReply, "往下") || strings.Contains(cleanReply, "匹配") || strings.Contains(lowerReply, "down") || strings.Contains(lowerReply, "next")) {
			candidates = downwardsCandidates
			currModelOpt = optional.None[string]()
		}

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
