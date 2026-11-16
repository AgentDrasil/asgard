package api

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/rs/zerolog/log"
	"google.golang.org/protobuf/proto"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
	"github.com/AgentDrasil/asgard/backend/lib/worktree"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// This file is `subagent ask` / `answer` / `questions`: an agent asks a
// specific chat of its spawn tree a question (addressed by chat id) and blocks
// until that chat answers.
//
// The questions live in memory (questionBook): a backend restart drops every
// open question, and the asker's `ask --resume` then fails with "not found".
// The store is bounded: open questions per tree and per asker are capped,
// answered and cancelled entries are dropped after a TTL, and the whole book
// has a hard size.

const (
	// maxOpenQuestionsPerTree and maxOpenQuestionsPerAsker cap unanswered
	// questions; a further ask is refused with a limit error.
	maxOpenQuestionsPerTree  = 64
	maxOpenQuestionsPerAsker = 8
	// questionAnsweredTTL is how long an answered or cancelled question is kept
	// for `ask --resume`; questionOpenTTL drops a question nobody answered.
	questionAnsweredTTL = time.Hour
	questionOpenTTL     = 24 * time.Hour
	// maxStoredQuestions is the hard bound of the book.
	maxStoredQuestions = 2048
	// defaultAskWait is the wait of `ask` without a timeout (the cap is
	// maxSubagentWait). It is kept below the tool timeout of the usual agent CLIs
	// (Claude Code's Bash tool kills a command at 120s by default).
	defaultAskWait = 90 * time.Second
	// questionFirstLineBytes bounds the first line shown by `questions`.
	questionFirstLineBytes = 200
)

// Question states reported for outgoing questions.
const (
	QuestionStatePending  = "pending"
	QuestionStateAnswered = "answered"
)

// Deliveries of AnswerQuestion besides the message deliveries of send.
const (
	// AnswerDeliveryUnblocked: the asker's blocked ask/resume got the answer.
	AnswerDeliveryUnblocked = "unblocked"
	// AnswerDeliveryStored: the answer is kept for `ask --resume` only; the
	// message to the asker chat could not be delivered.
	AnswerDeliveryStored = "stored"
)

type questionPhase int

const (
	questionOpen questionPhase = iota
	questionAnswered
	questionCancelled
)

// question is one question between two chats of a spawn tree.
type question struct {
	qid  string
	root string // root chat of the spawn tree
	// asker and target are chat ids; the names are what the messages show.
	asker, askerAgent, askerLabel string
	target                        string
	text                          string
	created                       time.Time

	phase        questionPhase
	answer       string
	cancelReason string
	finishedAt   time.Time
	// done is closed when the question leaves the open phase.
	done chan struct{}
	// delivering: the question is added but its delivery has not returned yet; a
	// run that ends meanwhile is not an end of the question (it may become the
	// prompt of the next turn).
	delivering bool
	// waiter is the one ask/resume call blocked on the question right now (nil
	// when none). A newer call replaces it, see attachLocked.
	waiter *askWaiter
	// consumed: an ask/resume returned the answer.
	consumed bool
	// injectSkipped: the answer was handed to a blocked call instead of being
	// delivered as a message to the asker chat.
	injectSkipped bool
	// answerMsg, answerSender and answerFrom describe the answer message for
	// the asker chat (answerFrom is the answering chat and agent id).
	answerMsg, answerSender, answerFrom, answerAgentID string
}

// askWaiter is one blocked ask/resume call of the asker.
type askWaiter struct {
	// superseded is closed when a newer ask/resume of the same question takes
	// over: a question has at most one blocked call.
	superseded chan struct{}
	// gotAnswer: the call returned the answer to its caller.
	gotAnswer bool
	// closed: the callback call ended (see endWaiter); a result it still
	// produces reaches nobody.
	closed bool
}

// attachLocked makes a new blocked call the question's only one; a call that
// was blocked before is superseded and fails.
func (q *question) attachLocked() *askWaiter {
	if q.waiter != nil {
		close(q.waiter.superseded)
	}
	w := &askWaiter{superseded: make(chan struct{})}
	q.waiter = w
	return w
}

