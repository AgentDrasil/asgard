package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// askRig is a spawn tree whose root (rigChat, agent alpha) and first child
// (agent beta, label "work") both run a turn that takes injections.
type askRig struct {
	*callbackRig
	c     *ctrlRuns
	child string
	inj   *fakeInjector
	runs  *dispatch.ActiveRuns
	// endChildRun unregisters the live run of the child.
	endChildRun func()
}

func newAskRig(t *testing.T) *askRig {
	t.Helper()
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	holdChat(t, rig)
	inj := &fakeInjector{fn: func(string) (bool, error) { return true, nil }}
	runs := dispatch.NewActiveRuns()
	runs.Register(rigChat, "alpha", "node", "run-root")
	endChild := runs.Register(child, "beta", "node", "run-child")
	rig.server.injector = inj
	rig.server.activeRuns = runs
	return &askRig{callbackRig: rig, c: c, child: child, inj: inj, runs: runs, endChildRun: endChild}
}

type askResult struct {
	out AskOutcome
	err error
}

func (r *askRig) askAsync(from, agent, to, text string, timeout time.Duration) chan askResult {
	ch := make(chan askResult, 1)
	go func() {
		out, err := r.server.AskSubagent(context.Background(), from, agent, to, text, timeout)
		ch <- askResult{out, err}
	}()
	return ch
}

func getAsk(t *testing.T, ch chan askResult) askResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("ask never returned")
		return askResult{}
	}
}

// openQID waits until chat has an open incoming question and returns its id.
func (r *askRig) openQID(t *testing.T, chat string) string {
	t.Helper()
	var qid string
	require.Eventually(t, func() bool {
		in, _ := r.server.Questions(chat)
		if len(in) == 0 {
			return false
		}
		qid = in[len(in)-1].QID
		return true
	}, 5*time.Second, 5*time.Millisecond)
	return qid
}

func (r *askRig) sent() []string {
	r.inj.mu.Lock()
	defer r.inj.mu.Unlock()
	return append([]string(nil), r.inj.got...)
}

func subagentErrKind(t *testing.T, err error) SubagentErrorKind {
	t.Helper()
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	return se.Kind
}

func TestAskAnsweredWhileBlocked(t *testing.T) {
	r := newAskRig(t)
	ch := r.askAsync(rigChat, "alpha", r.child, "which branch?\nsecond line", 0)
	qid := r.openQID(t, r.child)

	assert.Equal(t, []string{"[question " + qid + " from alpha " + rigChat + "]\nwhich branch?\nsecond line\nAnswer with: subagent answer " + qid + " -- <text>"}, r.sent())
	// The question appears in the child's transcript like any injected message.
	full, err := r.repo.GetSession(r.child)
	require.NoError(t, err)
	assert.Contains(t, full.Messages[len(full.Messages)-1].Content, "[question "+qid)

	got, err := r.server.AnswerQuestion(context.Background(), r.child, "beta", qid, "main")
	require.NoError(t, err)
	assert.Equal(t, AnswerDeliveryUnblocked, got)
	res := getAsk(t, ch)
	require.NoError(t, res.err)
	assert.Equal(t, AskOutcome{QID: qid, Answered: true, Answer: "main"}, res.out)
	// The answer went through the blocked call only: nothing was injected.
	assert.Len(t, r.sent(), 1)
}

func TestAskMessageNamesTheAskersLabel(t *testing.T) {
	r := newAskRig(t)
	gc := spawnGrandchild(t, r.callbackRig, r.c, r.child)
	r.runs.Register(gc, "gamma", "node", "run-gc")
	// The grandchild (label "deep") asks its uncle-less ancestor chain: the child.
	ch := r.askAsync(gc, "gamma", r.child, "ready?", 50*time.Millisecond)
	qid := r.openQID(t, r.child)
	assert.Equal(t, "[question "+qid+" from gamma "+gc+" (deep)]\nready?\nAnswer with: subagent answer "+qid+" -- <text>", r.sent()[0])
	assert.False(t, getAsk(t, ch).out.Answered)
}

func TestAskTimeoutThenResumeGetsTheAnswer(t *testing.T) {
	r := newAskRig(t)
	res := getAsk(t, r.askAsync(rigChat, "alpha", r.child, "ping", 50*time.Millisecond))
	require.NoError(t, res.err)
	assert.False(t, res.out.Answered)
	qid := res.out.QID
	require.NotEmpty(t, qid)

	_, out := r.server.Questions(rigChat)
	require.Len(t, out, 1)
	assert.Equal(t, QuestionInfo{QID: qid, ToChatID: r.child, State: QuestionStatePending, Created: out[0].Created}, out[0])

	// Nobody is blocked: the answer is delivered to the asker as a message and
	// kept for resume.
	got, err := r.server.AnswerQuestion(context.Background(), r.child, "beta", qid, "late answer")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryInjected, got)
	msgs := r.sent()
	require.Len(t, msgs, 2)
	assert.Equal(t, "[answer "+qid+" from beta "+r.child+" (work)]\nlate answer", msgs[1])
	_, out = r.server.Questions(rigChat)
	require.Len(t, out, 1)
	assert.Equal(t, QuestionStateAnswered, out[0].State)

	res = getAsk(t, startResume(r, rigChat, qid, 0))
	require.NoError(t, res.err)
	assert.Equal(t, AskOutcome{QID: qid, Answered: true, Answer: "late answer"}, res.out)
	_, out = r.server.Questions(rigChat)
	assert.Empty(t, out, "picked up")
}

func startResume(r *askRig, chat, qid string, timeout time.Duration) chan askResult {
	ch := make(chan askResult, 1)
	go func() {
		out, err := r.server.ResumeQuestion(context.Background(), chat, qid, timeout)
		ch <- askResult{out, err}
	}()
	return ch
}

