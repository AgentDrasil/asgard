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

// executeParallel runs ALL CLI targets concurrently and combines their results into a single response.
func (e *agentExecutor) executeParallel(
	ctx context.Context,
	yield func(a2a.Event, error) bool,
	execCtx *a2asrv.ExecutorContext,
	prompt string,
	chatID string,
	runDirOpt optional.Option[string],
	sessionMode string,
	statusCh <-chan AgentStatusUpdate,
) {
	// Resolve sessions map for resume mode.
	sessions := map[string]string{}
	if sessionMode != "fresh" && e.repo != nil {
		if s, err := e.repo.GetAgentSessions(chatID, e.agent.Config.Name); err == nil && s != nil {
			sessions = s
		}
	}

	// ── Run all targets concurrently ──────────────────────────────────────
	type runAllResult struct {
		results []run.RunResult
	}
	resultCh := make(chan runAllResult, 1)
	go func() {
		results := run.RunAll(ctx, e.agent, prompt, sessions, runDirOpt, chatID, e.conf)
		resultCh <- runAllResult{results}
	}()

	// Drain status events while waiting for all targets to finish.
	for {
		if statusCh == nil {
			result := <-resultCh
			e.handleParallelResult(yield, execCtx, result.results, chatID, runDirOpt, sessionMode)
			return
		}

		select {
		case update, ok := <-statusCh:
			if !ok {
				statusCh = nil
				continue
			}
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
					log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append parallel step message to repo")
				}
			}
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
			e.handleParallelResult(yield, execCtx, result.results, chatID, runDirOpt, sessionMode)
			return

		case <-ctx.Done():
			if e.repo != nil {
				_ = e.repo.UpdateAgentStatus(chatID, e.agent.Config.Name, dbmodels.AgentStatusCompleted)
			}
			yield(nil, ctx.Err())
			return
		}
	}
}

// handleParallelResult combines all RunResult outputs into a single final message.
// Session IDs are persisted per-target (keyed by CLIKey) unless sessionMode is "fresh".
func (e *agentExecutor) handleParallelResult(
	yield func(a2a.Event, error) bool,
	execCtx *a2asrv.ExecutorContext,
	results []run.RunResult,
	chatID string,
	runDirOpt optional.Option[string],
	sessionMode string,
) {
	var combined strings.Builder
	for _, r := range results {
		lastContent, sessionID, _, _ := parseOutput(r.Output)

		// Persist session ID per CLI target (resume mode only).
		if e.repo != nil && sessionMode != "fresh" && sessionID != "" && r.CLIKey != "" {
			if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.Name, r.CLIKey, sessionID, runDirOpt); err != nil {
				log.Warn().Err(err).Str("cliKey", r.CLIKey).Msg("failed to update agent session for parallel target")
			}
		} else if e.repo != nil && combined.Len() == 0 {
			// Still update runDir on the first target even in fresh mode.
			if err := e.repo.UpdateAgentSession(chatID, e.agent.Config.Name, "", "", runDirOpt); err != nil {
				log.Error().Err(err).Str("chat_id", chatID).Msg("failed to update agent session runDir in parallel mode")
			}
		}

		if combined.Len() > 0 {
			combined.WriteString("\n\n")
		}
		if r.Err != nil {
			fmt.Fprintf(&combined, "--- %s (error) ---\n%s", r.CLIKey, r.Err.Error())
		} else {
			fmt.Fprintf(&combined, "--- %s ---\n%s", r.CLIKey, lastContent)
		}
	}

	respText := combined.String()

	if e.repo != nil {
		if err := e.repo.UpdateAgentStatus(chatID, e.agent.Config.Name, dbmodels.AgentStatusCompleted); err != nil {
			log.Error().Err(err).Str("chat_id", chatID).Msg("failed to update agent status to completed in repo")
		}
		if respText != "" {
			if err := e.repo.AppendMessage(chatID, dbmodels.ChatMessage{
				ID:        fmt.Sprintf("assistant-%s-%s", chatID, uuid.Must(uuid.NewV7()).String()),
				Role:      "assistant",
				Content:   respText,
				AgentName: e.agent.Config.Name,
				Timestamp: time.Now().UnixMilli(),
			}); err != nil {
				log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append parallel assistant response to repo")
			}
		}
	}

	respMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(respText))
	yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, respMsg), nil)
}
