package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

// holdInjector is an injector that can also hold runs: it logs injections and
// controls in one order.
type holdInjector struct {
	mu       sync.Mutex
	log      []string
	injectFn func(prompt string) (bool, error)
	ctlErr   error
	// replyGen is the idle generation reported with every accepted injection.
	replyGen uint64
}

// InjectGen implements genInjector.
func (h *holdInjector) InjectGen(ctx context.Context, nodeID, runID, prompt string) (bool, uint64, error) {
	ok, err := h.Inject(ctx, nodeID, runID, prompt)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !ok {
		return false, 0, err
	}
	return true, h.replyGen, err
}

func (h *holdInjector) Inject(_ context.Context, _, _, prompt string) (bool, error) {
	h.mu.Lock()
	h.log = append(h.log, "inject:"+prompt)
	fn := h.injectFn
	h.mu.Unlock()
	if fn == nil {
		return true, nil
	}
	return fn(prompt)
}

func (h *holdInjector) ControlRun(_, runID, action string, idle time.Duration) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctlErr != nil {
		return h.ctlErr
	}
	h.log = append(h.log, fmt.Sprintf("%s:%s:%d", action, runID, int(idle/time.Second)))
	return nil
}

func (h *holdInjector) entries() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.log...)
}

// controls are the hold / release entries of the log.
func (h *holdInjector) controls() []string {
	var out []string
	for _, e := range h.entries() {
		if !strings.HasPrefix(e, "inject:") {
			out = append(out, e)
		}
	}
	return out
}

func (h *holdInjector) injected() []string {
	var out []string
	for _, e := range h.entries() {
		if p, ok := strings.CutPrefix(e, "inject:"); ok {
			out = append(out, p)
		}
	}
	return out
}

// holdRig is a report rig whose parent chat runs (guard held, run registered)
// with a holding injector.
func holdRig(t *testing.T) (*callbackRig, *ctrlRuns, *holdInjector) {
	t.Helper()
	rig, c := reportRig(t)
	rig.server.holdIdle = 90 * time.Second
	holdChat(t, rig)
	inj := &holdInjector{}
	runs := dispatch.NewActiveRuns()
	runs.Register(rigChat, "alpha", "node", "run-parent")
	rig.server.injector = inj
	rig.server.activeRuns = runs
	t.Cleanup(func() { rig.server.activeRuns = nil })
	return rig, c, inj
}

func eventually(t *testing.T, f func() bool) {
	t.Helper()
	require.Eventually(t, f, 5*time.Second, 5*time.Millisecond)
}

func TestHoldSentAtFirstChildAndReleasedAtLastFinish(t *testing.T) {
	rig, c, inj := holdRig(t)
	a, b := spawnTwo(t, rig, c)
	assert.Equal(t, []string{"hold:run-parent:90"}, inj.controls(), "one hold, not one per child")

	c.finish(a, nil)
	eventually(t, func() bool { return len(inj.injected()) == 1 })
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, []string{"hold:run-parent:90"}, inj.controls(), "a child is still alive")

	c.finish(b, nil)
	eventually(t, func() bool { return len(inj.controls()) == 2 })
	assert.Equal(t, "release:run-parent:0", inj.controls()[1])
	// The last finish is injected into the still-held process before the
	// hold goes away.
	log := inj.entries()
	require.Len(t, log, 4)
	assert.Contains(t, log[2], "inject:[subagent "+b+" ")
	assert.Equal(t, "release:run-parent:0", log[3])
}

func TestHoldReleasedWhenLastChildKilled(t *testing.T) {
	rig, c, inj := holdRig(t)
	child := spawnOne(t, rig, c)
	require.Equal(t, []string{"hold:run-parent:90"}, inj.controls())

	_, err := rig.server.KillSubagent(rigChat, child)
	require.NoError(t, err)
	eventually(t, func() bool { return len(inj.controls()) == 2 })
	assert.Equal(t, "release:run-parent:0", inj.controls()[1])
	assert.Empty(t, inj.injected(), "a killed child is not pushed")
}