func TestResumeBlocksUntilAnswered(t *testing.T) {
	r := newAskRig(t)
	res := getAsk(t, r.askAsync(rigChat, "alpha", r.child, "ping", 30*time.Millisecond))
	qid := res.out.QID
	ch := startResume(r, rigChat, qid, 0)
	require.Eventually(t, func() bool {
		r.server.questions.mu.Lock()
		defer r.server.questions.mu.Unlock()
		return r.server.questions.m[qid].waiter != nil
	}, 5*time.Second, 5*time.Millisecond)

	got, err := r.server.AnswerQuestion(context.Background(), r.child, "beta", qid, "now")
	require.NoError(t, err)
	assert.Equal(t, AnswerDeliveryUnblocked, got)
	res = getAsk(t, ch)
	require.NoError(t, res.err)
	assert.Equal(t, "now", res.out.Answer)
	assert.Len(t, r.sent(), 1, "a blocked resume gets the answer without a message")
}

func TestOnlyTheAskerCanResume(t *testing.T) {
	r := newAskRig(t)
	res := getAsk(t, r.askAsync(rigChat, "alpha", r.child, "ping", 30*time.Millisecond))
	_, err := r.server.ResumeQuestion(context.Background(), r.child, res.out.QID, time.Millisecond)
	require.Error(t, err)
	assert.Equal(t, SubagentDenied, subagentErrKind(t, err))
	_, err = r.server.ResumeQuestion(context.Background(), rigChat, "no-such-question", time.Millisecond)
	require.Error(t, err)
	assert.Equal(t, SubagentNotFound, subagentErrKind(t, err))
	assert.Contains(t, err.Error(), "backend restarted")
}

func TestOnlyTheTargetCanAnswerAndOnlyOnce(t *testing.T) {
	r := newAskRig(t)
	other := spawnOne(t, r.callbackRig, r.c)
	r.runs.Register(other, "beta", "node", "run-other")
	ch := r.askAsync(rigChat, "alpha", r.child, "ping", 0)
	qid := r.openQID(t, r.child)

	for _, who := range []string{rigChat, other} {
		_, err := r.server.AnswerQuestion(context.Background(), who, "alpha", qid, "mine")
		require.Error(t, err, who)
		assert.Equal(t, SubagentDenied, subagentErrKind(t, err), who)
	}
	_, err := r.server.AnswerQuestion(context.Background(), r.child, "beta", qid, "   ")
	assert.Equal(t, SubagentInvalid, subagentErrKind(t, err))

	_, err = r.server.AnswerQuestion(context.Background(), r.child, "beta", qid, "first")
	require.NoError(t, err)
	_, err = r.server.AnswerQuestion(context.Background(), r.child, "beta", qid, "second")
	require.Error(t, err)
	assert.Equal(t, SubagentInvalid, subagentErrKind(t, err))
	assert.Contains(t, err.Error(), "already answered")
	res := getAsk(t, ch)
	assert.Equal(t, "first", res.out.Answer, "the first answer stands")
}

func TestAskRefusesOtherTreesSelfAndBadIDs(t *testing.T) {
	r := newAskRig(t)
	saveTreeChat(t, r.callbackRig, "other-root", "", "alpha", "", false)
	saveTreeChat(t, r.callbackRig, "other-child", "other-root", "beta", "x", false)
	ctx := context.Background()

	for _, to := range []string{"other-root", "other-child", "no-such-chat"} {
		_, err := r.server.AskSubagent(ctx, rigChat, "alpha", to, "hi", time.Millisecond)
		require.Error(t, err, to)
		assert.Equal(t, SubagentNotFound, subagentErrKind(t, err), to)
	}
	// From the other tree the same ids of this tree are invisible too.
	_, err := r.server.AskSubagent(ctx, "other-child", "beta", r.child, "hi", time.Millisecond)
	assert.Equal(t, SubagentNotFound, subagentErrKind(t, err))

	_, err = r.server.AskSubagent(ctx, rigChat, "alpha", rigChat, "hi", time.Millisecond)
	assert.Equal(t, SubagentInvalid, subagentErrKind(t, err))
	assert.Contains(t, err.Error(), "yourself")
	_, err = r.server.AskSubagent(ctx, rigChat, "alpha", r.child, "  ", time.Millisecond)
	assert.Equal(t, SubagentInvalid, subagentErrKind(t, err))
	_, err = r.server.AskSubagent(ctx, rigChat, "alpha", "bad id!", "hi", time.Millisecond)
	assert.Equal(t, SubagentInvalid, subagentErrKind(t, err))
	assert.Len(t, r.sent(), 0, "nothing was delivered")
}

func TestAskReachesAncestorsAndSiblings(t *testing.T) {
	r := newAskRig(t)
	sibling := spawnOne(t, r.callbackRig, r.c)
	r.runs.Register(sibling, "beta", "node", "run-sib")

	// A child asks the root (an ancestor).
	up := r.askAsync(r.child, "beta", rigChat, "need a decision", 0)
	qid := r.openQID(t, rigChat)
	_, err := r.server.AnswerQuestion(context.Background(), rigChat, "alpha", qid, "go")
	require.NoError(t, err)
	assert.Equal(t, "go", getAsk(t, up).out.Answer)

	// A child asks its sibling.
	side := r.askAsync(r.child, "beta", sibling, "your view?", 0)
	qid = r.openQID(t, sibling)
	_, err = r.server.AnswerQuestion(context.Background(), sibling, "beta", qid, "fine")
	require.NoError(t, err)
	assert.Equal(t, "fine", getAsk(t, side).out.Answer)
}