// questionBook is the in-memory store of all questions. The zero value is
// ready to use.
type questionBook struct {
	mu        sync.Mutex
	m         map[string]*question
	lastSweep time.Time
	// now is the clock (tests); nil means time.Now.
	now func() time.Time
}

func (b *questionBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// sweepLocked drops expired entries (at most once a minute unless force).
func (b *questionBook) sweepLocked(force bool) {
	now := b.clock()
	if !force && now.Sub(b.lastSweep) < time.Minute {
		return
	}
	b.lastSweep = now
	for id, q := range b.m {
		switch q.phase {
		case questionOpen:
			if now.Sub(q.created) > questionOpenTTL {
				b.cancelLocked(q, "question "+id+" expired without an answer")
			}
		default:
			if now.Sub(q.finishedAt) > questionAnsweredTTL {
				delete(b.m, id)
			}
		}
	}
}

// cancelLocked ends an open question without an answer and wakes its callers.
func (b *questionBook) cancelLocked(q *question, reason string) {
	if q.phase != questionOpen {
		return
	}
	q.phase, q.cancelReason, q.finishedAt = questionCancelled, reason, b.clock()
	close(q.done)
}

// addLocked stores a new open question after checking the caps.
func (b *questionBook) addLocked(q *question) error {
	b.sweepLocked(false)
	if b.m == nil {
		b.m = map[string]*question{}
	}
	tree, asker := 0, 0
	for _, o := range b.m {
		if o.phase != questionOpen {
			continue
		}
		if o.root == q.root {
			tree++
		}
		if o.asker == q.asker {
			asker++
		}
	}
	if asker >= maxOpenQuestionsPerAsker {
		return subagentErr(SubagentLimit, "too many open questions: you already have %d of %d unanswered; wait for answers (`subagent questions`, `subagent ask --resume <qid>`)", asker, maxOpenQuestionsPerAsker)
	}
	if tree >= maxOpenQuestionsPerTree {
		return subagentErr(SubagentLimit, "too many open questions in this spawn tree (%d of %d unanswered)", tree, maxOpenQuestionsPerTree)
	}
	if len(b.m) >= maxStoredQuestions {
		// Make room: the oldest finished entry goes first.
		var oldest *question
		for _, o := range b.m {
			if o.phase != questionOpen && (oldest == nil || o.finishedAt.Before(oldest.finishedAt)) {
				oldest = o
			}
		}
		if oldest == nil {
			return subagentErr(SubagentLimit, "too many open questions")
		}
		delete(b.m, oldest.qid)
	}
	b.m[q.qid] = q
	return nil
}

// cancelQuestionsOf cancels the open questions that involve a chat that was
// killed, stopped or deleted: those addressed to it (their blocked asks fail at
// once with "target <id> <verb>") and those it asked (nobody is left to read
// the answer).
func (s *Server) cancelQuestionsOf(chatID, verb string) {
	b := &s.questions
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, q := range b.m {
		if q.phase != questionOpen {
			continue
		}
		switch chatID {
		case q.target:
			b.cancelLocked(q, fmt.Sprintf("target %s was %s", chatID, verb))
		case q.asker:
			b.cancelLocked(q, fmt.Sprintf("the asker %s was %s: the question is cancelled", chatID, verb))
		}
	}
}

// AskOutcome is the result of AskSubagent and ResumeQuestion.
type AskOutcome struct {
	QID string
	// Answered is false when the timeout passed first; the question stays open.
	Answered bool
	Answer   string
}

// QuestionInfo describes one question for `questions`.
type QuestionInfo struct {
	QID                   string
	FromChatID, FromAgent string
	FromLabel, FirstLine  string
	ToChatID, State       string
	Created               time.Time
}

// chatAgentName is the display name of an agent id on a chat's runner.
func (s *Server) chatAgentName(runner, agentID string) string {
	if a, _ := s.findAgentFor(runner, agentID); a != nil {
		return a.Config.Name
	}
	return agentID
}

// AskSubagent asks chat to (any other chat of callerChat's spawn tree: an
// ancestor, sibling, descendant or the root) the question text and blocks until
// it answers, the timeout passes (capped at maxSubagentWait; zero means the
// default) or ctx (the caller's run) is done. The tree is derived from
// callerChat alone, so a chat of another tree is never addressable. The
// question is delivered like `subagent send`: injected into the target's
// running or idle-held turn, otherwise queued or started as a turn.
func (s *Server) AskSubagent(ctx context.Context, callerChat, callerAgent, to, text string, timeout time.Duration) (AskOutcome, error) {
	if s.repo == nil {
		return AskOutcome{}, subagentErr(SubagentUnavailable, "database unavailable in degraded mode")
	}
	if strings.TrimSpace(text) == "" {
		return AskOutcome{}, subagentErr(SubagentInvalid, "a question is required")
	}
	if len(text) > maxSubagentMessageBytes {
		return AskOutcome{}, subagentErr(SubagentInvalid, "question too large (limit %d bytes)", maxSubagentMessageBytes)
	}
	if !IsValidChatID(to) {
		return AskOutcome{}, subagentErr(SubagentInvalid, "invalid chat id")
	}
	if to == callerChat {
		return AskOutcome{}, subagentErr(SubagentInvalid, "you cannot ask yourself")
	}
	caller, err := s.repo.GetSessionMeta(callerChat)
	if err != nil {
		return AskOutcome{}, subagentErr(SubagentInternal, "failed to get session: %v", err)
	}
	if caller == nil {
		return AskOutcome{}, subagentErr(SubagentNotFound, "caller chat not found")
	}
	target, err := s.treeMember(caller, to)
	if err != nil {
		return AskOutcome{}, err
	}
	root, _ := s.repo.SpawnRootID(callerChat)
	if ctx.Err() != nil || s.sessionStopInProgress(callerChat) || s.sessionStopInProgress(to) {
		return AskOutcome{}, subagentErr(SubagentDenied, "the target or the caller is being stopped")
	}
	targetAgent, err := s.findAgentFor(target.RunnerNodeID, target.CurrentAgent)
	if err != nil {
		return AskOutcome{}, subagentErr(SubagentInternal, "failed to list agents of runner: %v", err)
	}
	if targetAgent == nil || targetAgent.Config.Type == "workflow" {
		return AskOutcome{}, subagentErr(SubagentNotFound, "the agent of chat %q is not available", to)
	}
	// Done and failed chats get a resumed turn, like `send`; a killed or
	// stopped one stays stopped.
	if s.subagentInfo(target).State == SubagentStateKilled || s.isStopped(to) {
		return AskOutcome{}, subagentErr(SubagentUnavailable, "target %s was killed", to)
	}
	if blocker := s.askCycle(callerChat, to); blocker != "" {
		return AskOutcome{}, subagentErr(SubagentUnavailable,
			"target %s is blocked in a question that leads back to you (%s waits for an answer from you); it cannot answer while it waits. Ask someone else or end your turn with the question", to, blocker)
	}
	liveAtCheck := s.activeRuns != nil && s.hasRunFor(to, targetAgent.Config.ID)
	if holder := s.askDeadlock(caller, target, targetAgent, !cliMayNotInject(targetAgent)); holder != "" {
		return AskOutcome{}, deadlockErr(to, holder)
	}

	q := &question{
		qid:        uuid.NewV7().String(),
		root:       root,
		asker:      callerChat,
		askerAgent: s.chatAgentName(caller.RunnerNodeID, callerAgent),
		askerLabel: caller.SpawnLabel,
		target:     to,
		text:       text,
		created:    s.questions.clock(),
		done:       make(chan struct{}),
	}
	// The asker counts as blocked from the moment the question exists, so an
	// answer that comes back while it is still being delivered is not also
	// injected into the asker chat.
	q.delivering = true
	s.questions.mu.Lock()
	err = s.questions.addLocked(q)
	var w *askWaiter
	if err == nil {
		w = q.attachLocked()
	}
	s.questions.mu.Unlock()
	if err != nil {
		return AskOutcome{}, err
	}
	onCallEnd(ctx, s.endWaiterHook(q, w))
	drop := func() {
		s.questions.mu.Lock()
		delete(s.questions.m, q.qid)
		s.questions.mu.Unlock()
	}
	prompt := fmt.Sprintf("[question %s from %s %s%s]\n%s\nAnswer with: subagent answer %s -- <text>",
		q.qid, q.askerAgent, callerChat, labelSuffix(q.askerLabel), text, q.qid)
	sender := fmt.Sprintf("agent %s %s%s", q.askerAgent, callerChat, labelSuffix(q.askerLabel))
	delivery, err := s.deliverToChat(ctx, callerChat, callerAgent, target, prompt, "subagent-ask", sender)
	s.questions.mu.Lock()
	q.delivering = false
	s.questions.mu.Unlock()
	if err != nil {
		drop()
		return AskOutcome{}, err
	}
	// A run that ended while the question was being delivered (an inject into a
	// run that is gone, nothing queued, nothing running) cannot answer it.
	s.cancelIncomingOnRunEnd(to)
	// The target had a live run at the check, so the question was expected to be
	// injected. It was queued instead (the inject failed): it runs as a later
	// turn, which needs the lock like any new turn, so check again.
	if delivery == SubagentDeliveryQueued && liveAtCheck {
		if holder := s.askDeadlock(caller, target, targetAgent, false); holder != "" {
			// Only when the message was still queued: the target may have taken
			// it up meanwhile, and then it is running the question.
			if s.dequeuePrompt(to, prompt) {
				drop()
				return AskOutcome{}, deadlockErr(to, holder)
			}
		}
	}
	return s.awaitQuestion(ctx, q, w, timeout)
}

// deadlockErr is the fail-fast error of an ask that could never be answered.
func deadlockErr(target, holder string) error {
	return subagentErr(SubagentUnavailable,
		"target %s cannot start a turn while %s holds the checkout; ask a running agent or end your turn with the question", target, holder)
}

// dequeuePrompt removes the queued message prompt of chat and reports whether
// it was still there.
func (s *Server) dequeuePrompt(chatID, prompt string) bool {
	queued, err := s.repo.GetQueuedMessages(chatID)
	if err != nil {
		return false
	}
	for _, m := range queued {
		if m.Prompt == prompt {
			if err := s.repo.DeleteQueuedMessage(chatID, m.ID); err != nil {
				log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to dequeue a question that cannot be answered")
				return false
			}
			if q, _ := s.repo.GetQueuedMessages(chatID); q != nil {
				s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeQueue, Payload: map[string]any{"queue": q}})
			}
			return true
		}
	}
	return false
}