// A child between runs with a queued head still counts as live. Dropping that
// head without any execution release (kill of the subagent, direct stop of
// the idle child chat) must still release the parent's hold.
func TestHoldReleasedWhenBetweenRunsLastChildKilled(t *testing.T) {
	rig, c, inj := holdRig(t)
	child := spawnOne(t, rig, c)
	require.Equal(t, []string{"hold:run-parent:90"}, inj.controls())
	rig.server.activeExecutions.Delete(child) // between runs, head about to run
	_, err := rig.repo.EnqueueMessage(child, "more", "")
	require.NoError(t, err)
	require.True(t, rig.server.hasLiveChildren(rigChat))

	_, err = rig.server.KillSubagent(rigChat, child)
	require.NoError(t, err)
	eventually(t, func() bool { return len(inj.controls()) == 2 })
	assert.Equal(t, "release:run-parent:0", inj.controls()[1])
}

func TestHoldReleasedWhenIdleChildWithQueuedHeadStopped(t *testing.T) {
	rig, c, inj := holdRig(t)
	child := spawnOne(t, rig, c)
	require.Equal(t, []string{"hold:run-parent:90"}, inj.controls())
	rig.server.activeExecutions.Delete(child)
	_, err := rig.repo.EnqueueMessage(child, "more", "")
	require.NoError(t, err)

	code, _ := rig.server.stopSessionExecution(child)
	require.Equal(t, http.StatusConflict, code)
	assert.Equal(t, []string{"hold:run-parent:90", "release:run-parent:0"}, inj.controls(), "released synchronously")
}

func TestHoldForgottenWhenParentStopped(t *testing.T) {
	rig, c, inj := holdRig(t)
	_ = spawnOne(t, rig, c)
	require.Equal(t, []string{"hold:run-parent:90"}, inj.controls())

	code, msg := rig.server.stopSessionExecution(rigChat)
	require.Equal(t, http.StatusOK, code, msg)
	rig.server.releaseExecution(rigChat) // the cancelled run unwinds and frees its guard
	eventually(t, func() bool {
		rig.server.holds.mu.Lock()
		defer rig.server.holds.mu.Unlock()
		return len(rig.server.holds.m) == 0
	})
}

func TestNewRunOfParentWithLiveChildrenIsHeldToo(t *testing.T) {
	rig, c, inj := holdRig(t)
	_ = spawnOne(t, rig, c)
	// The parent's process died (crash / idle cap) while the child lives and a
	// push started a new run.
	rig.server.activeRuns.(*dispatch.ActiveRuns).Register(rigChat, "alpha", "node", "run-parent-2")
	rig.server.syncHold(rigChat)
	assert.Equal(t, []string{"hold:run-parent:90", "hold:run-parent-2:90"}, inj.controls())
}

func TestHoldNotSentToRunnerThatCannotHold(t *testing.T) {
	rig, c := reportRig(t)
	holdChat(t, rig)
	inj := &fakeInjector{fn: func(string) (bool, error) { return false, nil }}
	runs := dispatch.NewActiveRuns()
	runs.Register(rigChat, "alpha", "node", "run-parent")
	rig.server.injector = inj
	rig.server.activeRuns = runs
	child := spawnOne(t, rig, c)

	// Non-injectable CLI: behaves as before, the push waits in the queue.
	c.finish(child, nil)
	eventually(t, func() bool {
		q, _ := rig.repo.GetQueuedMessages(rigChat)
		return len(q) == 1
	})
	assert.False(t, rig.server.isIdleHeld(rigChat))
}

func TestFailedHoldIsRetriedAtNextSync(t *testing.T) {
	rig, c, inj := holdRig(t)
	inj.ctlErr = fmt.Errorf("runner offline")
	_ = spawnOne(t, rig, c)
	assert.Empty(t, inj.controls())
	inj.mu.Lock()
	inj.ctlErr = nil
	inj.mu.Unlock()
	rig.server.syncHold(rigChat)
	assert.Equal(t, []string{"hold:run-parent:90"}, inj.controls())
}

// ---- idle hold: waiting mark and the next turn -------------------------------

func markedNow(rig *callbackRig, chat string) bool {
	return rig.server.chatMarked(chat)
}