func TestKilledTargetFailsABlockedAsk(t *testing.T) {
	r := newAskRig(t)
	ch := r.askAsync(rigChat, "alpha", r.child, "ping", 0)
	r.openQID(t, r.child)

	_, err := r.server.KillSubagent(rigChat, r.child)
	require.NoError(t, err)
	res := getAsk(t, ch)
	require.Error(t, res.err)
	assert.Equal(t, "target "+r.child+" was killed", res.err.Error())

	// A new ask to a killed chat fails at once, and so does an answer from it.
	_, err = r.server.AskSubagent(context.Background(), rigChat, "alpha", r.child, "again", time.Second)
	require.Error(t, err)
	assert.Equal(t, "target "+r.child+" was killed", err.Error())
	in, out := r.server.Questions(r.child)
	assert.Empty(t, in)
	assert.Empty(t, out)
}

func TestKilledAskerCancelsItsQuestions(t *testing.T) {
	r := newAskRig(t)
	ch := r.askAsync(r.child, "beta", rigChat, "ping", 0)
	qid := r.openQID(t, rigChat)

	_, err := r.server.KillSubagent(rigChat, r.child)
	require.NoError(t, err)
	res := getAsk(t, ch)
	require.Error(t, res.err)
	assert.Contains(t, res.err.Error(), "cancelled")
	in, _ := r.server.Questions(rigChat)
	assert.Empty(t, in, "the open question is gone")
	_, err = r.server.AnswerQuestion(context.Background(), rigChat, "alpha", qid, "too late")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
}

func TestAskWakesTheTargetsWait(t *testing.T) {
	r := newAskRig(t)
	gc := spawnGrandchild(t, r.callbackRig, r.c, r.child)
	wait := startWaitOutcomeAs(r.callbackRig, r.child, "run-child", []string{gc}, false, 0)
	waitRegistered(t, r.callbackRig)

	ch := r.askAsync(rigChat, "alpha", r.child, "quick one", 50*time.Millisecond)
	w := getWaitOutcome(t, wait)
	require.NoError(t, w.err)
	assert.Equal(t, []string{"agent alpha " + rigChat}, w.o.WokenBy)
	require.Len(t, w.o.Results, 1)
	assert.True(t, w.o.Results[0].StillRunning)
	getAsk(t, ch)
}

func TestQueuedQuestionDoesNotWakeTheTargetsWait(t *testing.T) {
	r := newAskRig(t)
	r.inj.fn = func(string) (bool, error) { return false, nil } // opencode: no live input
	gc := spawnGrandchild(t, r.callbackRig, r.c, r.child)
	wait := startWaitOutcomeAs(r.callbackRig, r.child, "run-child", []string{gc}, false, 200*time.Millisecond)
	waitRegistered(t, r.callbackRig)

	got := getAsk(t, r.askAsync(rigChat, "alpha", r.child, "later", 30*time.Millisecond))
	require.NoError(t, got.err)
	assert.False(t, got.out.Answered)
	w := getWaitOutcome(t, wait)
	assert.Empty(t, w.o.WokenBy)
	assert.Nil(t, unreadOf(r.callbackRig, r.child))
}

func TestAskToAnIdleChildStartsAResumedTurnAndCountsTowardTheCap(t *testing.T) {
	r := newAskRig(t)
	r.c.finish(r.child, nil)
	waitIdle(t, r.callbackRig, r.child)
	// The finished child has no live run any more.
	r.endChildRun()

	// Resumed like `send`.
	res := getAsk(t, r.askAsync(rigChat, "alpha", r.child, "are you there?", 50*time.Millisecond))
	require.NoError(t, res.err)
	assert.Equal(t, r.child, waitStarted(t, r.c.started))
	prompts := r.c.promptsOf(r.child)
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "[question "+res.out.QID+" from alpha "+rigChat+"]")
	r.c.finish(r.child, nil)
	waitIdle(t, r.callbackRig, r.child)

	// Four other children run: the cap of the parent is reached, so the idle
	// child cannot be restarted, as with `send`.
	for range maxRunningSubagents {
		spawnOne(t, r.callbackRig, r.c)
	}
	_, err := r.server.AskSubagent(context.Background(), rigChat, "alpha", r.child, "once more", time.Second)
	require.Error(t, err)
	assert.Equal(t, SubagentLimit, subagentErrKind(t, err))
	// The refused question is not left open.
	_, out := r.server.Questions(rigChat)
	for _, q := range out {
		assert.NotEqual(t, "once more", q.FirstLine)
	}
	assert.Empty(t, out, "the first question was cancelled when the child finished without answering")
}

