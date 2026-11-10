package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"uuid"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// runInjector pushes a message into a running agent CLI (runnerhub.Hub).
type runInjector interface {
	Inject(ctx context.Context, nodeID, runID, prompt string) (injected bool, err error)
}

// genInjector is implemented by injectors that also report the idle
// generation of the run's aw after it accepted a message (runnerhub.Hub).
type genInjector interface {
	InjectGen(ctx context.Context, nodeID, runID, prompt string) (injected bool, idleGen uint64, err error)
}

// inject pushes prompt into the run, with the idle generation when known (0
// otherwise).
func (s *Server) inject(ctx context.Context, nodeID, runID, prompt string) (bool, uint64, error) {
	if gi, ok := s.injector.(genInjector); ok {
		return gi.InjectGen(ctx, nodeID, runID, prompt)
	}
	injected, err := s.injector.Inject(ctx, nodeID, runID, prompt)
	return injected, 0, err
}

// activeRunLookup finds the run currently executing for a chat and agent
// (dispatch.ActiveRuns).
type activeRunLookup interface {
	Lookup(chatID, agentID string) (nodeID, runID string, ok bool)
}

// Reasons reported with injected=false.
const (
	sendNowNotRunning   = "not_running"
	sendNowNotSupported = "not_supported"
	sendNowFailed       = "failed"
	// sendNowHeldByChild: the chat waits between turns for its subagents and
	// one of them holds the checkout, so a new turn must not start.
	sendNowHeldByChild = "descendant_holds_checkout"
)

