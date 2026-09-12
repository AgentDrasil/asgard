package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"uuid"

	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// TriggerMessageRequest represents the payload for POST /api/agents/{id}/message.
type TriggerMessageRequest struct {
	Prompt            string `json:"prompt"`
	ChatID            string `json:"chatId,omitempty"`
	RunDir            string `json:"runDir,omitempty"`
	Model             string `json:"model,omitempty"`
	Wait              bool   `json:"wait,omitempty"`
	Headless          bool   `json:"-"`
	AllowCrossSession *bool  `json:"allowCrossSession,omitempty"`

	Metadata map[string]any `json:"metadata,omitempty"`

	Attachments []dbmodels.Attachment `json:"attachments,omitempty"`
}

// formatPromptWithAttachments formats a user prompt with attached files info for sandboxed agent execution.
// It enforces zero-trust validation:
// 1. Limits attachments to at most 20 entries.
// 2. Ignores any client-supplied Path.
// 3. Sanitizes Name via filepath.Base, length <= 255, and control character filtering.
// 4. Generates sandbox path strictly as /tmp/attachments/<safeName>.
func formatPromptWithAttachments(prompt string, attachments []dbmodels.Attachment) string {
	if len(attachments) == 0 {
		return prompt
	}

	maxAtts := 20
	if len(attachments) > maxAtts {
		attachments = attachments[:maxAtts]
	}

	type validAtt struct {
		name        string
		sandboxPath string
		size        int64
	}

	valid := make([]validAtt, 0, len(attachments))
	for _, att := range attachments {
		rawName := strings.TrimSpace(att.Name)
		if rawName == "" {
			continue
		}
		// Replace backslashes first for cross-platform safety
		rawName = strings.ReplaceAll(rawName, "\\", "/")
		base := filepath.Base(rawName)
		if base == "." || base == ".." || base == "/" || base != rawName {
			continue
		}
		if len(base) > 255 {
			continue
		}

		var sb strings.Builder
		hasControl := false
		for _, r := range base {
			if r < 32 || r == 127 || r == '/' || r == '\\' {
				hasControl = true
				break
			}
			sb.WriteRune(r)
		}
		if hasControl {
			continue
		}
		safeName := strings.TrimSpace(sb.String())
		if safeName == "" || safeName == "." || safeName == ".." {
			continue
		}

		sandboxPath := "/tmp/attachments/" + safeName
		valid = append(valid, validAtt{
			name:        safeName,
			sandboxPath: sandboxPath,
			size:        att.Size,
		})
	}

	if len(valid) == 0 {
		return prompt
	}

	var sb strings.Builder
	sb.WriteString(prompt)
	sb.WriteString("\n\n[Attached Files]\n")
	for _, att := range valid {
		fmt.Fprintf(&sb, "- %s (%s, %d bytes)\n", att.name, att.sandboxPath, att.size)
	}
	sb.WriteString("Please inspect and process these attachments directly from the sandbox filesystem.")

	return sb.String()
}