// cliMayNotInject reports whether a CLI of the agent cannot take live input:
// only claude and agy can. An agent without CLI targets is assumed to inject.
func cliMayNotInject(a *agentspec.Agent) bool {
	for _, t := range a.Config.CLI {
		if t.CLI != "claude" && t.CLI != "agy" {
			return true
		}
	}
	return false
}

// ResumeQuestion keeps waiting for a question callerChat asked earlier; an
// answer that already arrived is returned at once. Only the asker chat can
// resume its own question, and a question has one blocked call at a time: a
// newer ask --resume replaces (and fails) the one still blocked.
func (s *Server) ResumeQuestion(ctx context.Context, callerChat, qid string, timeout time.Duration) (AskOutcome, error) {
	b := &s.questions
	b.mu.Lock()
	b.sweepLocked(false)
	q := b.m[qid]
	if q == nil {
		b.mu.Unlock()
		return AskOutcome{}, subagentErr(SubagentNotFound, "question %q not found: it expired, or the backend restarted (open questions are kept in memory only)", qid)
	}
	if q.asker != callerChat {
		b.mu.Unlock()
		return AskOutcome{}, subagentErr(SubagentDenied, "question %s was not asked by this chat", qid)
	}
	w := q.attachLocked()
	b.mu.Unlock()
	onCallEnd(ctx, s.endWaiterHook(q, w))
	return s.awaitQuestion(ctx, q, w, timeout)
}

