package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
	"uuid"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

// executionHandle tracks one in-flight execution of a chat session so a user
// can cancel it mid-flight. It is the value stored in Server.activeExecutions.
type executionHandle struct {
	cancel context.CancelFunc
	// ctx is the execution context the cancel func ends; nested call-peer runs
	// derive from it so cancelling the caller cancels its peers. Nil for
	// guards of executions started elsewhere.
	ctx        context.Context
	agentID    string
	isWorkflow bool
	startTime  time.Time
	// stopRequested is set when the stop API targeted this execution.
	stopRequested bool
}

// executionHandleKey carries the active *executionHandle through the execution
// context so nested consumers (e.g. the queue consumer loop) can re-register
// the exact same handle when they temporarily release and re-acquire the guard.
type executionHandleKey struct{}

// executionHandleFromContext returns the execution handle attached to ctx, if any.
func executionHandleFromContext(ctx context.Context) *executionHandle {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(executionHandleKey{}).(*executionHandle)
	return h
}

// beginExecution creates a cancellable execution context and atomically
// registers it as the session's exclusive guard. When another execution already
// owns the guard it returns loaded=true with nil ctx/handle (the freshly
// created context is cancelled immediately so it cannot leak).
func (s *Server) beginExecution(chatID, agentID string, isWorkflow bool) (context.Context, *executionHandle, bool) {
	ctx, cancel := context.WithCancel(s.Context())
	handle := &executionHandle{
		cancel:     cancel,
		agentID:    agentID,
		isWorkflow: isWorkflow,
		startTime:  time.Now(),
	}
	handle.ctx = ctx // before publishing the handle, so readers never see nil
	if _, loaded := s.activeExecutions.LoadOrStore(chatID, handle); loaded {
		cancel()
		return nil, nil, true
	}
	// A stop's suppression of the finish push ended with the run it stopped.
	s.clearQuiet(chatID)
	s.unparkQueue(chatID)
	return context.WithValue(ctx, executionHandleKey{}, handle), handle, false
}

// ensureExecutionGuard registers a guard for an execution that is already
// running (e.g. a workflow started outside the request path). It never clobbers
// an existing handle, so cancellation stays wired to the original execution.
// The created handle carries no cancel func: workflow guards rely on
// Engine.CancelSession, single-agent ones on the owner goroutine's context.
func (s *Server) ensureExecutionGuard(chatID, agentID string, isWorkflow bool) {
	if chatID == "" {
		return
	}
	_, _ = s.activeExecutions.LoadOrStore(chatID, &executionHandle{
		agentID:    agentID,
		isWorkflow: isWorkflow,
		startTime:  time.Now(),
	})
}

// releaseExecution removes the session guard. It intentionally does not cancel
// the execution context: the owning goroutine cancels its own context exactly
// once when it truly finishes (the queue consumer releases and re-acquires the
// guard during its drain-boundary handshake). A pending stop marker is also
// consumed so later failures of this session are not misclassified.
func (s *Server) releaseExecution(chatID string) {
	s.activeExecutions.Delete(chatID)
	// The guard's holder is gone, so no call-peer chain of it can be live; an
	// abnormal teardown of a blocked root must not leave a stale chain.
	s.peerMu.Lock()
	delete(s.peerChains, chatID)
	s.peerMu.Unlock()
	s.consumeSessionStop(chatID)
	// Wake the subagent waiters of this chat, clear its waiting mark and tell
	// the chat that spawned it (if any) that it finished.
	// It runs off the caller's path like the drain below: releases sit on
	// latency-sensitive paths.
	epoch := s.bumpReleaseEpoch(chatID)
	go s.onExecutionReleasedAt(chatID, epoch)
	// Whoever releases last drains: messages queued meanwhile, or left behind
	// by a consumer that gave up on an unavailable runner, run now if possible.
	// It runs off the caller's path: releases are on latency-sensitive paths
	// (workflow suspension) that update session state right afterwards.
	if s.repo != nil {
		go s.drainQueueAfterRelease(chatID)
	}
}

// executionGuardValue returns the value to store when a running loop re-acquires
// the guard: the original handle when known, otherwise a legacy placeholder.
func executionGuardValue(ctx context.Context) any {
	if handle := executionHandleFromContext(ctx); handle != nil {
		return handle
	}
	return struct{}{}
}