// handleSendQueuedNow handles POST /api/sessions/{id}/queue/{messageId}/send-now.
//
// It tries to push a queued message into the turn the session's agent is
// running right now. A restore is never refused: if the queue was cleared
// while the message was claimed, the message comes back anyway (never losing
// a message wins over honoring the clear). Not injecting is a normal outcome (200 with
// injected=false): the message then simply stays queued where it was.
func (s *Server) handleSendQueuedNow(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
		return
	}
	id := r.PathValue("id")
	if id == "" || !IsValidChatID(id) {
		writeJSONError(w, http.StatusBadRequest, "invalid session id format")
		return
	}
	session, aerr := s.authorizeSession(r, id)
	if aerr != nil {
		writeJSONError(w, aerr.status, aerr.msg)
		return
	}
	if session == nil {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}
	messageID := r.PathValue("messageId")
	if messageID == "" {
		writeJSONError(w, http.StatusBadRequest, "messageId is required")
		return
	}

	visible, err := s.agentsFor(session.RunnerNodeID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list agents of runner: "+err.Error())
		return
	}
	var targetAgent *agentspec.Agent
	for _, a := range visible {
		if (a.Config.ID == session.CurrentAgent || a.Config.Name == session.CurrentAgent) && a.Config.Type != "workflow" {
			targetAgent = a
			break
		}
	}
	if targetAgent == nil {
		writeJSONError(w, http.StatusBadRequest, "Agent not found or offline")
		return
	}

	// (a) Nothing to inject into: leave the queue alone.
	var nodeID, runID string
	var running bool
	if s.activeRuns != nil && s.injector != nil {
		nodeID, runID, running = s.activeRuns.Lookup(id, targetAgent.Config.ID)
	}
	if !running {
		writeSendNowResult(w, map[string]any{"injected": false, "reason": sendNowNotRunning})
		return
	}

	// (b) Claim the message so the queue consumer cannot also run it.
	msg, err := s.repo.ClaimQueuedMessage(id, messageID)
	if err != nil {
		if errors.Is(err, dbmodels.ErrQueuedMessageNotFound) {
			writeJSONError(w, http.StatusNotFound, "message no longer queued")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to claim queued message: "+err.Error())
		return
	}

	// From here until the outcome is known the message exists only in memory,
	// so every exit (error, panic) goes through this restore.
	restore := true
	restoreFailed := false
	restoreMsg := func() {
		if !restore {
			return
		}
		restore = false
		if rerr := s.requeueWithRetry(msg); rerr != nil {
			// The message is lost from the queue; never claim otherwise. Put
			// its text in the transcript so the user can resend it.
			restoreFailed = true
			log.Error().Err(rerr).Str("chat_id", id).Str("queued_id", msg.ID).Msg("failed to restore claimed queued message")
			errMsg := dbmodels.ChatMessage{
				ID:        fmt.Sprintf("error-%s-%s", id, uuid.NewV7().String()),
				Role:      "error",
				Content:   fmt.Sprintf("a queued message could not be put back in the queue and was not executed (%v). please resend it: %s", rerr, msg.Prompt),
				AgentName: targetAgent.Config.Name,
				Timestamp: time.Now().UnixMilli(),
			}
			if aerr := s.repo.AppendMessage(id, errMsg); aerr != nil {
				log.Error().Err(aerr).Str("chat_id", id).Msg("failed to append restore failure notice")
			} else {
				s.PublishSessionEvent(id, SessionEvent{Type: EventTypeMessage, Message: &errMsg})
			}
		}
		s.publishQueue(id)
		// The run may have ended while we tried: the consumer must pick the
		// restored message up.
		s.unparkQueue(id)
		s.clearStopped(id)
		s.startQueueConsumerIfIdle(session.ChatID, targetAgent, session.RunDir)
	}
	// Safety net for panics; the normal paths restore before responding so a
	// client that refetches the queue after the response already sees it.
	defer restoreMsg()
	// The queue shrank by the claim; tell the UI right away so it never shows
	// a message that is about to be injected as still queued (restore
	// republishes if it comes back).
	s.publishQueue(id)

	// (c) Inject blocks up to ~6s and ignores cancellation, and giving up
	// early would risk delivering the message twice, so the request context
	// is detached: the outcome must be recorded even if the client left.
	//
	// A chat waiting between turns for its subagents gives up its waiting mark
	// first; if a writable subagent holds the checkout the message stays
	// queued (as for every other inject path).
	ready, done := s.prepareHeldInject(id)
	if !ready {
		restoreMsg()
		if restoreFailed {
			writeJSONError(w, http.StatusInternalServerError, "the message could not be restored to the queue and was not sent; it was added to the transcript, please resend it")
			return
		}
		writeSendNowResult(w, map[string]any{"injected": false, "reason": sendNowHeldByChild})
		return
	}
	injected, idleGen, ierr := s.inject(context.WithoutCancel(r.Context()), nodeID, runID, msg.Prompt)
	done(injected, idleGen)
	if !injected {
		reason := sendNowNotSupported
		switch {
		case ierr == nil:
		case errors.Is(ierr, runnerhub.ErrUnknownRun), errors.Is(ierr, runnerhub.ErrRunnerOffline):
			reason = sendNowNotRunning
		default:
			reason = sendNowFailed
			log.Warn().Err(ierr).Str("chat_id", id).Msg("send-now inject failed, message stays queued")
		}
		restoreMsg()
		if restoreFailed {
			writeJSONError(w, http.StatusInternalServerError, "the message could not be restored to the queue and was not sent; it was added to the transcript, please resend it")
			return
		}
		writeSendNowResult(w, map[string]any{"injected": false, "reason": reason})
		return
	}
	// Delivered: the message must not come back, whatever happens below.
	restore = false

	// (d) Record it like the incoming prompt of a run.
	userMsg := dbmodels.ChatMessage{
		ID:        fmt.Sprintf("msg-%s", uuid.NewV7().String()),
		Role:      "user",
		Content:   msg.Prompt,
		Timestamp: time.Now().UnixMilli(),
	}
	if aerr := s.repo.AppendMessage(id, userMsg); aerr != nil {
		log.Error().Err(aerr).Str("chat_id", id).Msg("failed to append injected message to repo")
	} else {
		s.PublishSessionEvent(id, SessionEvent{Type: EventTypeMessage, Message: &userMsg})
	}
	queue := s.publishQueue(id)
	writeSendNowResult(w, map[string]any{"injected": true, "queue": queue})
}

// sendNowRestoreRetryDelay is the pause between restore attempts; a variable
// for tests.
var sendNowRestoreRetryDelay = 100 * time.Millisecond

const sendNowRestoreAttempts = 4

// requeueWithRetry puts a claimed message back, retrying because the database
// (SQLite) can fail a write transiently when it is busy.
func (s *Server) requeueWithRetry(msg *dbmodels.QueuedMessage) error {
	requeue := s.repo.RequeueFront
	if s.requeueFn != nil {
		requeue = s.requeueFn
	}
	var err error
	for i := 0; i < sendNowRestoreAttempts; i++ {
		if i > 0 {
			time.Sleep(sendNowRestoreRetryDelay)
		}
		if err = requeue(msg); err == nil {
			return nil
		}
	}
	return err
}

// publishQueue broadcasts the current queue snapshot and returns it.
func (s *Server) publishQueue(chatID string) []dbmodels.QueuedMessage {
	queue, _ := s.repo.GetQueuedMessages(chatID)
	if queue == nil {
		queue = []dbmodels.QueuedMessage{}
	}
	s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeQueue, Payload: map[string]any{"queue": queue}})
	return queue
}

func writeSendNowResult(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}