// endWaiterHook is the onCallEnd hook of a blocked call: when it took back an
// answer, the cached response of the call becomes the pending form, so a runner
// retry that attaches later does not get the answer a second time (it is
// delivered as a message).
func (s *Server) endWaiterHook(q *question, w *askWaiter) func(bool) proto.Message {
	return func(delivered bool) proto.Message {
		if s.endWaiter(q, w, delivered) {
			return &runnerpb.AskSubagentResponse{Qid: q.qid, State: QuestionStatePending}
		}
		return nil
	}
}

// endWaiter runs when the callback call of waiter w ends. delivered tells
// whether its result reached an attached RPC. An answer the call took but could
// not deliver (the CLI died while blocked; the call itself runs until its own
// timeout) is given back, and endWaiter reports true: it stays stored for
// `ask --resume` and, when it was not delivered as a message because the call
// held it, is delivered as one now.
func (s *Server) endWaiter(q *question, w *askWaiter, delivered bool) bool {
	b := &s.questions
	b.mu.Lock()
	w.closed = true
	if q.waiter == w {
		q.waiter = nil
	}
	var snap *question
	tookBack := !delivered && w.gotAnswer
	if tookBack {
		w.gotAnswer = false
		q.consumed = false
		if q.injectSkipped {
			q.injectSkipped = false
			c := *q
			snap = &c
		}
	}
	b.mu.Unlock()
	if snap != nil {
		go s.deliverAnswerMessage(s.Context(), snap)
	}
	return tookBack
}

