package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
)

// This file is how subagents report back: `subagent wait` (the parent blocks
// until children finish), `subagent reply` (a child messages its parent) and
// the push notification a finished child sends to its parent.

const (
	// defaultSubagentWait is the wait of `subagent wait` without a timeout.
	defaultSubagentWait = 300 * time.Second
	// maxSubagentWait caps any requested wait.
	maxSubagentWait = 300 * time.Second
	// waitSummaryBytes is the output tail reported per finished subagent.
	waitSummaryBytes = 1500
	// pushSummaryBytes is the output tail in a finish notification.
	pushSummaryBytes = 300
	// maxCoalescedPushBytes stops a queued notification from growing forever.
	maxCoalescedPushBytes = 8 << 10
)

// subagentOutputNote tells a waiting agent where the work of a finished
// subagent is.
const subagentOutputNote = "files the subagent wrote for you are under /shared (shared by your whole spawn tree); `subagent status <id>` shows the tail of its last output"

// SubagentWaitResult is one subagent in the answer of WaitSubagents.
type SubagentWaitResult struct {
	Info SubagentInfo
	// StillRunning is true for a subagent that had not finished when the wait
	// ended.
	StillRunning bool
	// Notes tells where the outputs of a finished subagent are.
	Notes string
}

// waitReg is one blocked `subagent wait`.
type waitReg struct {
	chat string // the waiting (calling) chat
	ids  map[string]bool
	wake chan struct{}

	mu      sync.Mutex
	closed  bool
	claimed map[string]bool
}

func (r *waitReg) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// claim reserves the delivery of a finished child to this wait: false when the
// wait is over already, so the finish must be delivered another way.
func (r *waitReg) claim(child string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.claimed[child] = true
	return true
}

func (r *waitReg) close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

// subagentSignals is the event plumbing of the subagent service: who waits
// for which child, which chats are marked as waiting on their descendants in
// the dir lock, and which finishes must not be pushed. The zero value is ready
// to use.
type subagentSignals struct {
	mu sync.Mutex
	// pubMu orders subagent state snapshots; pubSeq is the last seq per chat.
	pubMu  sync.Mutex
	pubSeq map[string]int64
	waits  map[*waitReg]struct{}
	// marked are the chats this service marked in the dir lock; holders lists
	// what keeps the mark (blocked waits, in-flight run state for later
	// users). A marked chat without holders waits for its mark to be cleared.
	marked  map[string]map[*waitReg]struct{}
	pending atomic.Int32 // marked chats without holders: clear still to do
	// quiet are chats whose finish is not pushed because the chat that would
	// hear of it is being stopped with them.
	quiet map[string]bool
	// releaseEpoch counts the releases of a chat's execution guard; a release
	// only reports a finish while no newer release exists, and lastPushed is
	// the epoch whose finish was reported last, so one finish is pushed once.
	releaseEpoch map[string]uint64
	lastPushed   map[string]uint64
	// queuedPush is the queued finish notification per parent chat, which
	// later finishes are merged into.
	queuedPush map[string]string
	// stopped marks chats the user (or a stop cascade) stopped. Unlike the stop
	// marker it survives until the chat starts a run that no subagent push or
	// reply triggered, so a late finish push or reply cannot restart the chat.
	stopped map[string]bool
}

// markStopped records that chatID was stopped.
func (s *Server) markStopped(chatID string) {
	g := &s.subagentSig
	g.mu.Lock()
	g.initLocked()
	g.stopped[chatID] = true
	g.mu.Unlock()
}

// clearStopped forgets the stopped mark: the chat is run for another reason
// than a subagent push or reply.
func (s *Server) clearStopped(chatID string) {
	g := &s.subagentSig
	g.mu.Lock()
	delete(g.stopped, chatID)
	g.mu.Unlock()
}

func (s *Server) isStopped(chatID string) bool {
	g := &s.subagentSig
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopped[chatID]
}