func TestAskCapsAndTTL(t *testing.T) {
	r := newAskRig(t)
	now := time.Now()
	r.server.questions.now = func() time.Time { return now }
	var qids []string
	for i := range maxOpenQuestionsPerAsker {
		res := getAsk(t, r.askAsync(rigChat, "alpha", r.child, "q", time.Millisecond))
		require.NoError(t, res.err, i)
		qids = append(qids, res.out.QID)
	}
	_, err := r.server.AskSubagent(context.Background(), rigChat, "alpha", r.child, "one too many", time.Millisecond)
	require.Error(t, err)
	assert.Equal(t, SubagentLimit, subagentErrKind(t, err))
	// Another asker is not affected by the per-asker cap.
	res := getAsk(t, r.askAsync(r.child, "beta", rigChat, "q", time.Millisecond))
	require.NoError(t, res.err)

	// Answered entries are dropped after the TTL.
	_, err = r.server.AnswerQuestion(context.Background(), r.child, "beta", qids[0], "ok")
	require.NoError(t, err)
	now = now.Add(questionAnsweredTTL + time.Minute)
	_, err = r.server.ResumeQuestion(context.Background(), rigChat, qids[0], time.Millisecond)
	require.Error(t, err)
	assert.Equal(t, SubagentNotFound, subagentErrKind(t, err))
	// Unanswered ones expire after the open TTL.
	now = now.Add(questionOpenTTL)
	_, err = r.server.ResumeQuestion(context.Background(), rigChat, qids[1], time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestQuestionsListsIncomingAndOutgoing(t *testing.T) {
	r := newAskRig(t)
	getAsk(t, r.askAsync(rigChat, "alpha", r.child, "first line\nsecond line", time.Millisecond))
	in, out := r.server.Questions(r.child)
	require.Len(t, in, 1)
	assert.Equal(t, rigChat, in[0].FromChatID)
	assert.Equal(t, "alpha", in[0].FromAgent)
	assert.Equal(t, "first line", in[0].FirstLine)
	assert.Empty(t, out)
	in, out = r.server.Questions(rigChat)
	assert.Empty(t, in)
	require.Len(t, out, 1)
	assert.Equal(t, r.child, out[0].ToChatID)
}

// ---- idempotency through the callback service ------------------------------

func TestAskIsIdempotentPerCallSeq(t *testing.T) {
	r := newAskRig(t)
	req := &runnerpb.AskSubagentRequest{Meta: meta(rigRun, 7), To: r.child, Text: "ping", TimeoutMs: 30}
	first, err := r.svc.AskSubagent(r.ctx(), req)
	require.NoError(t, err)
	assert.Equal(t, QuestionStatePending, first.GetState())

	// A transport retry of the same call gets the same result and creates no
	// second question.
	again, err := r.svc.AskSubagent(r.ctx(), req)
	require.NoError(t, err)
	assert.Equal(t, first.GetQid(), again.GetQid())
	assert.Len(t, r.sent(), 1)
	in, _ := r.server.Questions(r.child)
	assert.Len(t, in, 1)

	// A new call_seq is a new question.
	second, err := r.svc.AskSubagent(r.ctx(), &runnerpb.AskSubagentRequest{Meta: meta(rigRun, 8), To: r.child, Text: "ping", TimeoutMs: 30})
	require.NoError(t, err)
	assert.NotEqual(t, first.GetQid(), second.GetQid())

	// Answering is idempotent too: the retry returns the first delivery
	// instead of "already answered".
	areq := &runnerpb.AnswerSubagentRequest{Meta: meta("run-child-cb", 1), Qid: first.GetQid(), Text: "yes"}
	r.callbackRig.runs.add(runnerhub.RunInfo{RunID: "run-child-cb", NodeID: rigNode, ChatID: r.child, AgentID: "beta", Done: r.done})
	a1, err := r.svc.AnswerSubagent(r.ctx(), areq)
	require.NoError(t, err)
	a2, err := r.svc.AnswerSubagent(r.ctx(), areq)
	require.NoError(t, err)
	assert.Equal(t, a1.GetDelivery(), a2.GetDelivery())

	// Resume through the service returns the stored answer.
	res, err := r.svc.AskSubagent(r.ctx(), &runnerpb.AskSubagentRequest{Meta: meta(rigRun, 9), ResumeQid: first.GetQid(), TimeoutMs: 30})
	require.NoError(t, err)
	assert.Equal(t, QuestionStateAnswered, res.GetState())
	assert.Equal(t, "yes", res.GetAnswer())
}

func TestSubagentQuestionsCallback(t *testing.T) {
	r := newAskRig(t)
	getAsk(t, r.askAsync(rigChat, "alpha", r.child, "hello", time.Millisecond))
	out, err := r.svc.SubagentQuestions(r.ctx(), &runnerpb.SubagentQuestionsRequest{Meta: meta(rigRun, 1)})
	require.NoError(t, err)
	require.Len(t, out.GetOutgoing(), 1)
	assert.Equal(t, r.child, out.GetOutgoing()[0].GetToChatId())
	assert.Equal(t, QuestionStatePending, out.GetOutgoing()[0].GetState())
	assert.Empty(t, out.GetIncoming())
}

func TestAskUserDenyTextPointsAtAsk(t *testing.T) {
	r := newAskRig(t)
	msg := r.server.askUserRefusal(r.child, nil)
	assert.Contains(t, msg, "subagent ask --to <chat-id>")
	assert.Contains(t, msg, "subagent tree")
}

// ---- deadlock analysis -----------------------------------------------------

// lockTree is a tree where the root waits for its writable children (it is
// marked waiting, as in `subagent wait`) and the asker "asker" holds the
// checkout lock.
type lockTree struct {
	*callbackRig
	c      *ctrlRuns
	runs   *dispatch.ActiveRuns
	inj    *fakeInjector
	relAsk func(bool)
}

func saveLockChat(t *testing.T, rig *callbackRig, id, parent string, readonly bool) {
	t.Helper()
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{
		ChatID: id, CurrentAgent: "beta", ParentChatID: parent, SpawnReadonly: readonly, RunDir: "/work/repo", SpawnLabel: id,
	}))
}

func newLockTree(t *testing.T) *lockTree {
	t.Helper()
	rig, c := reportRig(t)
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "alpha", RunDir: "/work/repo"}))
	inj := &fakeInjector{fn: func(string) (bool, error) { return true, nil }}
	runs := dispatch.NewActiveRuns()
	rig.server.injector = inj
	rig.server.activeRuns = runs

	// The root holds the lock and waits on its descendants.
	relRoot, err := rig.server.dirLocks.Acquire(t.Context(), lockReq(rigChat))
	require.NoError(t, err)
	t.Cleanup(func() { relRoot(false) })
	require.NoError(t, rig.server.dirLocks.SetWaitingOnDescendants(rigChat, true))

	saveLockChat(t, rig, "asker", rigChat, false)
	relAsk, err := rig.server.dirLocks.Acquire(t.Context(), lockReq("asker", rigChat))
	require.NoError(t, err)
	t.Cleanup(func() { relAsk(false) })
	return &lockTree{callbackRig: rig, c: c, runs: runs, inj: inj, relAsk: relAsk}
}