func TestIdleHeldParentMarkedAndPushInjectedWithoutNewRun(t *testing.T) {
	rig, c, inj := holdRig(t)
	child := spawnOne(t, rig, c)
	ctx := t.Context()
	relParent, err := rig.server.dirLocks.Acquire(ctx, lockReq(rigChat))
	require.NoError(t, err)
	defer relParent(false)

	// Before the parent's turn ends, a writable child queues behind it.
	pre := acquireAsync(rig, ctx, lockReq("child-pre", rigChat))
	expectQueued(t, rig, "child-pre")

	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 0, false)
	assert.True(t, rig.server.isIdleHeld(rigChat))
	rel := expectGranted(t, pre) // the mark let it through
	rel(false)

	// The status shows the chat between turns, not running.
	full, err := rig.repo.GetSession(rigChat)
	require.NoError(t, err)
	for _, a := range full.Agents {
		assert.NotEqual(t, dbmodels.AgentStatusRunning, a.Status)
	}

	// The child finishes: the push becomes the next turn of the same process.
	c.finish(child, nil)
	eventually(t, func() bool { return len(inj.injected()) == 1 })
	assert.Contains(t, inj.injected()[0], "[subagent "+child+" (work) finished: done]")
	c.noRunFor(t, rigChat)
	assert.False(t, markedNow(rig, rigChat), "the mark is cleared before the turn starts")
	assert.False(t, rig.server.isIdleHeld(rigChat))
	// The turn writes: children queue again.
	_ = acquireAsync(rig, ctx, lockReq("child-after", rigChat))
	expectQueued(t, rig, "child-after")
}

// A stale idle report (an earlier generation, delivered late) must neither
// mark the running turn waiting nor be applied twice.
func TestStaleIdleReportOfEarlierGenerationIgnored(t *testing.T) {
	rig, c, _ := holdRig(t)
	_ = spawnOne(t, rig, c)
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 1, true)
	require.True(t, rig.server.isIdleHeld(rigChat))
	require.True(t, rig.server.tryInject(rigChat, "alpha", "hello"))
	assert.False(t, rig.server.isIdleHeld(rigChat))
	// Delivered late, long after the injection (no clock involved).
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 1, true)
	assert.False(t, rig.server.isIdleHeld(rigChat))
	assert.False(t, markedNow(rig, rigChat))
	// The injected turn's own reports are newer and apply.
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnActive, 2, true)
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 3, true)
	assert.True(t, rig.server.isIdleHeld(rigChat))
	assert.True(t, markedNow(rig, rigChat))
}

// A fast injected turn (active then idle reported at once) must bring the
// mark back; no grace period swallows its idle.
func TestFastInjectedTurnIdleReportMarksAgain(t *testing.T) {
	rig, c, _ := holdRig(t)
	_ = spawnOne(t, rig, c)
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 1, true)
	require.True(t, rig.server.tryInject(rigChat, "alpha", "ok?"))
	require.False(t, markedNow(rig, rigChat))
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnActive, 2, true)
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 3, true)
	assert.True(t, rig.server.isIdleHeld(rigChat))
	assert.True(t, markedNow(rig, rigChat))
}

// The active report for an injection whose idle report from before arrives
// late clears the wrongly applied mark; a lost active report is covered by the
// next idle's newer generation.
func TestActiveReportClearsWronglyAppliedIdle(t *testing.T) {
	rig, c, _ := holdRig(t)
	_ = spawnOne(t, rig, c)
	// The inject happened while the backend did not yet know aw was idle.
	require.True(t, rig.server.tryInject(rigChat, "alpha", "hi"))
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 1, true) // stale, applied
	require.True(t, markedNow(rig, rigChat))
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnActive, 2, true)
	assert.False(t, markedNow(rig, rigChat))
	assert.False(t, rig.server.isIdleHeld(rigChat))
}

// A dropped active report: the later idle still applies (newer generation).
func TestDroppedActiveReportStillAllowsIdle(t *testing.T) {
	rig, c, _ := holdRig(t)
	_ = spawnOne(t, rig, c)
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 1, true)
	require.True(t, rig.server.tryInject(rigChat, "alpha", "hi"))
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 3, true) // active (2) lost
	assert.True(t, rig.server.isIdleHeld(rigChat))
}

func TestTurnReportGen(t *testing.T) {
	g, ok := turnReportGen(map[string]any{common.MetaIdleGen: float64(7)})
	assert.True(t, ok)
	assert.Equal(t, uint64(7), g)
	_, ok = turnReportGen(nil)
	assert.False(t, ok)
}