// awaitQuestion blocks the call w on q until it is answered or cancelled, the
// timeout passes (the default is defaultAskWait, the cap maxSubagentWait), the
// call is superseded or ctx is done. The hand-over of the answer is decided
// under the book's lock, so an answer is either returned here or delivered as
// a message, never both and never neither: a call whose ctx ended (or whose
// callback call ended, see endWaiter) never takes the answer.
func (s *Server) awaitQuestion(ctx context.Context, q *question, w *askWaiter, timeout time.Duration) (AskOutcome, error) {
	if timeout <= 0 {
		timeout = defaultAskWait
	}
	timeout = min(timeout, maxSubagentWait)
	b := &s.questions
	b.mu.Lock()
	if q.phase == questionOpen && ctx.Err() == nil && !w.closed {
		done := q.done
		b.mu.Unlock()
		timer := time.NewTimer(timeout)
		select {
		case <-done:
		case <-timer.C:
		case <-ctx.Done():
		case <-w.superseded:
		}
		timer.Stop()
		b.mu.Lock()
	}
	if q.waiter == w {
		q.waiter = nil
	}
	select {
	case <-w.superseded:
		b.mu.Unlock()
		return AskOutcome{}, subagentErr(SubagentDenied, "this wait was replaced by a newer `subagent ask --resume %s`", q.qid)
	default:
	}
	over := ctx.Err() != nil || w.closed
	switch {
	case q.phase == questionAnswered && !over:
		q.consumed = true
		w.gotAnswer = true
		out := AskOutcome{QID: q.qid, Answered: true, Answer: q.answer}
		b.mu.Unlock()
		return out, nil
	case q.phase == questionAnswered:
		// The call is over as the answer came: it must still reach the asker
		// chat as a message.
		var snap *question
		if q.injectSkipped && !q.consumed {
			q.injectSkipped = false
			c := *q
			snap = &c
		}
		b.mu.Unlock()
		if snap != nil {
			go s.deliverAnswerMessage(s.Context(), snap)
		}
		return AskOutcome{}, subagentErr(SubagentDenied, "the caller's run ended")
	case q.phase == questionCancelled:
		reason := q.cancelReason
		b.mu.Unlock()
		return AskOutcome{}, subagentErr(SubagentUnavailable, "%s", reason)
	}
	b.mu.Unlock()
	if over {
		return AskOutcome{}, subagentErr(SubagentDenied, "the caller's run ended")
	}
	return AskOutcome{QID: q.qid}, nil
}