func (l *lockTree) ask(to string) (AskOutcome, error) {
	return l.server.AskSubagent(context.Background(), "asker", "beta", to, "need you", 30*time.Millisecond)
}

// A target that needs a new turn needs the lock the asker holds: fail fast.
func TestAskFailsFastWhenTheTargetCannotStartATurn(t *testing.T) {
	l := newLockTree(t)
	saveLockChat(t, l.callbackRig, "sibling", rigChat, false)

	start := time.Now()
	_, err := l.ask("sibling")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second, "it must not wait for the timeout")
	assert.Equal(t, SubagentUnavailable, subagentErrKind(t, err))
	assert.Equal(t, "target sibling cannot start a turn while asker holds the checkout; ask a running agent or end your turn with the question", err.Error())
	assert.Len(t, l.inj.got, 0)
	_, out := l.server.Questions("asker")
	assert.Empty(t, out, "no question is left behind")
	in, _ := l.server.Questions("sibling")
	assert.Empty(t, in)
}

// A readonly spawn takes no lock, so it can be started.
func TestAskToAReadonlySiblingIsFine(t *testing.T) {
	l := newLockTree(t)
	saveLockChat(t, l.callbackRig, "sibling", rigChat, true)
	res, err := l.ask("sibling")
	require.NoError(t, err)
	assert.False(t, res.Answered, "delivered and waiting for the answer")
	assert.Equal(t, "sibling", waitStarted(t, l.c.started))
}

// A running target takes the question by injection, which needs no lock.
func TestAskToARunningSiblingNeedsNoLock(t *testing.T) {
	l := newLockTree(t)
	saveLockChat(t, l.callbackRig, "sibling", rigChat, false)
	l.runs.Register("sibling", "beta", "node", "run-sib")
	_, handle, loaded := l.server.beginExecution("sibling", "beta", false) // a live run has an execution
	require.False(t, loaded)
	t.Cleanup(func() { l.server.activeExecutions.Delete("sibling"); handle.cancel() })
	res, err := l.ask("sibling")
	require.NoError(t, err)
	assert.False(t, res.Answered)
	assert.Len(t, l.inj.got, 1)
}

// A target that already waits for the lock held by the asker waits for it.
func TestAskFailsFastWhenTheTargetWaitsForTheAskersLock(t *testing.T) {
	l := newLockTree(t)
	saveLockChat(t, l.callbackRig, "sibling", rigChat, false)
	// sibling is executing but queued for the lock (no run yet).
	_, handle, loaded := l.server.beginExecution("sibling", "beta", false)
	require.False(t, loaded)
	t.Cleanup(func() { l.server.activeExecutions.Delete("sibling"); handle.cancel() })
	queued := acquireAsync(l.callbackRig, t.Context(), lockReq("sibling", rigChat))
	expectQueued(t, l.callbackRig, "sibling")
	_ = queued

	_, err := l.ask("sibling")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot start a turn while asker holds the checkout")
}

// An idle-held parent (waiting between turns for its subagents) cannot take an
// injection while a writable descendant holds the checkout: its waiting mark
// cannot be cleared, so the message would queue until the hold ends. When that
// descendant is the asker, fail fast.
func TestAskFailsFastForAnIdleHeldTargetWhoseDescendantHoldsTheCheckout(t *testing.T) {
	l := newLockTree(t)
	holdChat(t, l.callbackRig)
	l.runs.Register(rigChat, "alpha", "node", "run-root")
	// The root is idle-held and marked (the setup above marked it already).
	st := l.server.holdStateLocked(rigChat, "run-root", "alpha")
	st.holding, st.idle = true, true
	l.server.holdWaitMark(rigChat, st.reg)

	// Premise: the injection really is refused.
	ready, _, _ := l.server.prepareHeldInjectIdle(rigChat)
	assert.False(t, ready, "the mark cannot be cleared while a writable child holds the checkout")

	_, err := l.ask(rigChat)
	require.Error(t, err)
	assert.Equal(t, "target "+rigChat+" cannot start a turn while asker holds the checkout; ask a running agent or end your turn with the question", err.Error())
	assert.Len(t, l.inj.got, 0)

	// Without a writable descendant holding the checkout the injection works
	// and needs no lock.
	l.relAsk(false)
	res, err := l.ask(rigChat)
	require.NoError(t, err)
	assert.False(t, res.Answered)
	assert.Len(t, l.inj.got, 1)
}

// A holder outside the asker's chain is independent and will finish: the
// question is delivered and waits for it.
func TestAskIsNotRefusedForAnIndependentHolder(t *testing.T) {
	l := newLockTree(t)
	saveLockChat(t, l.callbackRig, "sibling", rigChat, false)
	l.relAsk(false)
	saveLockChat(t, l.callbackRig, "other", rigChat, false)
	relOther, err := l.server.dirLocks.Acquire(t.Context(), lockReq("other", rigChat))
	require.NoError(t, err)
	defer relOther(false)
	saveLockChat(t, l.callbackRig, "asker2", rigChat, true)

	res, err := l.server.AskSubagent(context.Background(), "asker2", "beta", "sibling", "hi", 30*time.Millisecond)
	require.NoError(t, err)
	assert.False(t, res.Answered)
}