func TestIdleHeldPushQueuedWhileWritableChildHoldsCheckout(t *testing.T) {
	rig, c, inj := holdRig(t)
	child, _ := spawnTwo(t, rig, c) // the other child stays alive: the hold stays
	ctx := t.Context()
	relParent, err := rig.server.dirLocks.Acquire(ctx, lockReq(rigChat))
	require.NoError(t, err)
	defer relParent(false)

	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 0, false)
	// A writable sibling works in the checkout.
	relSibling := expectGranted(t, acquireAsync(rig, ctx, lockReq("child-writer", rigChat)))

	c.finish(child, nil)
	// The mark cannot be cleared while the sibling holds the checkout: the
	// turn must not start, so the push is queued instead of injected.
	eventually(t, func() bool {
		q, _ := rig.repo.GetQueuedMessages(rigChat)
		return len(q) == 1
	})
	assert.Empty(t, inj.injected())
	assert.True(t, rig.server.isIdleHeld(rigChat), "still idle: no turn started")
	assert.True(t, markedNow(rig, rigChat))

	// Once the sibling lets go, the next message is injected.
	relSibling(false)
	require.True(t, rig.server.tryInject(rigChat, "alpha", "go on"))
	assert.Equal(t, []string{"go on"}, inj.injected())
	assert.False(t, markedNow(rig, rigChat))
}

func TestTurnActiveReportClearsMarkAndShowsRunning(t *testing.T) {
	rig, c, _ := holdRig(t)
	_ = spawnOne(t, rig, c)
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 0, false)
	require.True(t, markedNow(rig, rigChat))
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnActive, 0, false)
	assert.False(t, markedNow(rig, rigChat))
	assert.False(t, rig.server.isIdleHeld(rigChat))
}

func TestRunEndWhileIdleHeldClearsMark(t *testing.T) {
	rig, c, _ := holdRig(t)
	_ = spawnOne(t, rig, c)
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 0, false)
	require.True(t, markedNow(rig, rigChat))

	rig.server.activeExecutions.Delete(rigChat) // the run ended (idle cap, crash)
	rig.server.onExecutionReleased(rigChat)
	assert.False(t, markedNow(rig, rigChat))
	assert.False(t, rig.server.isIdleHeld(rigChat))
}

func TestFailedInjectIntoIdleHeldRunRestoresIdle(t *testing.T) {
	rig, c, inj := holdRig(t)
	_ = spawnOne(t, rig, c)
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 0, false)
	inj.mu.Lock()
	inj.injectFn = func(string) (bool, error) { return false, nil }
	inj.mu.Unlock()

	assert.False(t, rig.server.tryInject(rigChat, "alpha", "x"))
	assert.True(t, rig.server.isIdleHeld(rigChat), "nothing started a turn: the run is still idle")
	assert.True(t, markedNow(rig, rigChat))
}

func TestUserMessageToIdleHeldChatIsInjected(t *testing.T) {
	rig, c, inj := holdRig(t)
	_ = spawnOne(t, rig, c)
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 0, false)

	target := rig.server.findLoadedAgent("alpha")
	require.NotNil(t, target)
	rec := newResponseCapture()
	rig.server.triggerMessage(context.Background(), nil, rec, "alpha", target, TriggerMessageRequest{Prompt: "how is it going?", ChatID: rigChat}, false)
	assert.Equal(t, http.StatusAccepted, rec.code, rec.body.String())
	assert.Contains(t, rec.body.String(), `"injected":true`)
	assert.Equal(t, []string{"how is it going?"}, inj.injected())
	q, _ := rig.repo.GetQueuedMessages(rigChat)
	assert.Empty(t, q)
	c.noRunFor(t, rigChat)
}

func TestUserMessageToBusyHeldChatStillQueues(t *testing.T) {
	rig, c, inj := holdRig(t)
	_ = spawnOne(t, rig, c)
	// Mid-turn (not idle): the old behavior, the message waits in the queue.
	target := rig.server.findLoadedAgent("alpha")
	require.NotNil(t, target)
	rec := newResponseCapture()
	rig.server.triggerMessage(context.Background(), nil, rec, "alpha", target, TriggerMessageRequest{Prompt: "later", ChatID: rigChat}, false)
	assert.Equal(t, http.StatusAccepted, rec.code, rec.body.String())
	assert.Contains(t, rec.body.String(), "queued")
	assert.Empty(t, inj.injected())
}

// holdWithQueuedWritableChild sets up a parent that holds the checkout with a
// writable child queued behind it, and returns the channel the child's grant
// arrives on. The parent's lock is released at cleanup.
func holdWithQueuedWritableChild(t *testing.T, rig *callbackRig) chan func(bool) {
	t.Helper()
	ctx := t.Context()
	relParent, err := rig.server.dirLocks.Acquire(ctx, lockReq(rigChat))
	require.NoError(t, err)
	t.Cleanup(func() { relParent(false) })
	pre := acquireAsync(rig, ctx, lockReq("child-pre", rigChat))
	expectQueued(t, rig, "child-pre")
	return pre
}

