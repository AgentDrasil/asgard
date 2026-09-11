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
	cancel     context.CancelFunc
	agentID    string
	isWorkflow bool
	startTime  time.Time
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
	if _, loaded := s.activeExecutions.LoadOrStore(chatID, handle); loaded {
		cancel()
		return nil, nil, true
	}
	return context.WithValue(ctx, executionHandleKey{}, handle), handle, false
}

// ensureExecutionGuard registers a guard for an execution that is already
// running (e.g. a workflow started outside the request path). It never clobbers
// an existing handle, so cancellation stays wired to the original execution.
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
// guard during its drain-boundary handshake).
func (s *Server) releaseExecution(chatID string) {
	s.activeExecutions.Delete(chatID)
}

// executionGuardValue returns the value to store when a running loop re-acquires
// the guard: the original handle when known, otherwise a legacy placeholder.
func executionGuardValue(ctx context.Context) any {
	if handle := executionHandleFromContext(ctx); handle != nil {
		return handle
	}
	return struct{}{}
}

// stopSessionExecution cancels the in-flight execution of a session, clears any
// queued messages, records a user-visible cancellation message and broadcasts
// the resulting status to the frontend. It returns the HTTP status code to
// respond with plus an error message when the code is not 200.
func (s *Server) stopSessionExecution(sessionID string) (int, string) {
	if !IsValidChatID(sessionID) {
		return http.StatusBadRequest, "invalid session id"
	}

	value, running := s.activeExecutions.Load(sessionID)
	if !running {
		return http.StatusConflict, "session is not currently running"
	}

	if handle, ok := value.(*executionHandle); ok {
		if handle.isWorkflow && s.workflowEngine != nil {
			s.workflowEngine.CancelSession(sessionID)
		}
		if handle.cancel != nil {
			handle.cancel()
		}
	}

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
			Content:      "执行已由用户终止",
			Timestamp:    time.Now().UnixMilli(),
		}
		if err := s.repo.AppendMessage(sessionID, msg); err != nil {
			log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to append cancellation message to repo")
		} else {
			s.PublishSessionEvent(sessionID, SessionEvent{Type: EventTypeMessage, Message: &msg})
		}
	}

	statusPayload := map[string]any{"isRunning": false}
	if handle, ok := value.(*executionHandle); ok && handle.agentID != "" {
		statusPayload["agent"] = handle.agentID
	}
	s.PublishSessionEvent(sessionID, SessionEvent{Type: EventTypeStatus, Payload: statusPayload})
	s.PublishSessionEvent(sessionID, SessionEvent{Type: EventTypeDone, Payload: map[string]any{}})

	return http.StatusOK, ""
}

// handleStopSession handles POST /api/sessions/{id}/stop.
func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
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