// forget drops everything the service keeps for a chat that is deleted or
// archived (and so never runs again).
func (g *subagentSignals) forget(chatID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.releaseEpoch, chatID)
	delete(g.lastPushed, chatID)
	delete(g.queuedPush, chatID)
	delete(g.quiet, chatID)
	delete(g.stopped, chatID)
	g.pubMu.Lock()
	delete(g.pubSeq, chatID)
	g.pubMu.Unlock()
}

func (g *subagentSignals) initLocked() {
	if g.waits == nil {
		g.waits = map[*waitReg]struct{}{}
		g.marked = map[string]map[*waitReg]struct{}{}
		g.quiet = map[string]bool{}
		g.releaseEpoch = map[string]uint64{}
		g.lastPushed = map[string]uint64{}
		g.queuedPush = map[string]string{}
		g.stopped = map[string]bool{}
	}
}

// locksChanged is the dir lock's change hook: a descendant may have let go, so
// marks that could not be cleared are retried. It never blocks the lock.
func (g *subagentSignals) locksChanged(s *Server) {
	if g.pending.Load() > 0 {
		go s.retryWaitMarkClears()
	}
}

// ---- waiting mark ---------------------------------------------------------

// holdWaitMark marks chat as waiting on its descendants in the dir lock for as
// long as reg is held, so its writable subagents can take the checkout.
func (s *Server) holdWaitMark(chat string, reg *waitReg) {
	g := &s.subagentSig
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	holders, isMarked := g.marked[chat]
	if !isMarked {
		if s.dirLocks == nil {
			return
		}
		holders = map[*waitReg]struct{}{}
		g.marked[chat] = holders
		_ = s.dirLocks.SetWaitingOnDescendants(chat, true)
	} else if len(holders) == 0 {
		g.pending.Add(-1)
	}
	holders[reg] = struct{}{}
}

// releaseWaitMark drops reg's hold on chat's mark and clears the mark when
// nothing else holds it. A writable descendant that still holds the lock keeps
// the mark: it is cleared when that descendant lets go, or retried at the
// latest when the run ends.
func (s *Server) releaseWaitMark(chat string, reg *waitReg) {
	g := &s.subagentSig
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	holders, ok := g.marked[chat]
	if !ok {
		return
	}
	if _, held := holders[reg]; !held {
		return
	}
	delete(holders, reg)
	if len(holders) == 0 {
		s.clearMarkLocked(chat)
	}
}

// clearMarkLocked tries to clear the mark of a chat without holders.
func (s *Server) clearMarkLocked(chat string) {
	g := &s.subagentSig
	err := s.dirLocks.SetWaitingOnDescendants(chat, false)
	if errors.Is(err, dirlock.ErrDescendantsActive) {
		g.pending.Add(1)
		return
	}
	delete(g.marked, chat)
}

// retryWaitMarkClears retries the clears that a running descendant refused.
func (s *Server) retryWaitMarkClears() {
	g := &s.subagentSig
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	if s.dirLocks == nil {
		return
	}
	for chat, holders := range g.marked {
		if len(holders) > 0 {
			continue
		}
		g.pending.Add(-1)
		s.clearMarkLocked(chat)
	}
}

// ---- finish events --------------------------------------------------------

// onExecutionReleased runs when a chat's execution guard is released: its run
// ended or the chat has nothing left to run. It wakes the waits on the chat,
// clears the chat's own waiting mark and, for a subagent that finished, tells
// its parent (unless a blocked wait of the parent delivers the result).
func (s *Server) onExecutionReleased(chatID string) {
	g := &s.subagentSig
	g.mu.Lock()
	g.initLocked()
	epoch := g.releaseEpoch[chatID]
	g.mu.Unlock()
	s.onExecutionReleasedAt(chatID, epoch)
}

// bumpReleaseEpoch records one more release of the chat's guard and returns
// its epoch.
func (s *Server) bumpReleaseEpoch(chatID string) uint64 {
	g := &s.subagentSig
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	g.releaseEpoch[chatID]++
	return g.releaseEpoch[chatID]
}

