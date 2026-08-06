package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/google/uuid"
	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agents/run"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

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
			// Save status update to session DB if content is present and not agent_response
			if e.repo != nil && update.Content != "" && update.EntryType != "agent_response" {
				role := update.EntryType
				if role == "" || role == "other" {
					role = "activity"
				}
				agentName := e.agent.Config.Name
				if name, ok := update.Metadata["agent_name"].(string); ok && name != "" {
					agentName = name
				}
				if err := e.repo.AppendMessage(chatID, dbmodels.ChatMessage{
					ID:           fmt.Sprintf("step-%s-%d", chatID, update.StepIndex),
					Role:         role,
					Content:      update.Content,
					AgentName:    agentName,
					Timestamp:    time.Now().UnixMilli(),
					ActivityType: strings.ToUpper(role),
					StepIndex:    update.StepIndex,
				}); err != nil {
					log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append step status message to repo")
				}
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
			if err := e.repo.UpdateAgentStatus(chatID, e.agent.Config.Name, dbmodels.AgentStatusCompleted); err != nil {
				log.Error().Err(err).Str("chat_id", chatID).Msg("failed to update agent status to completed in repo")
			}
		}
		// Save final assistant response to DB session
		if respText != "" {
			if err := e.repo.AppendMessage(chatID, dbmodels.ChatMessage{
				ID:          fmt.Sprintf("assistant-%s-%s", chatID, uuid.Must(uuid.NewV7()).String()),
				Role:        "assistant",
				Content:     respText,
				AgentName:   e.agent.Config.Name,
				Timestamp:   time.Now().UnixMilli(),
				InputTokens: inputTokens,
				MaxTokens:   maxTokens,
			}); err != nil {
				log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append final assistant response to repo")
			}
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