func TestBlockerOfTheDirlock(t *testing.T) {
	m := dirlock.NewManager(func(string) {})
	rel, err := m.Acquire(t.Context(), lockReq("a"))
	require.NoError(t, err)
	defer rel(false)
	holder, blocked := m.Blocker(lockReq("b"))
	assert.True(t, blocked)
	assert.Equal(t, "a", holder)
	_, blocked = m.Blocker(dirlock.AcquireRequest{ChatID: "c", Dirs: []string{"/elsewhere"}})
	assert.False(t, blocked)
	_, blocked = m.Blocker(dirlock.AcquireRequest{ChatID: "c"})
	assert.False(t, blocked, "no dirs, no lock")
	_, blocked = m.Blocker(lockReq("a"))
	assert.False(t, blocked, "re-entering its own lock")
	// A waiting ancestor does not block its descendant.
	require.NoError(t, m.SetWaitingOnDescendants("a", true))
	_, blocked = m.Blocker(lockReq("b", "a"))
	assert.False(t, blocked)
}

// ---- review follow-ups -----------------------------------------------------

func (r *askRig) askViaSvc(ctx context.Context, seq uint64, text string, timeoutMs int64) chan error {
	ch := make(chan error, 1)
	go func() {
		_, err := r.svc.AskSubagent(ctx, &runnerpb.AskSubagentRequest{Meta: meta(rigRun, seq), To: r.child, Text: text, TimeoutMs: timeoutMs})
		ch <- err
	}()
	return ch
}