// clearQuiet forgets a pending suppression of the chat's finish push: a new
// run of the chat began, so the stop it was set for is over.
func (s *Server) clearQuiet(chatID string) {
	g := &s.subagentSig
	g.mu.Lock()
	delete(g.quiet, chatID)
	g.mu.Unlock()
}

// onExecutionReleasedAt handles the release with the given epoch. A release
// that a newer one overtook leaves the finish to the newer one.
func (s *Server) onExecutionReleasedAt(chatID string, epoch uint64) {
	parentChat, finished := "", false
	if s.repo != nil {
		if sess, err := s.repo.GetSessionMeta(chatID); err == nil && sess != nil && sess.ParentChatID != "" {
			parentChat = sess.ParentChatID
			finished = s.childFinished(chatID)
		}
	}

	g := &s.subagentSig
	g.mu.Lock()
	g.initLocked()
	stale := epoch < g.releaseEpoch[chatID]
	// The suppression is for the release that ends the chat: an earlier one
	// (still running, or overtaken) leaves it for that release.
	quiet := false
	if finished && !stale {
		quiet = g.quiet[chatID]
		delete(g.quiet, chatID)
	}
	// A blocked wait of the parent that covers the chat takes over reporting
	// it. Claiming happens here, in the same step that wakes the wait, so a
	// wait that returns because of this finish never has it pushed as well.
	claimed := false
	for reg := range g.waits {
		if !reg.ids[chatID] {
			continue
		}
		if finished && reg.chat == parentChat && reg.claim(chatID) {
			claimed = true
		}
		reg.signal()
	}
	// The run is over, so no blocked wait of it is left to keep a mark; a new
	// run of the chat that already started keeps the marks it set.
	_, running := s.activeExecutions.Load(chatID)
	if holders, ok := g.marked[chatID]; ok && s.dirLocks != nil && !running {
		if len(holders) == 0 {
			g.pending.Add(-1)
		}
		clear(holders)
		s.clearMarkLocked(chatID)
	}
	push := parentChat != "" && finished && !claimed && !quiet && !stale && g.lastPushed[chatID] < epoch
	if push {
		g.lastPushed[chatID] = epoch
	}
	g.mu.Unlock()
	if parentChat != "" && !stale {
		s.publishSubagentState(chatID)
	}

	// The chat's own run is over: forget its hold. For a child, the parent's
	// hold is re-evaluated once the finish is pushed (the push is injected
	// into the still-held process; releasing first would end it).
	s.forgetHold(chatID)
	if parentChat != "" {
		go func() {
			if push {
				s.pushSubagentFinished(chatID)
			}
			s.syncHold(parentChat)
		}()
	} else if push {
		go s.pushSubagentFinished(chatID)
	}
}

// stopDescendant stops a descendant that is stopped together with the chat
// above it: nobody above it is left to hear that it was killed.
func (s *Server) stopDescendant(chatID string) (int, string) {
	g := &s.subagentSig
	g.mu.Lock()
	g.initLocked()
	g.quiet[chatID] = true
	g.mu.Unlock()
	code, msg := s.stopOneExecution(chatID)
	if code != http.StatusOK {
		g.mu.Lock()
		delete(g.quiet, chatID)
		g.mu.Unlock()
	}
	if code == http.StatusConflict && s.repo != nil {
		// The child is between runs. When it is in the queue consumer's drain
		// handshake it is about to run its queued head: drop that, so it
		// cannot run on after its ancestor was stopped.
		if head, err := s.repo.PeekNextQueuedMessage(chatID); err == nil && head != nil && !s.queueParkedNow(chatID) {
			if _, err := s.repo.ClearQueuedMessages(chatID); err != nil {
				log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to clear queued messages of an escaped subagent")
			}
			if err := s.repo.SetSpawnOutcome(chatID, dbmodels.SpawnOutcomeKilled); err != nil {
				log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to record killed outcome of an escaped subagent")
			}
			s.PublishSessionEvent(chatID, SessionEvent{
				Type:    EventTypeQueue,
				Payload: map[string]any{"queue": []dbmodels.QueuedMessage{}},
			})
			s.markStopped(chatID)
			// The child counted as live through its queued head and no
			// execution release follows: re-evaluate its parent's hold.
			s.syncParentHold(chatID)
		}
	}
	return code, msg
}