// handleTriggerMessage handles POST /api/agents/{id}/message, launching agent execution
// either asynchronously (202 Accepted) or synchronously (200 OK when wait=true).
func (s *Server) handleTriggerMessage(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		http.Error(w, `{"error":"agent id is required"}`, http.StatusBadRequest)
		return
	}

	var targetAgent *agentspec.Agent
	s.mu.RLock()
	for _, a := range s.agents {
		if a.Config.ID == agentID || a.Config.Name == agentID {
			targetAgent = a
			break
		}
	}
	s.mu.RUnlock()

	if targetAgent == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "agent not found"})
		return
	}

	var req TriggerMessageRequest
	if r.Body != nil && r.Body != http.NoBody {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body: `+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
	}

	if strings.TrimSpace(req.Prompt) == "" {
		http.Error(w, `{"error":"prompt is required"}`, http.StatusBadRequest)
		return
	}

	chatID := req.ChatID
	if chatID == "" {
		chatID = uuid.NewV7().String()
	} else if !IsValidChatID(chatID) {
		http.Error(w, `{"error":"invalid chatId format"}`, http.StatusBadRequest)
		return
	}

	wait := req.Wait || r.URL.Query().Get("wait") == "true"

	// 1. Workflow branch: acquire the guard; on conflict return 409, otherwise run
	// runWorkflow and keep the guard release
	if targetAgent.Config.Type == "workflow" {
		execCtx, handle, loaded := s.beginExecution(chatID, targetAgent.Config.ID, true)
		if loaded {
			http.Error(w, `{"error":"session is already running a task"}`, http.StatusConflict)
			return
		}

		runDirOpt := optional.None[string]()
		if req.RunDir != "" {
			runDirOpt = optional.Some(req.RunDir)
		}
		allowOpt := optional.None[bool]()
		if req.AllowCrossSession != nil {
			allowOpt = optional.Some(*req.AllowCrossSession)
		}
		if s.repo != nil {
			if err := s.repo.UpdateAgentSession(chatID, targetAgent.Config.ID, "", "", runDirOpt, allowOpt); err != nil {
				log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to update agent session on trigger message")
			}
		}

		if wait {
			defer s.releaseExecution(chatID)
			defer handle.cancel()
			status, output, err := s.runWorkflow(execCtx, targetAgent, chatID, req)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": status,
					"error":  err.Error(),
					"chatId": chatID,
				})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": status,
				"output": output,
				"chatId": chatID,
			})
			return
		}

		// Async workflow
		go func() {
			defer s.releaseExecution(chatID)
			defer handle.cancel()
			_, _, err := s.runWorkflow(execCtx, targetAgent, chatID, req)
			if err != nil {
				log.Error().Err(err).Str("chat_id", chatID).Str("agent", targetAgent.Config.ID).Msg("async workflow execution error")
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "accepted",
			"chatId": chatID,
		})
		return
	}

	// Try to acquire the guard
	execCtx, handle, loaded := s.beginExecution(chatID, targetAgent.Config.ID, false)

	// 2. Synchronous wait branch while running: keep the existing 409 contract
	if wait && loaded {
		http.Error(w, `{"error":"session is already running a task"}`, http.StatusConflict)
		return
	}

	// 3. Synchronous wait branch while idle: keep the existing sync path with guard release
	if wait && !loaded {
		defer s.releaseExecution(chatID)
		defer handle.cancel()
		runDirOpt := optional.None[string]()
		if req.RunDir != "" {
			runDirOpt = optional.Some(req.RunDir)
		}
		allowOpt := optional.None[bool]()
		if req.AllowCrossSession != nil {
			allowOpt = optional.Some(*req.AllowCrossSession)
		}
		if s.repo != nil {
			if err := s.repo.UpdateAgentSession(chatID, targetAgent.Config.ID, "", "", runDirOpt, allowOpt); err != nil {
				log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to update agent session on trigger message")
			}
		}
		status, output, err := s.runSingleAgent(execCtx, targetAgent, chatID, req)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": status,
				"error":  err.Error(),
				"chatId": chatID,
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": status,
			"output": output,
			"chatId": chatID,
		})
		return
	}

	// 4. Async single-agent enqueue branch: !wait && loaded
	if loaded {
		if len(req.Attachments) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "queued messages only support plain text; attachments are not allowed"})
			return
		}

		if s.repo != nil {
			existing, err := s.repo.GetQueuedMessages(chatID)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to check queue capacity: " + err.Error()})
				return
			}
			if len(existing) >= dbmodels.MaxQueuedMessagesPerSession {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "Queue limit reached (maximum 3 messages)"})
				return
			}

			qmsg, err := s.repo.EnqueueMessage(chatID, req.Prompt, req.Model)
			if err != nil {
				if errors.Is(err, dbmodels.ErrQueueFull) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Queue limit reached (maximum 3 messages)"})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to enqueue message: " + err.Error()})
				return
			}

			updatedQueue, _ := s.repo.GetQueuedMessages(chatID)
			if updatedQueue == nil {
				updatedQueue = []dbmodels.QueuedMessage{}
			}
			s.PublishSessionEvent(chatID, SessionEvent{
				Type:    EventTypeQueue,
				Payload: map[string]any{"queue": updatedQueue},
			})

			// Try to start the shared consumer (in case the previous one just finished)
			session, _ := s.repo.GetSession(chatID)
			runDir := req.RunDir
			if session != nil && session.RunDir != "" {
				runDir = session.RunDir
			}
			s.startQueueConsumerIfIdle(chatID, targetAgent, runDir)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":    "queued",
				"chatId":    chatID,
				"messageId": qmsg.ID,
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "database unavailable in degraded mode"})
		return
	}

	// 5. Async single-agent initial dispatch branch: !wait && !loaded
	// The guard lifecycle is fully handed over to runSingleAgentWithQueue (no defer here)
	runDirOpt := optional.None[string]()
	if req.RunDir != "" {
		runDirOpt = optional.Some(req.RunDir)
	}
	allowOpt := optional.None[bool]()
	if req.AllowCrossSession != nil {
		allowOpt = optional.Some(*req.AllowCrossSession)
	}
	if s.repo != nil {
		if err := s.repo.UpdateAgentSession(chatID, targetAgent.Config.ID, "", "", runDirOpt, allowOpt); err != nil {
			log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to update agent session on trigger message")
		}
	}

	go s.runSingleAgentWithQueue(execCtx, targetAgent, chatID, req)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "accepted",
		"chatId": chatID,
	})
}

// startQueueConsumerIfIdle attempts to atomically acquire the guard and start consumer loop
func (s *Server) startQueueConsumerIfIdle(chatID string, targetAgent *agentspec.Agent, runDir string) bool {
	execCtx, handle, loaded := s.beginExecution(chatID, targetAgent.Config.ID, false)
	if loaded {
		return false
	}
	go func() {
		defer handle.cancel()
		s.runQueueConsumerLoop(execCtx, targetAgent, chatID, runDir)
	}()
	return true
}

func (s *Server) executeSingleAgent(ctx context.Context, agent *agentspec.Agent, chatID string, req TriggerMessageRequest) (string, string, error) {
	if s.runSingleAgentFn != nil {
		return s.runSingleAgentFn(ctx, agent, chatID, req)
	}
	return s.runSingleAgent(ctx, agent, chatID, req)
}

// handleQueuedTaskError handles task execution errors for both the initial run and queued tasks.
// If the error is an agentRunError (sandbox error), subsequent queued messages are cleared and an error message is appended and published.
// If it is a non-sandbox failure (excluding cancellation/shutdown), a visible error message is appended and queued messages are preserved.
func (s *Server) handleQueuedTaskError(chatID string, targetAgent *agentspec.Agent, qErr error) {
	if s.repo == nil {
		return
	}
	var runErr *agentRunError
	if errors.As(qErr, &runErr) {
		// Core sandbox failure: clear the remaining queue and append an error notice
		_, _ = s.repo.ClearQueuedMessages(chatID)
		errMsg := dbmodels.ChatMessage{
			ID:        fmt.Sprintf("error-%s-%s", chatID, uuid.NewV7().String()),
			Role:      "error",
			Content:   fmt.Sprintf("task execution failed: %v. all queued messages for this session have been cleared.", qErr),
			AgentName: targetAgent.Config.Name,
			Timestamp: time.Now().UnixMilli(),
		}
		_ = s.repo.AppendMessage(chatID, errMsg)
		s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &errMsg})
		s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeQueue, Payload: map[string]any{"queue": []dbmodels.QueuedMessage{}}})
	} else if !errors.Is(qErr, context.Canceled) && !errors.Is(qErr, context.DeadlineExceeded) {
		// Non-sandbox pre-execution failure that is not a graceful shutdown:
		// append a visible error telling the user the message never ran; keep the queue
		errMsg := dbmodels.ChatMessage{
			ID:        fmt.Sprintf("error-%s-%s", chatID, uuid.NewV7().String()),
			Role:      "error",
			Content:   fmt.Sprintf("task scheduling failed (this message was not executed): %v", qErr),
			AgentName: targetAgent.Config.Name,
			Timestamp: time.Now().UnixMilli(),
		}
		_ = s.repo.AppendMessage(chatID, errMsg)
		s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &errMsg})
	}
}

func (s *Server) runSingleAgentWithQueue(ctx context.Context, targetAgent *agentspec.Agent, chatID string, req TriggerMessageRequest) {
	if handle := executionHandleFromContext(ctx); handle != nil {
		defer handle.cancel()
	}

	// Phase 1: execute the initial task
	_, _, qErr := s.executeSingleAgent(ctx, targetAgent, chatID, req)
	if qErr != nil {
		s.handleQueuedTaskError(chatID, targetAgent, qErr)
		s.releaseExecution(chatID)
		return
	}

	// Phase 2: initial task succeeded, enter the single consumer loop for queued tasks
	s.runQueueConsumerLoop(ctx, targetAgent, chatID, req.RunDir)
}

func (s *Server) runQueueConsumerLoop(ctx context.Context, targetAgent *agentspec.Agent, chatID string, runDir string) {
	for {
		if s.repo == nil {
			s.releaseExecution(chatID)
			break
		}

		// Key invariant: the Pop at the top of the loop always runs inside the
		// guard critical section
		nextMsg, popErr := s.repo.PopNextQueuedMessage(chatID)
		if popErr != nil {
			log.Error().Err(popErr).Msg("failed to pop next queued message")
			// Append a visible error but keep the remaining queue; release the
			// guard and exit
			errMsg := dbmodels.ChatMessage{
				ID:        fmt.Sprintf("error-%s-%s", chatID, uuid.NewV7().String()),
				Role:      "error",
				Content:   fmt.Sprintf("failed to fetch queued message: %v. the session is paused; queued messages are preserved and can be retried after restart.", popErr),
				AgentName: targetAgent.Config.Name,
				Timestamp: time.Now().UnixMilli(),
			}
			_ = s.repo.AppendMessage(chatID, errMsg)
			s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &errMsg})
			s.releaseExecution(chatID)
			break
		}
		if nextMsg == nil {
			// Release the guard and re-check non-destructively via Peek
			s.releaseExecution(chatID)
			head, perr := s.repo.PeekNextQueuedMessage(chatID)
			if perr != nil {
				log.Error().Err(perr).Msg("failed to peek next queued message")
				errMsg := dbmodels.ChatMessage{
					ID:        fmt.Sprintf("error-%s-%s", chatID, uuid.NewV7().String()),
					Role:      "error",
					Content:   fmt.Sprintf("failed to fetch queued message: %v. the session is paused; queued messages are preserved and can be retried after restart.", perr),
					AgentName: targetAgent.Config.Name,
					Timestamp: time.Now().UnixMilli(),
				}
				_ = s.repo.AppendMessage(chatID, errMsg)
				s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &errMsg})
				break
			}
			if head == nil {
				break // queue is truly drained; safe to exit
			}
			if _, loaded := s.activeExecutions.LoadOrStore(chatID, executionGuardValue(ctx)); loaded {
				break // a concurrent enqueuer already acquired the guard and started a new
				// consumer goroutine; head was not physically deleted and is picked up by
				// that goroutine — zero message loss
			}
			// Guard re-acquired: the destructive Pop is now safe
			nextMsg, _ = s.repo.PopNextQueuedMessage(chatID)
			if nextMsg == nil {
				// The queue head was just withdrawn by the user. This goroutine still
				// owns the guard — do NOT release it here! continue back to the loop top,
				// keeping the "Pop always runs under the guard" invariant and preventing
				// concurrent double-runs or deleting someone else's guard
				continue
			}
		}

		// Broadcast the latest queue snapshot after dequeue
		remaining, _ := s.repo.GetQueuedMessages(chatID)
		if remaining == nil {
			remaining = []dbmodels.QueuedMessage{}
		}
		s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeQueue, Payload: map[string]any{"queue": remaining}})

		// Execute the queued task
		_, _, qErr := s.executeSingleAgent(ctx, targetAgent, chatID, TriggerMessageRequest{Prompt: nextMsg.Prompt, Model: nextMsg.Model, RunDir: runDir})
		if qErr != nil {
			s.handleQueuedTaskError(chatID, targetAgent, qErr)
			s.releaseExecution(chatID)
			break
		}
	}
}

// runSingleAgent executes a single CLI agent synchronously, returning its final assistant text.
func (s *Server) runSingleAgent(ctx context.Context, agent *agentspec.Agent, chatID string, req TriggerMessageRequest) (status string, output string, err error) {
	exec := NewSingleAgentExecutor(agent, s.conf, s.repo, s, nil)
	out, err := exec.Execute(ctx, SingleAgentRunParams{
		ChatID:      chatID,
		Prompt:      req.Prompt,
		RunDir:      req.RunDir,
		Model:       req.Model,
		Metadata:    req.Metadata,
		Attachments: req.Attachments,
	})
	if err != nil {
		return "failed", "", err
	}
	return "completed", out, nil
}
