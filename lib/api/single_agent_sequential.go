package api

import (
	"context"
	"fmt"
	"time"

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

	return e.streamAndFinish(ctx, chatID, runDirOpt, sessionMode, statusCh, resultCh)
}

// streamAndFinish drains status events from statusCh until resultCh delivers the final output.
func (e *SingleAgentExecutor) streamAndFinish(
	ctx context.Context,
	chatID string,
	runDirOpt optional.Option[string],
	sessionMode string,
	statusCh <-chan AgentStatusUpdate,
	resultCh <-chan seqRunResult,
) (string, error) {
	workspaceDir := ""
	if runDirOpt.IsSome() {
		workspaceDir = runDirOpt.Unwrap()
	}

	for {
		if statusCh == nil {
			// No listener configured — just wait for result.
			select {
			case result := <-resultCh:
				if result.err != nil {
					return "", fmt.Errorf("failed to run agent: %w", result.err)
				}
				return e.handleFinalResult(result.out, chatID, runDirOpt, sessionMode)
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		select {
		case update, ok := <-statusCh:
			if !ok {
				// Channel closed unexpectedly; wait for result.
				statusCh = nil
				continue
			}
			recordStatusUpdate(e.server, e.repo, chatID, update, &e.agent.Config, workspaceDir)

		case result := <-resultCh:
			if result.err != nil {
				return "", fmt.Errorf("failed to run agent: %w", result.err)
			}
			return e.handleFinalResult(result.out, chatID, runDirOpt, sessionMode)

		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// handleFinalResult parses the agent output and records final message to DB.
// sessionMode controls whether the returned session ID is persisted to DB.
func (e *SingleAgentExecutor) handleFinalResult(
	out []byte,
	chatID string,
	runDirOpt optional.Option[string],
	sessionMode string,
) (string, error) {
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
			return "", fmt.Errorf("failed to update agent session: %w", err)
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

	return respText, nil
}