// syncParentHold re-evaluates the hold of the parent of a subagent chat whose
// liveness changed without a release of its execution (its queued head was
// dropped). Callers hold spawnMu or nothing; the hold lock is taken inside.
func (s *Server) syncParentHold(chatID string) {
	if s.repo == nil {
		return
	}
	if sess, err := s.repo.GetSessionMeta(chatID); err == nil && sess != nil && sess.ParentChatID != "" {
		s.syncHold(sess.ParentChatID)
	}
}

// childFinished reports whether the chat has stopped for good: no execution and
// no queued message that is about to run.
func (s *Server) childFinished(chatID string) bool {
	if _, running := s.activeExecutions.Load(chatID); running {
		return false
	}
	head, err := s.repo.PeekNextQueuedMessage(chatID)
	if err != nil || head == nil {
		return true
	}
	return s.queueParkedNow(chatID)
}

// pushRetryDelays are the waits before the further delivery attempts of a
// finish notification the parent's runner was unavailable for.
var defaultPushRetryDelays = []time.Duration{10 * time.Second, 20 * time.Second}

// pushSubagentFinished tells the parent of a finished child about it, unless
// that is pointless: the parent is gone, was stopped, or waits for the child.
// A delivery that failed because the parent's runner is unavailable is retried
// a few times, until the server shuts down.
func (s *Server) pushSubagentFinished(chatID string) {
	delays := defaultPushRetryDelays
	if s.pushRetryDelays != nil {
		delays = s.pushRetryDelays
	}
	for attempt := 0; ; attempt++ {
		if !s.pushSubagentFinishedOnce(chatID, attempt >= len(delays)) || attempt >= len(delays) {
			return
		}
		t := time.NewTimer(delays[attempt])
		select {
		case <-t.C:
		case <-s.Context().Done():
			t.Stop()
			return
		}
	}
}

// pushSubagentFinishedOnce makes one delivery attempt; it returns true when
// the attempt failed in a way worth retrying.
// With final, a failure to reach the parent is not retried but noted in the
// parent's transcript.
func (s *Server) pushSubagentFinishedOnce(chatID string, final bool) (retry bool) {
	sess, err := s.repo.GetSessionMeta(chatID)
	if err != nil || sess == nil || sess.ParentChatID == "" {
		return false
	}
	if !s.childFinished(chatID) {
		return false
	}
	parent, err := s.repo.GetSessionMeta(sess.ParentChatID)
	if err != nil || parent == nil || s.sessionStopInProgress(parent.ChatID) {
		return false
	}
	info := s.subagentInfo(sess)
	if info.State == SubagentStateRunning || info.State == SubagentStateQueued {
		return false
	}
	text := fmt.Sprintf("[subagent %s%s finished: %s]", chatID, labelSuffix(sess.SpawnLabel), info.State)
	if tail := strings.Join(strings.Fields(s.lastOutputTail(chatID, pushSummaryBytes)), " "); tail != "" {
		text += " " + tail
	}
	text += fmt.Sprintf("; full output: see `subagent status %s` or /shared", chatID)
	_, err = s.deliverToParent(s.Context(), parent, text, "subagent-finished", true)
	if err == nil {
		return false
	}
	ev := log.Warn().Err(err).Str("chat_id", chatID).Str("parent", parent.ChatID)
	var se *SubagentError
	if errors.As(err, &se) {
		switch se.Kind {
		case SubagentUnavailable:
			if final {
				ev.Msg("failed to notify the parent of a finished subagent, giving up")
				s.noteDroppedFinish(parent.ChatID, chatID, info.State, "the chat's runner stayed unavailable")
				return false
			}
			ev.Msg("failed to notify the parent of a finished subagent, will retry")
			return true
		case SubagentLimit:
			ev.Msg("the parent's queue is full: dropped the notification of a finished subagent")
			s.noteDroppedFinish(parent.ChatID, chatID, info.State, "the chat's message queue was full")
			return false
		}
	}
	ev.Msg("failed to notify the parent of a finished subagent")
	return false
}