// AnswerQuestion answers qid from callerChat, which must be the chat the
// question was addressed to; a question is answered once. A blocked ask/resume
// of the asker gets the answer through that call and nothing is injected.
// Otherwise (it timed out, or its turn ended) the answer is kept for
// `ask --resume` and also delivered to the asker chat as a message, like
// `reply`; the result is that message's delivery, or "stored" when it could not
// be delivered.
func (s *Server) AnswerQuestion(ctx context.Context, callerChat, callerAgent, qid, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", subagentErr(SubagentInvalid, "an answer is required")
	}
	if len(text) > maxSubagentMessageBytes {
		return "", subagentErr(SubagentInvalid, "answer too large (limit %d bytes)", maxSubagentMessageBytes)
	}
	if s.repo == nil {
		return "", subagentErr(SubagentUnavailable, "database unavailable in degraded mode")
	}
	caller, err := s.repo.GetSessionMeta(callerChat)
	if err != nil {
		return "", subagentErr(SubagentInternal, "failed to get session: %v", err)
	}
	if caller == nil {
		return "", subagentErr(SubagentNotFound, "caller chat not found")
	}
	name := s.chatAgentName(caller.RunnerNodeID, callerAgent)

	b := &s.questions
	b.mu.Lock()
	b.sweepLocked(false)
	q := b.m[qid]
	switch {
	case q == nil:
		b.mu.Unlock()
		return "", subagentErr(SubagentNotFound, "question %q not found: it expired, was cancelled, or the backend restarted (open questions are kept in memory only)", qid)
	case q.target != callerChat:
		b.mu.Unlock()
		return "", subagentErr(SubagentDenied, "question %s is not addressed to this chat", qid)
	case q.phase == questionAnswered:
		b.mu.Unlock()
		return "", subagentErr(SubagentInvalid, "question %s was already answered", qid)
	case q.phase == questionCancelled:
		reason := q.cancelReason
		b.mu.Unlock()
		return "", subagentErr(SubagentInvalid, "question %s was cancelled: %s", qid, reason)
	}
	if ctx.Err() != nil {
		b.mu.Unlock()
		return "", subagentErr(SubagentDenied, "the caller is being stopped")
	}
	q.phase, q.answer, q.finishedAt = questionAnswered, text, b.clock()
	q.answerMsg = fmt.Sprintf("[answer %s from %s %s%s]\n%s", qid, name, callerChat, labelSuffix(caller.SpawnLabel), text)
	q.answerSender = fmt.Sprintf("agent %s %s%s", name, callerChat, labelSuffix(caller.SpawnLabel))
	q.answerFrom, q.answerAgentID = callerChat, callerAgent
	blocked := q.waiter != nil
	q.injectSkipped = blocked
	close(q.done)
	snap := *q
	b.mu.Unlock()

	if blocked {
		return AnswerDeliveryUnblocked, nil
	}
	return s.deliverAnswerMessage(ctx, &snap), nil
}

// deliverAnswerMessage delivers the answer of q to the asker chat as a message
// and returns the delivery, or "stored" when it cannot be delivered (the
// answer stays available to `ask --resume`).
func (s *Server) deliverAnswerMessage(ctx context.Context, q *question) string {
	asker, err := s.repo.GetSessionMeta(q.asker)
	if err != nil || asker == nil || s.isStopped(q.asker) || s.subagentInfo(asker).State == SubagentStateKilled {
		return AnswerDeliveryStored
	}
	delivery, err := s.deliverToChat(ctx, q.answerFrom, q.answerAgentID, asker, q.answerMsg, "subagent-answer", q.answerSender)
	if err != nil {
		log.Warn().Err(err).Str("qid", q.qid).Str("asker", q.asker).Msg("failed to deliver the answer of a question; it stays available to `subagent ask --resume`")
		return AnswerDeliveryStored
	}
	return delivery
}

// Questions lists the open questions addressed to callerChat and the
// questions it asked that are pending (or answered but not picked up).
func (s *Server) Questions(callerChat string) (incoming, outgoing []QuestionInfo) {
	b := &s.questions
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked(false)
	for _, q := range b.m {
		switch {
		case q.target == callerChat && q.phase == questionOpen:
			first, _, _ := strings.Cut(q.text, "\n")
			if len(first) > questionFirstLineBytes {
				first = tailFree(first, questionFirstLineBytes)
			}
			incoming = append(incoming, QuestionInfo{QID: q.qid, FromChatID: q.asker, FromAgent: q.askerAgent, FromLabel: q.askerLabel, FirstLine: first, Created: q.created})
		case q.asker == callerChat && q.phase == questionOpen:
			outgoing = append(outgoing, QuestionInfo{QID: q.qid, ToChatID: q.target, State: QuestionStatePending, Created: q.created})
		case q.asker == callerChat && q.phase == questionAnswered && !q.consumed:
			outgoing = append(outgoing, QuestionInfo{QID: q.qid, ToChatID: q.target, State: QuestionStateAnswered, Created: q.created})
		}
	}
	byAge := func(l []QuestionInfo) {
		sort.Slice(l, func(i, j int) bool {
			if l[i].Created.Equal(l[j].Created) {
				return l[i].QID < l[j].QID
			}
			return l[i].Created.Before(l[j].Created)
		})
	}
	byAge(incoming)
	byAge(outgoing)
	return incoming, outgoing
}