// cancelWorkflowSession aborts the workflow runs of a session. A run registers
// its cancel func with the engine only after its setup, and the workflow
// executor deliberately detaches the run from the request context, so a stop
// that lands in that window would find nothing to cancel and the run would go
// on for good. When nothing was cancelled it therefore retries until a run was
// signalled, the guard that was active at the stop is released or replaced, or
// a generous deadline passes.
func (s *Server) cancelWorkflowSession(chatID string, guard any) {
	engine := s.workflowEngine
	if engine == nil || chatID == "" {
		return
	}
	if engine.CancelSession(chatID) > 0 {
		return
	}
	go func() {
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		deadline := time.NewTimer(30 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-deadline.C:
				return
			case <-t.C:
			}
			if cur, running := s.activeExecutions.Load(chatID); !running || cur != guard {
				return
			}
			if engine.CancelSession(chatID) > 0 {
				return
			}
		}
	}()
}

// sessionStopInProgress reports whether the session's execution was aborted
// via the stop API and the stop marker has not been consumed yet. It lets
// event handlers classify cancellation-induced failures precisely instead of
// pattern-matching error strings.
func (s *Server) sessionStopInProgress(chatID string) bool {
	if s == nil || chatID == "" {
		return false
	}
	if _, stopped := s.cancelledExecutions.Load(chatID); stopped {
		return true
	}
	if handle, ok := func() (*executionHandle, bool) {
		v, running := s.activeExecutions.Load(chatID)
		h, ok := v.(*executionHandle)
		return h, running && ok
	}(); ok && handle != nil && handle.stopRequested {
		return true
	}
	return false
}

// consumeSessionStop clears the stop marker once the aborted execution has
// fully settled, so later runs of the same session are not misclassified.
func (s *Server) consumeSessionStop(chatID string) {
	s.cancelledExecutions.Delete(chatID)
}

// stopSessionExecution cancels the in-flight execution of a session, clears any
// queued messages, records a user-visible cancellation message and broadcasts
// the resulting status to the frontend. It returns the HTTP status code to
// respond with plus an error message when the code is not 200.
//
// Stopping a chat also stops every subagent chat spawned below it. The chat
// itself may be idle while a descendant runs: that is a success as long as
// something was stopped.
func (s *Server) stopSessionExecution(sessionID string) (int, string) {
	if !IsValidChatID(sessionID) {
		return http.StatusBadRequest, "invalid session id"
	}
	// The cascade is one critical section with subagent spawns: a spawn either
	// started its child (guard taken, so the cascade sees it) before the stop
	// or runs after it and is refused because its parent is stopped.
	s.spawnMu.Lock()
	defer s.spawnMu.Unlock()
	// Mark the target even when it is idle: a late finish push of a subagent
	// must not restart a chat the user stopped.
	s.markStopped(sessionID)
	code, msg := s.stopOneExecution(sessionID)
	stopped := code == http.StatusOK
	if code == http.StatusConflict {
		s.dropQueueOfIdleStopTarget(sessionID)
	}
	descendants, derr := s.descendantChats(sessionID)
	for _, id := range descendants {
		if c, _ := s.stopDescendant(id); c == http.StatusOK {
			stopped = true
		}
	}
	if derr != nil {
		return http.StatusInternalServerError, fmt.Sprintf("stopped the chat but failed to enumerate its subagents, retry the stop: %v", derr)
	}
	if stopped {
		return http.StatusOK, ""
	}
	return code, msg
}

// dropQueueOfIdleStopTarget clears the queued messages of a stop target that
// was idle, as a stop of a running chat does. A queued head (a subagent's
// finish push or reply, or a message the queue consumer is about to run in its
// drain handshake) would otherwise run on after the stop, whether the queue is
// parked or not. A subagent target is recorded as killed.
func (s *Server) dropQueueOfIdleStopTarget(chatID string) {
	if s.repo == nil {
		return
	}
	head, err := s.repo.PeekNextQueuedMessage(chatID)
	if err != nil || head == nil {
		return
	}
	if _, err := s.repo.ClearQueuedMessages(chatID); err != nil {
		log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to clear queued messages of an idle stopped chat")
	}
	if sess, err := s.repo.GetSessionMeta(chatID); err == nil && sess != nil && sess.ParentChatID != "" {
		if err := s.repo.SetSpawnOutcome(chatID, dbmodels.SpawnOutcomeKilled); err != nil {
			log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to record killed outcome of an idle stopped subagent")
		}
	}
	s.PublishSessionEvent(chatID, SessionEvent{
		Type:    EventTypeQueue,
		Payload: map[string]any{"queue": []dbmodels.QueuedMessage{}},
	})
	// The child counted as live through its queued head, and no execution
	// release follows: let the parent's hold notice (spawnMu is held).
	s.syncParentHold(chatID)
}