// noteDroppedFinish tells the user, in the parent chat, about a finish
// notification that could not be delivered.
func (s *Server) noteDroppedFinish(parentChat, child, state, reason string) {
	msg := dbmodels.ChatMessage{
		ID:           fmt.Sprintf("subagent-finish-dropped-%s-%s", child, uuid.NewV7().String()),
		Role:         "activity",
		ActivityType: "SUBAGENT_NOTICE",
		Content:      fmt.Sprintf("subagent %s finished (%s) but %s: check `subagent status %s`", child, state, reason, child),
		Timestamp:    time.Now().UnixMilli(),
	}
	if err := s.repo.AppendMessage(parentChat, msg); err != nil {
		return
	}
	s.PublishSessionEvent(parentChat, SessionEvent{Type: EventTypeMessage, Message: &msg})
}

// noteHeldBack tells the user, in a stopped chat, about a subagent message
// that was not delivered because the chat was stopped.
func (s *Server) noteHeldBack(chatID, text string) {
	if len(text) > 500 {
		text = text[:500] + "..."
	}
	msg := dbmodels.ChatMessage{
		ID:           fmt.Sprintf("subagent-held-back-%s-%s", chatID, uuid.NewV7().String()),
		Role:         "activity",
		ActivityType: "SUBAGENT_NOTICE",
		Content:      "not delivered to this stopped chat: " + text,
		Timestamp:    time.Now().UnixMilli(),
	}
	if err := s.repo.AppendMessage(chatID, msg); err != nil {
		return
	}
	s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &msg})
}

func labelSuffix(label string) string {
	if label == "" {
		return ""
	}
	return " (" + label + ")"
}

// deliverToParent hands text to the parent chat of a subagent: injected into
// its running turn when its CLI takes live input, else as its next turn
// (queued behind the running turn, or started when it is idle). With coalesce,
// a notification already queued absorbs text instead of taking another slot.
// ctx is the sender's run; once it is done nothing is delivered.
func (s *Server) deliverToParent(ctx context.Context, parent *dbmodels.Session, text, source string, coalesce bool) (string, error) {
	if s.sessionStopInProgress(parent.ChatID) {
		return "", subagentErr(SubagentDenied, "the parent chat is being stopped")
	}
	target, err := s.findAgentFor(parent.RunnerNodeID, parent.CurrentAgent)
	if err != nil {
		return "", subagentErr(SubagentInternal, "failed to list agents of runner: %v", err)
	}
	if target == nil || target.Config.Type == "workflow" {
		return "", subagentErr(SubagentNotFound, "the agent of the parent chat is not available")
	}
	// The agent whose turn is running may differ from the chat's current one.
	runAgent := target.Config.ID
	if v, running := s.activeExecutions.Load(parent.ChatID); running {
		if h, ok := v.(*executionHandle); ok && h != nil {
			if h.isWorkflow {
				runAgent = ""
			} else if h.agentID != "" {
				runAgent = h.agentID
			}
		}
	}
	if runAgent != "" && s.tryInject(parent.ChatID, runAgent, text) {
		return SubagentDeliveryInjected, nil
	}
	if ctx.Err() != nil {
		return "", subagentErr(SubagentDenied, "the sender is being stopped")
	}

	// Under spawnMu a stop cascade is either done (marker visible) or waits.
	s.spawnMu.Lock()
	defer s.spawnMu.Unlock()
	if ctx.Err() != nil || s.sessionStopInProgress(parent.ChatID) {
		return "", subagentErr(SubagentDenied, "the sender or the parent chat is being stopped")
	}
	// A stopped chat stays stopped: nothing a subagent says restarts it, but
	// the user gets to see what was held back.
	if s.isStopped(parent.ChatID) {
		s.noteHeldBack(parent.ChatID, text)
		return "", subagentErr(SubagentDenied, "the parent chat was stopped")
	}
	if coalesce && s.mergeQueuedPush(parent.ChatID, text) {
		return SubagentDeliveryQueued, nil
	}
	rec := newResponseCapture()
	s.triggerMessage(s.Context(), nil, rec, target.Config.ID, target, TriggerMessageRequest{
		Prompt:   text,
		ChatID:   parent.ChatID,
		RunnerID: parent.RunnerNodeID,
		RunDir:   parent.RunDir,
		Metadata: map[string]any{"source": source},
	}, false)
	var body struct {
		Status    string `json:"status"`
		MessageID string `json:"messageId"`
		Error     string `json:"error"`
	}
	_ = json.Unmarshal(rec.body.Bytes(), &body)
	if rec.code != http.StatusAccepted {
		kind := SubagentInternal
		switch {
		case rec.code == http.StatusServiceUnavailable:
			kind = SubagentUnavailable
		case body.Error == queueLimitMessage:
			kind = SubagentLimit
		}
		return "", subagentErr(kind, "failed to deliver to the parent: %s", firstNonEmpty(body.Error, fmt.Sprintf("HTTP status %d", rec.code)))
	}
	if body.Status == "queued" {
		if coalesce && body.MessageID != "" {
			g := &s.subagentSig
			g.mu.Lock()
			g.initLocked()
			g.queuedPush[parent.ChatID] = body.MessageID
			g.mu.Unlock()
		}
		return SubagentDeliveryQueued, nil
	}
	return SubagentDeliveryStarted, nil
}