// tailFree keeps the first n bytes of s on a rune boundary, with an ellipsis.
func tailFree(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}

// treeMember loads chat id, which must be in the spawn tree of caller (the same
// root: any position, not only a descendant). An unknown chat and one of
// another tree look the same to the caller.
func (s *Server) treeMember(caller *dbmodels.Session, id string) (*dbmodels.Session, error) {
	notFound := subagentErr(SubagentNotFound, "chat %q is not in your spawn tree (find ids with `subagent tree`)", id)
	sess, err := s.repo.GetSessionMeta(id)
	if err != nil {
		return nil, subagentErr(SubagentInternal, "failed to get chat: %v", err)
	}
	if sess == nil {
		return nil, notFound
	}
	callerRoot, err := s.repo.SpawnRootID(caller.ChatID)
	if err != nil || callerRoot == "" {
		return nil, subagentErr(SubagentInternal, "failed to find the root of the spawn tree: %v", err)
	}
	root, err := s.repo.SpawnRootID(id)
	if err != nil || root != callerRoot {
		return nil, notFound
	}
	return sess, nil
}

// askDeadlock decides whether a question to target can never be answered
// because delivering it would need the checkout lock that the asker, or a chat
// above it that is stuck waiting for it, holds. It returns that holder, or ""
// when the delivery is safe. injectable says whether a live run of the target is
// expected to take the question by injection.
//
//   - A live, injectable run takes the question without any lock. The
//     exception is an idle-held target (a parent between turns, waiting for its
//     subagents) while a writable descendant holds the checkout: the injection is
//     refused (its waiting mark cannot be cleared, see prepareHeldInject) and the
//     question would be queued until the hold ends.
//   - A target without a live run, or whose run may not take the injection
//     (opencode, simplest, or an inject that failed: the question is then queued
//     behind its turn), needs a new turn. That takes the lock like any run, or
//     re-enters the lock the target holds, which waits for its descendants that
//     hold it (dirlock Acquire). A target already queued for the lock waits for
//     its holder. Read-only --readonly spawns take no lock.
//
// Only a holder that is stuck on the asker counts (see stuckOnAsker); another
// holder is independent and will finish, so the question is delivered and waits
// for it (bounded by the timeout).
func (s *Server) askDeadlock(asker, target *dbmodels.Session, targetAgent *agentspec.Agent, injectable bool) string {
	if s.dirLocks == nil {
		return ""
	}
	chain := []string{asker.ChatID}
	if anc, err := s.subagentAncestors(asker); err == nil {
		chain = append(chain, anc...)
	}
	stuck := func(id string) bool { return slices.Contains(chain, id) && s.stuckOnAsker(id, asker.ChatID, chain) }

	live := s.activeRuns != nil && s.hasRunFor(target.ChatID, targetAgent.Config.ID)
	switch {
	case live && s.isIdleHeld(target.ChatID):
		descendants, _ := s.descendantChats(target.ChatID)
		for _, id := range descendants {
			if st := s.dirLockStatus(id); st != nil && st.IsLocked && stuck(id) {
				return id
			}
		}
		return ""
	case live && injectable:
		return ""
	case !live:
		if _, executing := s.activeExecutions.Load(target.ChatID); executing {
			// Waiting for the lock (no run yet): it waits for its holder.
			if st := s.dirLockStatus(target.ChatID); st != nil && st.IsWaitingForLock && stuck(st.LockedByChatID) {
				return st.LockedByChatID
			}
			return ""
		}
	}
	if target.SpawnReadonly {
		return ""
	}
	req, ok := s.turnLockRequest(target, targetAgent)
	if !ok {
		return ""
	}
	if holder, blocked := s.dirLocks.Blocker(req); blocked && stuck(holder) {
		return holder
	}
	return ""
}