// stopOneExecution is stopSessionExecution for a single chat, without the
// cascade to its subagents.
func (s *Server) stopOneExecution(sessionID string) (int, string) {
	if !IsValidChatID(sessionID) {
		return http.StatusBadRequest, "invalid session id"
	}

	value, running := s.activeExecutions.Load(sessionID)
	if !running {
		return http.StatusConflict, "session is not currently running"
	}

	// Persist the outcome before cancelling, so the run's own failure handling
	// (which sees the stop marker) never records "failed" over it.
	if s.repo != nil {
		if err := s.repo.SetSpawnOutcome(sessionID, dbmodels.SpawnOutcomeKilled); err != nil {
			log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to record killed outcome on stop")
		}
	}
	if handle, ok := value.(*executionHandle); ok {
		if handle.isWorkflow {
			s.cancelWorkflowSession(sessionID, value)
		}
		if handle.cancel != nil {
			handle.cancel()
		}
		handle.stopRequested = true
	}
	s.cancelledExecutions.Store(sessionID, time.Now())
	s.markStopped(sessionID)

	if s.repo != nil {
		if _, err := s.repo.ClearQueuedMessages(sessionID); err != nil {
			log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to clear queued messages on stop")
		}
		s.PublishSessionEvent(sessionID, SessionEvent{
			Type:    EventTypeQueue,
			Payload: map[string]any{"queue": []dbmodels.QueuedMessage{}},
		})

		msg := dbmodels.ChatMessage{
			ID:           fmt.Sprintf("cancelled-%s-%s", sessionID, uuid.NewV7().String()),
			Role:         "activity",
			ActivityType: "CANCELLED",
			Content:      "execution stopped by user",
			Timestamp:    time.Now().UnixMilli(),
		}
		if err := s.repo.AppendMessage(sessionID, msg); err != nil {
			log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to append cancellation message to repo")
		} else {
			s.PublishSessionEvent(sessionID, SessionEvent{Type: EventTypeMessage, Message: &msg})
		}
	}

	statusPayload := map[string]any{"isRunning": false}
	if handle, ok := value.(*executionHandle); ok {
		statusPayload["agent"] = handle.agentID
	}
	// Guards registered without a cancel func (workflow resume/redrive paths)
	// may carry no agentID; fall back to the session's current agent so the
	// frontend can still attribute the status change.
	if statusPayload["agent"] == "" && s.repo != nil {
		if sess, err := s.repo.GetSessionMeta(sessionID); err == nil && sess != nil && sess.CurrentAgent != "" {
			statusPayload["agent"] = sess.CurrentAgent
		}
	}
	s.PublishSessionEvent(sessionID, SessionEvent{Type: EventTypeStatus, Payload: statusPayload})
	s.PublishSessionEvent(sessionID, SessionEvent{Type: EventTypeDone, Payload: map[string]any{}})

	return http.StatusOK, ""
}

// handleStopSession handles POST /api/sessions/{id}/stop.
func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if _, aerr := s.authorizeSession(r, sessionID); aerr != nil {
		writeJSONError(w, aerr.status, aerr.msg)
		return
	}
	code, message := s.stopSessionExecution(sessionID)
	if code != http.StatusOK {
		writeJSONError(w, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "stopped",
		"chatId": sessionID,
	})
}

// handleStopWorkflowRun handles POST /api/workflows/{runID}/stop. It resolves
// the session owning the run and delegates to the shared stop path.
func (s *Server) handleStopWorkflowRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if runID == "" {
		writeJSONError(w, http.StatusBadRequest, "run id is required")
		return
	}
	if s.workflowRunRepo == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "workflow runs are not available")
		return
	}

	row, err := s.workflowRunRepo.GetRunRow(runID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to query workflow run: "+err.Error())
		return
	}
	if row == nil {
		writeJSONError(w, http.StatusNotFound, "workflow run not found")
		return
	}
	if _, aerr := s.authorizeSession(r, row.SessionID); aerr != nil {
		writeJSONError(w, aerr.status, aerr.msg)
		return
	}
	// The execution guard is per-session, not per-run: a run that just settled
	// must not drag an unrelated follow-up execution of the same session into
	// cancellation. Only an unfinished run may stop its session.
	if row.Status != dbmodels.WorkflowStatusRunning && row.Status != dbmodels.WorkflowStatusWaitingHuman {
		writeJSONError(w, http.StatusConflict, fmt.Sprintf("workflow run status is %s; only unfinished runs can be stopped", row.Status))
		return
	}

	code, message := s.stopSessionExecution(row.SessionID)
	if code != http.StatusOK {
		writeJSONError(w, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "stopped",
		"runId":  runID,
		"chatId": row.SessionID,
	})
}