// mergeQueuedPush appends text to the finish notification still queued for the
// chat, if there is one.
func (s *Server) mergeQueuedPush(chatID, text string) bool {
	g := &s.subagentSig
	g.mu.Lock()
	g.initLocked()
	id := g.queuedPush[chatID]
	g.mu.Unlock()
	if id == "" {
		return false
	}
	queued, err := s.repo.GetQueuedMessage(chatID, id)
	if err != nil || queued == nil || len(queued.Prompt)+len(text) > maxCoalescedPushBytes {
		return false
	}
	if _, err := s.repo.UpdateQueuedMessage(chatID, id, queued.Prompt+"\n"+text); err != nil {
		return false
	}
	if q, _ := s.repo.GetQueuedMessages(chatID); q != nil {
		s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeQueue, Payload: map[string]any{"queue": q}})
	}
	return true
}

// ---- wait -----------------------------------------------------------------

// WaitSubagents blocks until the given subagents of callerChat (or of its
// descendants) finished, or, with anyOf, until one did, or until timeout
// passes (capped at maxSubagentWait; zero means the default). Without ids it
// waits for all running direct children. Every wait reports each subagent: its
// state and output tail, or StillRunning. It ends early, with a denial, when
// ctx (the caller's run) is done. While it blocks, callerChat is marked as
// waiting on its descendants in the dir lock, so its writable subagents can
// take the checkout.
func (s *Server) WaitSubagents(ctx context.Context, callerChat string, ids []string, anyOf bool, timeout time.Duration) ([]SubagentWaitResult, error) {
	if s.repo == nil {
		return nil, subagentErr(SubagentUnavailable, "database unavailable in degraded mode")
	}
	if timeout <= 0 {
		timeout = defaultSubagentWait
	}
	timeout = min(timeout, maxSubagentWait)

	var order []string
	seen := map[string]bool{}
	if len(ids) == 0 {
		children, err := s.ListSubagents(callerChat)
		if err != nil {
			return nil, err
		}
		for _, c := range children {
			if c.State == SubagentStateRunning || c.State == SubagentStateQueued {
				order = append(order, c.ID)
			}
		}
	} else {
		for _, id := range ids {
			if _, err := s.ownedSubagent(callerChat, id); err != nil {
				return nil, err
			}
			if !seen[id] {
				seen[id] = true
				order = append(order, id)
			}
		}
	}
	if len(order) == 0 {
		return []SubagentWaitResult{}, nil
	}

	reg := &waitReg{
		chat:    callerChat,
		ids:     map[string]bool{},
		wake:    make(chan struct{}, 1),
		claimed: map[string]bool{},
	}
	for _, id := range order {
		reg.ids[id] = true
	}
	g := &s.subagentSig
	g.mu.Lock()
	g.initLocked()
	g.waits[reg] = struct{}{}
	g.mu.Unlock()
	defer func() {
		reg.close()
		g.mu.Lock()
		delete(g.waits, reg)
		g.mu.Unlock()
		s.releaseWaitMark(callerChat, reg)
	}()

	satisfied := func() bool {
		finished := 0
		for _, id := range order {
			if s.childFinished(id) {
				finished++
			}
		}
		if anyOf {
			return finished > 0
		}
		return finished == len(order)
	}

	if !satisfied() {
		s.holdWaitMark(callerChat, reg)
		timer := time.NewTimer(timeout)
		defer timer.Stop()
	loop:
		for !satisfied() {
			select {
			case <-reg.wake:
			case <-timer.C:
				break loop
			case <-ctx.Done():
				return nil, subagentErr(SubagentDenied, "the caller's run ended")
			}
		}
	}
	// From here on a finish is no longer handed to this wait but pushed, and
	// every child that finished until now is in the answer.
	reg.close()

	out := make([]SubagentWaitResult, 0, len(order))
	for _, id := range order {
		sess, err := s.repo.GetSessionMeta(id)
		if err != nil || sess == nil {
			continue
		}
		info := s.subagentInfo(sess)
		res := SubagentWaitResult{Info: info}
		if s.childFinished(id) {
			info.Summary = s.lastOutputTail(id, waitSummaryBytes)
			res.Info = info
			res.Notes = subagentOutputNote
		} else {
			res.StillRunning = true
		}
		out = append(out, res)
	}
	return out, nil
}

