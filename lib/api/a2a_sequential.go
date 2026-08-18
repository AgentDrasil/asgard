package api

import (
	"context"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/google/uuid"
	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agents/run"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
	"github.com/AgentDrasil/asgard/lib/workflow"
)

// seqRunResult carries the output of a sequential run.Run call.
type seqRunResult struct {
	out []byte
	err error
}

// executeSequential runs the first available CLI target (by quota) and streams results.
func (e *SingleAgentExecutor) executeSequential(
	ctx context.Context,
	yield func(a2a.Event, error) bool,
	execCtx *a2asrv.ExecutorContext,
	prompt string,
	chatID string,
	runDirOpt optional.Option[string],
	modelOpt optional.Option[string],
	sessionMode string,
	session *dbmodels.Session,
	statusCh <-chan AgentStatusUpdate,
) {
	// Resolve which session ID to resume (if any).
	agentSessionID := optional.None[string]()
	if sessionMode != "fresh" && e.repo != nil && session != nil {
		for _, dbAgent := range session.Agents {
			if dbAgent.Name == e.agent.Config.Name || dbAgent.Name == e.agent.Config.ID {
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
	runToken := uuid.Must(uuid.NewV7()).String()
	resultCh := make(chan seqRunResult, 1)
	go func() {
		out, err := run.Run(ctx, e.agent, prompt, agentSessionID, runDirOpt, modelOpt, chatID, run.StatusScope{RunToken: runToken}, e.conf)
		resultCh <- seqRunResult{out, err}
	}()

	e.streamAndFinish(ctx, yield, execCtx, chatID, runDirOpt, sessionMode, statusCh, resultCh)
}

// streamAndFinish drains status events from statusCh until resultCh delivers the final output.
func (e *SingleAgentExecutor) streamAndFinish(
	ctx context.Context,
	yield func(a2a.Event, error) bool,
	execCtx *a2asrv.ExecutorContext,
	chatID string,
	runDirOpt optional.Option[string],
	sessionMode string,
	statusCh <-chan AgentStatusUpdate,
	resultCh <-chan seqRunResult,
) {
	workspaceDir := ""
	if runDirOpt.IsSome() {
		workspaceDir = runDirOpt.Unwrap()
	}

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
			recordStatusUpdate(e.server, e.repo, chatID, update, &e.agent.Config, workspaceDir)

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
			updateMsg.Metadata = workflow.SanitizeMetadata(metadata)
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
func (e *SingleAgentExecutor) handleFinalResult(
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
		if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.ID, cliKey, persistSessionID, runDirOpt); err != nil {
			yield(nil, fmt.Errorf("failed to update agent session: %w", err))
			return
		}

		// Save final assistant response to DB session
		if respText != "" {
			finalMsg := dbmodels.ChatMessage{
				ID:          fmt.Sprintf("assistant-%s-%s", chatID, uuid.Must(uuid.NewV7()).String()),
				Role:        "assistant",
				Content:     respText,
				AgentName:   e.agent.Config.Name,
				Timestamp:   time.Now().UnixMilli(),
				InputTokens: inputTokens,
				MaxTokens:   maxTokens,
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

	respMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(respText))
	meta := map[string]any{
		"is_final": true,
	}
	if inputTokens > 0 || maxTokens > 0 {
		meta["input_tokens"] = inputTokens
		meta["max_tokens"] = maxTokens
	}
	respMsg.Metadata = workflow.SanitizeMetadata(meta)
	yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, respMsg), nil)
}