func expectNotGranted(t *testing.T, got chan func(bool)) {
	t.Helper()
	select {
	case <-got:
		t.Fatal("the queued writable child was granted the checkout")
	case <-time.After(150 * time.Millisecond):
	}
}

// The late pre-inject idle report must not mark a running turn: the reply's
// generation makes it stale, so a queued writable child is not let in.
func TestLatePreInjectIdleReportDoesNotGrantWritableChild(t *testing.T) {
	rig, c, inj := holdRig(t)
	_ = spawnOne(t, rig, c)
	inj.replyGen = 2
	pre := holdWithQueuedWritableChild(t, rig)

	require.True(t, rig.server.tryInject(rigChat, "alpha", "hi"))
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 1, true) // late, pre-inject
	assert.False(t, markedNow(rig, rigChat))
	assert.False(t, rig.server.isIdleHeld(rigChat))
	expectNotGranted(t, pre)

	// The injected turn ends: its idle is newer and applies.
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 3, true)
	assert.True(t, rig.server.isIdleHeld(rigChat))
}

// An aw without reply generation: the idle that arrives while the injection is
// in flight is held back, and dropped once the injection succeeded.
func TestIdleDuringInflightInjectDroppedOnSuccess(t *testing.T) {
	rig, c, inj := holdRig(t)
	_ = spawnOne(t, rig, c)
	pre := holdWithQueuedWritableChild(t, rig)
	started, release := make(chan struct{}), make(chan struct{})
	inj.mu.Lock()
	inj.injectFn = func(string) (bool, error) { close(started); <-release; return true, nil }
	inj.mu.Unlock()

	res := make(chan bool, 1)
	go func() { res <- rig.server.tryInject(rigChat, "alpha", "hi") }()
	<-started
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 1, true)
	assert.False(t, markedNow(rig, rigChat), "not applied while the injection is in flight")
	close(release)
	require.True(t, <-res)
	assert.False(t, markedNow(rig, rigChat))
	assert.False(t, rig.server.isIdleHeld(rigChat))
	expectNotGranted(t, pre)
}

// If the injection failed the stashed idle is the truth and is applied.
func TestIdleDuringInflightInjectAppliedOnFailure(t *testing.T) {
	rig, c, inj := holdRig(t)
	_ = spawnOne(t, rig, c)
	started, release := make(chan struct{}), make(chan struct{})
	inj.mu.Lock()
	inj.injectFn = func(string) (bool, error) { close(started); <-release; return false, nil }
	inj.mu.Unlock()

	res := make(chan bool, 1)
	go func() { res <- rig.server.tryInject(rigChat, "alpha", "hi") }()
	<-started
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 1, true)
	assert.False(t, rig.server.isIdleHeld(rigChat))
	close(release)
	require.False(t, <-res)
	assert.True(t, rig.server.isIdleHeld(rigChat))
	assert.True(t, markedNow(rig, rigChat))
}

// A failed injection prepared mid-turn (no idle reported) must not mark the
// chat: the turn is still running.
func TestFailedInjectMidTurnDoesNotMarkOrGrantWritableChild(t *testing.T) {
	rig, c, inj := holdRig(t)
	_ = spawnOne(t, rig, c)
	pre := holdWithQueuedWritableChild(t, rig)
	inj.mu.Lock()
	inj.injectFn = func(string) (bool, error) { return false, nil }
	inj.mu.Unlock()

	assert.False(t, rig.server.tryInject(rigChat, "alpha", "hi"))
	assert.False(t, markedNow(rig, rigChat))
	assert.False(t, rig.server.isIdleHeld(rigChat))
	expectNotGranted(t, pre)
}

// An idle newer than the reply generation is the injected turn's own end, even
// when it overtakes the injection result.
func TestIdleDuringInflightInjectNewerThanReplyApplied(t *testing.T) {
	rig, c, inj := holdRig(t)
	_ = spawnOne(t, rig, c)
	inj.replyGen = 2
	started, release := make(chan struct{}), make(chan struct{})
	inj.mu.Lock()
	inj.injectFn = func(string) (bool, error) { close(started); <-release; return true, nil }
	inj.mu.Unlock()

	res := make(chan bool, 1)
	go func() { res <- rig.server.tryInject(rigChat, "alpha", "hi") }()
	<-started
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 3, true)
	close(release)
	require.True(t, <-res)
	assert.True(t, rig.server.isIdleHeld(rigChat))
}