// ---- reply ----------------------------------------------------------------

// ReplyToParent delivers text from a subagent (callerChat)
// to the chat that spawned it, like SendToSubagent in the other direction:
// injected into the parent's running turn, or run as its next turn. A chat
// without a parent is refused. ctx is the caller's run: once it is done, or
// the parent was stopped, nothing is delivered, so a stopped parent is not
// restarted.
func (s *Server) ReplyToParent(ctx context.Context, callerChat, text string) (string, error) {
	if s.repo == nil {
		return "", subagentErr(SubagentUnavailable, "database unavailable in degraded mode")
	}
	if strings.TrimSpace(text) == "" {
		return "", subagentErr(SubagentInvalid, "message is required")
	}
	if len(text) > maxSubagentMessageBytes {
		return "", subagentErr(SubagentInvalid, "message too large (limit %d bytes)", maxSubagentMessageBytes)
	}
	sess, err := s.repo.GetSessionMeta(callerChat)
	if err != nil {
		return "", subagentErr(SubagentInternal, "failed to get session: %v", err)
	}
	if sess == nil {
		return "", subagentErr(SubagentNotFound, "caller chat not found")
	}
	if sess.ParentChatID == "" {
		return "", subagentErr(SubagentInvalid, "this chat is not a subagent: there is no parent to reply to")
	}
	parent, err := s.repo.GetSessionMeta(sess.ParentChatID)
	if err != nil {
		return "", subagentErr(SubagentInternal, "failed to get the parent chat: %v", err)
	}
	if parent == nil {
		return "", subagentErr(SubagentNotFound, "the parent chat no longer exists")
	}
	if ctx.Err() != nil || s.sessionStopInProgress(callerChat) {
		return "", subagentErr(SubagentDenied, "the subagent is being stopped")
	}
	prompt := fmt.Sprintf("[message from subagent %s%s]\n%s", callerChat, labelSuffix(sess.SpawnLabel), text)
	return s.deliverToParent(ctx, parent, prompt, "subagent-reply", false)
}
