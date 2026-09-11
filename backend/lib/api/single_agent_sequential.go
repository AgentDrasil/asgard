package api

import (
	"context"
	"fmt"
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

	// ── Run the agent in a goroutine, collect result on resultCh ──────────
	runToken := uuid.NewV7().String()
	resultCh := make(chan seqRunResult, 1)
	go func() {
		out, target, err := run.Run(ctx, e.agent, prompt, sessions, runDirOpt, modelOpt, chatID, run.StatusScope{RunToken: runToken}, e.conf)
		resultCh <- seqRunResult{out: out, target: target, err: err}
	}()

	return e.streamAndFinish(ctx, chatID, runDirOpt, modelOpt, sessionMode, statusCh, resultCh)
}

// streamAndFinish drains status events from statusCh until resultCh delivers the final output.
func (e *SingleAgentExecutor) streamAndFinish(
	ctx context.Context,
	chatID string,
	runDirOpt optional.Option[string],
	modelOpt optional.Option[string],
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
					return "", &agentRunError{Err: result.err}
				}
				return e.handleFinalResult(result.out, result.target, chatID, runDirOpt, modelOpt, sessionMode)
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
				return "", &agentRunError{Err: result.err}
			}
			return e.handleFinalResult(result.out, result.target, chatID, runDirOpt, modelOpt, sessionMode)

		case <-ctx.Done():
			return "", ctx.Err()
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