// stuckOnAsker reports whether the lock holder id (in the asker's chain) can
// not let go while the asker is blocked in ask: the asker itself, a chat that
// waits for its subagents (marked in the lock manager, it waits for the asker
// below it), a chat whose turn ended with the lock parked, or a chat blocked in
// an ask toward someone in the chain. An ancestor that is merely busy running
// will finish or start waiting, so it is not.
func (s *Server) stuckOnAsker(id, asker string, chain []string) bool {
	if id == asker || s.chatMarked(id) {
		return true
	}
	if _, executing := s.activeExecutions.Load(id); !executing {
		return true
	}
	b := &s.questions
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, q := range b.m {
		if q.phase == questionOpen && q.waiter != nil && q.asker == id && slices.Contains(chain, q.target) {
			return true
		}
	}
	return false
}

// askCycle returns a chat of a wait-for cycle that asking target would close:
// the chats blocked in an ask form a graph (asker -> target of its open,
// blocked questions); if target reaches callerChat in it, target cannot answer.
func (s *Server) askCycle(callerChat, target string) string {
	b := &s.questions
	b.mu.Lock()
	defer b.mu.Unlock()
	seen := map[string]bool{}
	var walk func(chat string) string
	walk = func(chat string) string {
		if seen[chat] {
			return ""
		}
		seen[chat] = true
		for _, q := range b.m {
			if q.phase != questionOpen || q.waiter == nil || q.asker != chat {
				continue
			}
			if q.target == callerChat {
				return chat
			}
			if hit := walk(q.target); hit != "" {
				return hit
			}
		}
		return ""
	}
	return walk(target)
}

// cancelIncomingOnRunEnd cancels the open questions addressed to chatID when its
// run ended for good (nothing left to run): the agent that could have answered
// is gone. An idle-held run is not an end (its guard stays held), and a chat
// with queued work is about to run again.
func (s *Server) cancelIncomingOnRunEnd(chatID string) {
	if s.repo == nil {
		return
	}
	if _, running := s.activeExecutions.Load(chatID); running {
		return
	}
	// Any queued message, parked for a runner reconnect or not, runs again.
	if head, err := s.repo.PeekNextQueuedMessage(chatID); err != nil || head != nil {
		return
	}
	b := &s.questions
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, q := range b.m {
		if q.phase == questionOpen && q.target == chatID && !q.delivering {
			b.cancelLocked(q, fmt.Sprintf("target %s finished without answering; ask again or ask someone else", chatID))
		}
	}
}

// hasRunFor reports whether the chat has a registered run of the agent.
func (s *Server) hasRunFor(chatID, agentID string) bool {
	_, _, ok := s.activeRuns.Lookup(chatID, agentID)
	return ok
}

// turnLockRequest is the lock request a new turn of target would make, derived
// like the executor does (run directory, worktree directory, mode, ancestors).
// ok is false when it cannot be derived.
func (s *Server) turnLockRequest(target *dbmodels.Session, agent *agentspec.Agent) (dirlock.AcquireRequest, bool) {
	rawRunDir := target.RunDir
	wtChat := ""
	if agent.Config.Worktree.Enabled {
		if target.WorktreeChatID != "" {
			wtChat = target.WorktreeChatID
		} else if target.Worktree {
			wtChat = target.ChatID
		}
	}
	if rawRunDir == "" && wtChat != "" {
		rawRunDir = agent.Config.DefaultRunDir()
	}
	if rawRunDir == "" && len(agent.Config.RunDirs) > 0 {
		rawRunDir = agent.Config.RunDirs[0]
	}
	lockDir := NormalizeSessionRunDir(rawRunDir, target.ChatID)
	if wtChat != "" {
		if lockDir == "" {
			return dirlock.AcquireRequest{}, false
		}
		p, err := worktree.PathFor(agent.Config.Worktree.Root, lockDir, wtChat)
		if err != nil {
			return dirlock.AcquireRequest{}, false
		}
		lockDir = p
	}
	ancestors, err := s.subagentAncestors(target)
	if err != nil {
		return dirlock.AcquireRequest{}, false
	}
	dirs, mode := dirlock.ComputeLockedDirs(target.ChatID, lockDir, &agent.Config)
	return dirlock.AcquireRequest{ChatID: target.ChatID, RunnerID: target.RunnerNodeID, Dirs: dirs, Mode: mode, Ancestors: ancestors}, true
}