// The CLI dies while blocked: the answer must not vanish into the orphaned call.
func TestAnswerIsDeliveredAsAMessageWhenTheCLIDiesWhileBlocked(t *testing.T) {
	r := newAskRig(t)
	rpcCtx, cancel := context.WithCancel(r.ctx())
	ch := r.askViaSvc(rpcCtx, 1, "ping", 60000)
	qid := r.openQID(t, r.child)

	cancel() // the sandbox command was killed
	require.Error(t, <-ch)

	// The orphaned call keeps running until its own timeout, so it still looks
	// blocked: the answer is handed to it, but nobody is attached to take it, so
	// it comes back as a message.
	got, err := r.server.AnswerQuestion(context.Background(), r.child, "beta", qid, "42")
	require.NoError(t, err)
	assert.Equal(t, AnswerDeliveryUnblocked, got)
	require.Eventually(t, func() bool {
		for _, m := range r.sent() {
			if strings.HasPrefix(m, "[answer "+qid+" from beta "+r.child+" (work)]\n42") {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	// It also stays stored for resume.
	res := getAsk(t, startResume(r, rigChat, qid, 0))
	require.NoError(t, res.err)
	assert.Equal(t, "42", res.out.Answer)
	// A runner retry of the cancelled call (same call_seq) must not get the
	// answer a second time: it is pending, and the message carries the answer.
	again, err := r.svc.AskSubagent(r.ctx(), &runnerpb.AskSubagentRequest{Meta: meta(rigRun, 1), To: r.child, Text: "ping", TimeoutMs: 60000})
	require.NoError(t, err)
	assert.Equal(t, QuestionStatePending, again.GetState())
	assert.Equal(t, qid, again.GetQid())
	assert.Empty(t, again.GetAnswer())
}

// An answer that comes back while the question is still being delivered goes to
// the blocked asker only.
func TestAnswerDuringDeliveryIsNotAlsoInjected(t *testing.T) {
	r := newAskRig(t)
	r.inj.fn = func(prompt string) (bool, error) {
		if strings.HasPrefix(prompt, "[question ") {
			qid := strings.Fields(prompt)[1]
			got, err := r.server.AnswerQuestion(context.Background(), r.child, "beta", qid, "instant")
			assert.NoError(t, err)
			assert.Equal(t, AnswerDeliveryUnblocked, got)
		}
		return true, nil
	}
	res := getAsk(t, r.askAsync(rigChat, "alpha", r.child, "quick", 0))
	require.NoError(t, res.err)
	assert.Equal(t, "instant", res.out.Answer)
	assert.Len(t, r.sent(), 1, "no answer message")
}

func TestOneBlockedCallPerQuestion(t *testing.T) {
	r := newAskRig(t)
	res := getAsk(t, r.askAsync(rigChat, "alpha", r.child, "ping", 20*time.Millisecond))
	qid := res.out.QID
	first := startResume(r, rigChat, qid, 0)
	require.Eventually(t, func() bool {
		r.server.questions.mu.Lock()
		defer r.server.questions.mu.Unlock()
		return r.server.questions.m[qid].waiter != nil
	}, 5*time.Second, 5*time.Millisecond)
	second := startResume(r, rigChat, qid, 0)
	old := getAsk(t, first)
	require.Error(t, old.err)
	assert.Contains(t, old.err.Error(), "replaced")

	got, err := r.server.AnswerQuestion(context.Background(), r.child, "beta", qid, "yes")
	require.NoError(t, err)
	assert.Equal(t, AnswerDeliveryUnblocked, got)
	assert.Equal(t, "yes", getAsk(t, second).out.Answer)
}

func TestBlockingCallsHaveTheirOwnCap(t *testing.T) {
	r := newAskRig(t)
	r.svc.maxBlocking = 1
	first := r.askViaSvc(r.ctx(), 1, "one", 60000)
	qid := r.openQID(t, r.child)
	_, err := r.svc.AskSubagent(r.ctx(), &runnerpb.AskSubagentRequest{Meta: meta(rigRun, 2), To: r.child, Text: "two", TimeoutMs: 60000})
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	// Wait shares the cap.
	_, err = r.svc.WaitSubagents(r.ctx(), &runnerpb.WaitSubagentsRequest{Meta: meta(rigRun, 3), Ids: []string{r.child}, TimeoutMs: 50})
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	// Short calls are not affected.
	_, err = r.svc.SubagentQuestions(r.ctx(), &runnerpb.SubagentQuestionsRequest{Meta: meta(rigRun, 4)})
	require.NoError(t, err)

	_, err = r.server.AnswerQuestion(context.Background(), r.child, "beta", qid, "ok")
	require.NoError(t, err)
	require.NoError(t, <-first)
	// The slot is free again.
	_, err = r.svc.WaitSubagents(r.ctx(), &runnerpb.WaitSubagentsRequest{Meta: meta(rigRun, 5), Ids: []string{r.child}, TimeoutMs: 20})
	require.NoError(t, err)
}

func TestTargetFinishingWithoutAnsweringCancelsTheQuestion(t *testing.T) {
	r := newAskRig(t)
	ch := r.askAsync(rigChat, "alpha", r.child, "ping", 0)
	qid := r.openQID(t, r.child)
	r.c.finish(r.child, nil)
	res := getAsk(t, ch)
	require.Error(t, res.err)
	assert.Equal(t, "target "+r.child+" finished without answering; ask again or ask someone else", res.err.Error())
	_, err := r.server.ResumeQuestion(context.Background(), rigChat, qid, time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finished without answering")
}

func TestKillingAnIdleChatCancelsItsQuestions(t *testing.T) {
	r := newAskRig(t)
	r.c.finish(r.child, nil)
	waitIdle(t, r.callbackRig, r.child)
	// A question that is still open for the idle child (e.g. delivered queued).
	q := &question{qid: "q-idle", root: rigChat, asker: rigChat, target: r.child, text: "x", created: time.Now(), done: make(chan struct{})}
	r.server.questions.mu.Lock()
	require.NoError(t, r.server.questions.addLocked(q))
	r.server.questions.mu.Unlock()

	_, err := r.server.KillSubagent(rigChat, r.child)
	require.NoError(t, err)
	_, err = r.server.ResumeQuestion(context.Background(), rigChat, "q-idle", time.Millisecond)
	require.Error(t, err)
	assert.Equal(t, "target "+r.child+" was killed", err.Error())
}

func TestAskRefusesWaitForCycles(t *testing.T) {
	r := newAskRig(t)
	sibling := spawnOne(t, r.callbackRig, r.c)
	r.runs.Register(sibling, "beta", "node", "run-sib")

	// A <-> B: root waits for child; child asking root would deadlock.
	root2child := r.askAsync(rigChat, "alpha", r.child, "a", 0)
	r.openQID(t, r.child)
	_, err := r.server.AskSubagent(context.Background(), r.child, "beta", rigChat, "b", time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leads back to you")
	// An unrelated ask is still fine.
	getAsk(t, r.askAsync(sibling, "beta", r.child, "c", 20*time.Millisecond))

	// A -> B -> C: sibling asking root closes the cycle root -> child -> sibling -> root.
	child2sib := r.askAsync(r.child, "beta", sibling, "d", 0)
	r.openQID(t, sibling)
	_, err = r.server.AskSubagent(context.Background(), sibling, "beta", rigChat, "e", time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leads back to you")

	// Releasing the chain lets the ask through.
	for _, who := range []struct{ chat, agent string }{{sibling, "beta"}, {r.child, "beta"}} {
		in, _ := r.server.Questions(who.chat)
		for _, q := range in {
			_, err := r.server.AnswerQuestion(context.Background(), who.chat, who.agent, q.QID, "ok")
			require.NoError(t, err)
		}
	}
	getAsk(t, root2child)
	getAsk(t, child2sib)
}

// ---- deadlock refinements --------------------------------------------------

// An opencode/simplest lead (no live injection) that waits for its writable
// subagent, which holds the checkout and asks it: the question would be queued
// and the lead could not re-enter its lock.
func TestAskFailsFastWhenTheRunningTargetCannotTakeInjection(t *testing.T) {
	l := newLockTree(t)
	holdChat(t, l.callbackRig)
	l.runs.Register(rigChat, "alpha", "node", "run-root")
	l.inj.fn = func(string) (bool, error) { return false, nil }

	// The CLI is only learned from the failed injection: refused after the
	// delivery, and the queued message is taken back.
	_, err := l.ask(rigChat)
	require.Error(t, err)
	assert.Equal(t, "target "+rigChat+" cannot start a turn while asker holds the checkout; ask a running agent or end your turn with the question", err.Error())
	queued, qerr := l.repo.GetQueuedMessages(rigChat)
	require.NoError(t, qerr)
	assert.Empty(t, queued, "the question was dequeued")
	_, out := l.server.Questions("asker")
	assert.Empty(t, out)
	in, _ := l.server.Questions(rigChat)
	assert.Empty(t, in)

	// A configured CLI that cannot inject is refused before anything is sent.
	for _, a := range l.server.agents {
		if a.Config.ID == "alpha" {
			a.Config.CLI = []agentspec.CLITarget{{CLI: "opencode"}}
		}
	}
	before := len(l.inj.got)
	_, err = l.ask(rigChat)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot start a turn while asker holds the checkout")
	assert.Len(t, l.inj.got, before, "nothing was sent")
}

// An ancestor that holds the lock but is merely busy will finish: not a deadlock.
func TestBusyAncestorHolderIsNotAPermanentBlock(t *testing.T) {
	setup := func(t *testing.T, rootExecuting bool) *lockTree {
		rig, c := reportRig(t)
		require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "alpha", RunDir: "/work/repo"}))
		rig.server.injector = &fakeInjector{fn: func(string) (bool, error) { return true, nil }}
		rig.server.activeRuns = dispatch.NewActiveRuns()
		rel, err := rig.server.dirLocks.Acquire(t.Context(), lockReq(rigChat)) // not marked waiting
		require.NoError(t, err)
		t.Cleanup(func() { rel(false) })
		if rootExecuting {
			holdChat(t, rig)
		}
		saveLockChat(t, rig, "asker2", rigChat, true)
		saveLockChat(t, rig, "sibling", rigChat, false)
		return &lockTree{callbackRig: rig, c: c}
	}
	t.Run("busy", func(t *testing.T) {
		l := setup(t, true)
		res, err := l.server.AskSubagent(context.Background(), "asker2", "beta", "sibling", "hi", 30*time.Millisecond)
		require.NoError(t, err)
		assert.False(t, res.Answered, "delivered, and waiting for the lock holder")
	})
	t.Run("lock parked after its turn ended", func(t *testing.T) {
		l := setup(t, false)
		_, err := l.server.AskSubagent(context.Background(), "asker2", "beta", "sibling", "hi", 30*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "while "+rigChat+" holds the checkout")
	})
}

// ---- round 2 ---------------------------------------------------------------

func TestCancelOnRunEndSkipsQuestionsBeingDelivered(t *testing.T) {
	r := newAskRig(t)
	r.c.finish(r.child, nil)
	waitIdle(t, r.callbackRig, r.child)
	q := &question{qid: "q-d", root: rigChat, asker: rigChat, target: r.child, text: "x", created: time.Now(), done: make(chan struct{}), delivering: true}
	r.server.questions.mu.Lock()
	require.NoError(t, r.server.questions.addLocked(q))
	r.server.questions.mu.Unlock()

	r.server.cancelIncomingOnRunEnd(r.child)
	in, _ := r.server.Questions(r.child)
	require.Len(t, in, 1, "the question may become the prompt of the next turn")

	r.server.questions.mu.Lock()
	q.delivering = false
	r.server.questions.mu.Unlock()
	r.server.cancelIncomingOnRunEnd(r.child)
	in, _ = r.server.Questions(r.child)
	assert.Empty(t, in)
}

// The run ended while the question was injected into it: cancelled at once.
func TestQuestionInjectedIntoARunThatEndedIsCancelled(t *testing.T) {
	r := newAskRig(t)
	r.inj.fn = func(prompt string) (bool, error) {
		if strings.HasPrefix(prompt, "[question ") {
			r.c.finish(r.child, nil)
			waitIdle(t, r.callbackRig, r.child)
		}
		return true, nil
	}
	res := getAsk(t, r.askAsync(rigChat, "alpha", r.child, "too late?", 0))
	require.Error(t, res.err)
	assert.Contains(t, res.err.Error(), "finished without answering")
}

func TestQueuedParkedMessageCountsAsWillRunAgain(t *testing.T) {
	r := newAskRig(t)
	r.c.finish(r.child, nil)
	waitIdle(t, r.callbackRig, r.child)
	_, err := r.repo.EnqueueMessage(r.child, "later", "")
	require.NoError(t, err)
	r.server.queueParks.Store(r.child, queuePark{hard: true}) // parked: childFinished says finished
	require.True(t, r.server.childFinished(r.child))
	q := &question{qid: "q-p", root: rigChat, asker: rigChat, target: r.child, text: "x", created: time.Now(), done: make(chan struct{})}
	r.server.questions.mu.Lock()
	require.NoError(t, r.server.questions.addLocked(q))
	r.server.questions.mu.Unlock()

	r.server.cancelIncomingOnRunEnd(r.child)
	in, _ := r.server.Questions(r.child)
	assert.Len(t, in, 1)
}

func TestDequeuePromptReportsWhetherItRemovedTheMessage(t *testing.T) {
	r := newAskRig(t)
	_, err := r.repo.EnqueueMessage(r.child, "the prompt", "")
	require.NoError(t, err)
	assert.False(t, r.server.dequeuePrompt(r.child, "another"))
	assert.True(t, r.server.dequeuePrompt(r.child, "the prompt"))
	assert.False(t, r.server.dequeuePrompt(r.child, "the prompt"), "gone already")
}

func TestBlockingCallsHavePerTreeLimit(t *testing.T) {
	r := newAskRig(t)
	r.svc.maxBlockingTree = 1
	first := r.askViaSvc(r.ctx(), 1, "one", 60000)
	qid := r.openQID(t, r.child)
	_, err := r.svc.WaitSubagents(r.ctx(), &runnerpb.WaitSubagentsRequest{Meta: meta(rigRun, 2), Ids: []string{r.child}, TimeoutMs: 50})
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	assert.Contains(t, err.Error(), "in this spawn tree; end some waits or retry in a few seconds")

	// Another tree is unaffected.
	saveTreeChat(t, r.callbackRig, "other-root", "", "alpha", "", false)
	r.callbackRig.runs.add(runnerhub.RunInfo{RunID: "run-other", NodeID: rigNode, ChatID: "other-root", AgentID: "alpha", Done: r.done})
	_, err = r.svc.WaitSubagents(r.ctx(), &runnerpb.WaitSubagentsRequest{Meta: meta("run-other", 1), TimeoutMs: 20})
	require.NoError(t, err)

	_, err = r.server.AnswerQuestion(context.Background(), r.child, "beta", qid, "ok")
	require.NoError(t, err)
	require.NoError(t, <-first)
}

func TestWaitDefaultIsBelowTheToolTimeout(t *testing.T) {
	assert.Equal(t, 90*time.Second, defaultSubagentWait)
	assert.Equal(t, 300*time.Second, maxSubagentWait)
}
