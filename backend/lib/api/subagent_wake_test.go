package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
)

type outcomeResult struct {
	o   SubagentWaitOutcome
	err error
}

func startWaitOutcome(rig *callbackRig, ids []string, anyOf bool, timeout time.Duration) chan outcomeResult {
	return startWaitOutcomeAs(rig, rigChat, "", ids, anyOf, timeout)
}

func startWaitOutcomeAs(rig *callbackRig, chat, run string, ids []string, anyOf bool, timeout time.Duration) chan outcomeResult {
	out := make(chan outcomeResult, 1)
	go func() {
		o, err := rig.server.WaitSubagentsOutcome(context.Background(), chat, run, ids, anyOf, timeout)
		out <- outcomeResult{o, err}
	}()
	return out
}

// unreadOf returns the unread record of chat for run (any run when empty).
func unreadOf(rig *callbackRig, chat string, run ...string) *unreadInbox {
	g := &rig.server.subagentSig
	g.mu.Lock()
	defer g.mu.Unlock()
	for r, u := range g.unread[chat] {
		if len(run) == 0 || run[0] == r {
			c := *u
			return &c
		}
	}
	return nil
}

func expectNoWaitOutcome(t *testing.T, ch chan outcomeResult) {
	t.Helper()
	select {
	case o := <-ch:
		t.Fatalf("wait returned early: %+v", o)
	case <-time.After(100 * time.Millisecond):
	}
}

func getWaitOutcome(t *testing.T, ch chan outcomeResult) outcomeResult {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(5 * time.Second):
		t.Fatal("wait never returned")
		return outcomeResult{}
	}
}

func TestWaitWakesOnReplyFromChild(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	inj := parentRunning(t, rig, func(string) (bool, error) { return true, nil })

	ch := startWaitOutcome(rig, nil, false, 0)
	waitRegistered(t, rig)
	got, err := rig.server.ReplyToParent(context.Background(), child, "which branch?")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryInjected, got)

	r := getWaitOutcome(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, []string{"subagent " + child + " (work)"}, r.o.WokenBy)
	require.Len(t, r.o.Results, 1)
	assert.True(t, r.o.Results[0].StillRunning, "the child keeps running")
	assert.Len(t, inj.got, 1, "the message was still injected into the turn")

	// The wait is over: its registration is gone.
	g := &rig.server.subagentSig
	g.mu.Lock()
	defer g.mu.Unlock()
	assert.Empty(t, g.waits)
}

func TestWaitStillReturnsOnFinishWithoutWake(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	ch := startWaitOutcome(rig, nil, false, 0)
	waitRegistered(t, rig)
	c.finish(child, nil)
	r := getWaitOutcome(t, ch)
	require.NoError(t, r.err)
	assert.Empty(t, r.o.WokenBy)
	require.Len(t, r.o.Results, 1)
	assert.Equal(t, SubagentStateDone, r.o.Results[0].Info.State)
}

func TestWaitTimeoutHasNoWake(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	r := getWaitOutcome(t, startWaitOutcome(rig, []string{child}, false, 50*time.Millisecond))
	require.NoError(t, r.err)
	assert.Empty(t, r.o.WokenBy)
	require.Len(t, r.o.Results, 1)
	assert.True(t, r.o.Results[0].StillRunning)
}

func TestMessageBeforeWaitRegistersWakesItAtOnce(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	parentRunning(t, rig, func(string) (bool, error) { return true, nil })
	_, err := rig.server.ReplyToParent(context.Background(), child, "fyi")
	require.NoError(t, err)
	u := unreadOf(rig, rigChat)
	require.NotNil(t, u)
	assert.Equal(t, []string{"subagent " + child + " (work)"}, u.senders)
	assert.EqualValues(t, 1, u.seq)

	r := getWaitOutcome(t, startWaitOutcome(rig, []string{child}, false, 0))
	require.NoError(t, r.err)
	assert.Equal(t, []string{"subagent " + child + " (work)"}, r.o.WokenBy)
	require.Len(t, r.o.Results, 1)
	assert.True(t, r.o.Results[0].StillRunning)
	assert.Nil(t, unreadOf(rig, rigChat), "consumed by the wait")

	// Consumed: the next wait blocks until its timeout.
	start := time.Now()
	r = getWaitOutcome(t, startWaitOutcome(rig, []string{child}, false, 150*time.Millisecond))
	assert.Empty(t, r.o.WokenBy)
	assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond)
}

func TestQueuedDeliveryDoesNotWake(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	parentRunning(t, rig, func(string) (bool, error) { return false, nil })

	ch := startWaitOutcome(rig, []string{child}, false, 0)
	waitRegistered(t, rig)
	got, err := rig.server.ReplyToParent(context.Background(), child, "later")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryQueued, got)
	expectNoWaitOutcome(t, ch)
	assert.Nil(t, unreadOf(rig, rigChat), "nothing to read in this turn")
	c.finish(child, nil)
	assert.Empty(t, getWaitOutcome(t, ch).o.WokenBy)
}

func TestUnreadRecordClearedOnRunEndAndStart(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	parentRunning(t, rig, func(string) (bool, error) { return true, nil })
	_, err := rig.server.ReplyToParent(context.Background(), child, "one")
	require.NoError(t, err)
	require.NotNil(t, unreadOf(rig, rigChat))

	// The run ends.
	rig.server.activeExecutions.Delete(rigChat)
	rig.server.onExecutionReleased(rigChat)
	assert.Nil(t, unreadOf(rig, rigChat))

	// A new run starts: the leftovers of the previous one are gone.
	rig.server.notifyInbox(rigChat, "run-old", "x")
	require.NotNil(t, unreadOf(rig, rigChat))
	_, handle, loaded := rig.server.beginExecution(rigChat, "alpha", false)
	require.False(t, loaded)
	defer handle.cancel()
	assert.Nil(t, unreadOf(rig, rigChat))
}

func TestWaitIgnoresMessagesForAnotherRunOfTheChat(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	ch := startWaitOutcomeAs(rig, rigChat, "run-outer", []string{child}, false, 0)
	waitRegistered(t, rig)

	// A call-peer nested run of the same chat got a message: not for this wait.
	rig.server.notifyInbox(rigChat, "run-inner", "someone")
	expectNoWaitOutcome(t, ch)
	rig.server.notifyInbox(rigChat, "run-outer", "somebody")
	r := getWaitOutcome(t, ch)
	assert.Equal(t, []string{"somebody"}, r.o.WokenBy)

	// The message of the inner run stays unread for that run only.
	u := unreadOf(rig, rigChat)
	require.NotNil(t, u)
	assert.Equal(t, "run-inner", u.run)
	assert.Nil(t, unreadOf(rig, rigChat, "run-outer"))
	r = getWaitOutcome(t, startWaitOutcomeAs(rig, rigChat, "run-outer", []string{child}, false, 50*time.Millisecond))
	assert.Empty(t, r.o.WokenBy)
	r = getWaitOutcome(t, startWaitOutcomeAs(rig, rigChat, "run-inner", []string{child}, false, 50*time.Millisecond))
	assert.Equal(t, []string{"someone"}, r.o.WokenBy)
}

func TestInboxMessagesCoalesceIntoOneWake(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	ch := startWaitOutcome(rig, []string{child}, false, 0)
	waitRegistered(t, rig)
	rig.server.notifyInbox(rigChat, "r", "someone")
	rig.server.notifyInbox(rigChat, "r", "someone")
	rig.server.notifyInbox("another-chat", "r", "nobody")
	r := getWaitOutcome(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, []string{"someone"}, r.o.WokenBy)
}

// nestedWaiter spawns a grandchild below child and returns it.
func spawnGrandchild(t *testing.T, rig *callbackRig, c *ctrlRuns, child string) string {
	t.Helper()
	gc, err := rig.server.SpawnSubagent(context.Background(), SpawnSubagentRequest{
		CallerChat: child, CallerAgent: "beta", TargetAgent: "gamma", Label: "deep", Prompt: "do it",
	})
	require.NoError(t, err)
	waitStarted(t, c.started)
	return gc
}

func TestSendWakesTheNestedWaitOfTheChild(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	gc := spawnGrandchild(t, rig, c, child)
	inj := &fakeInjector{fn: func(string) (bool, error) { return true, nil }}
	runs := dispatch.NewActiveRuns()
	runs.Register(child, "beta", "node", "run-child")
	rig.server.injector = inj
	rig.server.activeRuns = runs

	ch := startWaitOutcomeAs(rig, child, "run-child", []string{gc}, false, 0)
	waitRegistered(t, rig)
	got, err := rig.server.SendToSubagent(context.Background(), rigChat, "alpha", child, "change of plan")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryInjected, got)
	r := getWaitOutcome(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, []string{"parent agent alpha"}, r.o.WokenBy)
	require.Len(t, r.o.Results, 1)
	assert.True(t, r.o.Results[0].StillRunning)
}

func TestSendQueuedDoesNotWakeTheNestedWait(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	gc := spawnGrandchild(t, rig, c, child)
	runs := dispatch.NewActiveRuns()
	runs.Register(child, "beta", "node", "run-child")
	rig.server.injector = &fakeInjector{fn: func(string) (bool, error) { return false, nil }}
	rig.server.activeRuns = runs

	ch := startWaitOutcomeAs(rig, child, "run-child", []string{gc}, false, 0)
	waitRegistered(t, rig)
	got, err := rig.server.SendToSubagent(context.Background(), rigChat, "alpha", child, "later")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryQueued, got)
	expectNoWaitOutcome(t, ch)
	c.finish(gc, nil)
	assert.Empty(t, getWaitOutcome(t, ch).o.WokenBy)
}

func finishPushes(inj *fakeInjector, child string) int {
	n := 0
	inj.mu.Lock()
	defer inj.mu.Unlock()
	for _, p := range inj.got {
		if strings.Contains(p, "[subagent "+child+" (work) finished") {
			n++
		}
	}
	return n
}

func TestFinishAfterEarlyWakeIsPushedOnce(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	inj := parentRunning(t, rig, func(string) (bool, error) { return true, nil })
	ch := startWaitOutcome(rig, []string{child}, false, 0)
	waitRegistered(t, rig)
	_, err := rig.server.ReplyToParent(context.Background(), child, "q")
	require.NoError(t, err)
	assert.Len(t, getWaitOutcome(t, ch).o.WokenBy, 1)

	c.finish(child, nil)
	require.Eventually(t, func() bool { return finishPushes(inj, child) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, finishPushes(inj, child))
}

func TestFinishAfterEarlyWakeIsReportedByNextWaitOnly(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	inj := parentRunning(t, rig, func(string) (bool, error) { return true, nil })
	ch := startWaitOutcome(rig, []string{child}, false, 0)
	waitRegistered(t, rig)
	_, err := rig.server.ReplyToParent(context.Background(), child, "q")
	require.NoError(t, err)
	getWaitOutcome(t, ch)

	ch = startWaitOutcome(rig, []string{child}, false, 0)
	waitRegistered(t, rig)
	c.finish(child, nil)
	r := getWaitOutcome(t, ch)
	assert.Empty(t, r.o.WokenBy)
	require.Len(t, r.o.Results, 1)
	assert.Equal(t, SubagentStateDone, r.o.Results[0].Info.State)
	time.Sleep(150 * time.Millisecond)
	assert.Zero(t, finishPushes(inj, child), "reported by the wait, so not pushed")
}

func TestFinishReportedBeforeItsReleaseIsHandledIsNotPushed(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	// The child's guard is gone but its release is not handled yet.
	rig.server.activeExecutions.Delete(child)
	r := getWaitOutcome(t, startWaitOutcome(rig, []string{child}, false, 0))
	require.Len(t, r.o.Results, 1)
	assert.False(t, r.o.Results[0].StillRunning)

	epoch := rig.server.bumpReleaseEpoch(child)
	rig.server.onExecutionReleasedAt(child, epoch)
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat), "already reported by the wait")
	g := &rig.server.subagentSig
	g.mu.Lock()
	assert.Empty(t, g.reported)
	g.mu.Unlock()
}

func TestRunningChildOfReturnedWaitIsStillPushed(t *testing.T) {
	rig, c := reportRig(t)
	a, b := spawnTwo(t, rig, c)
	ch := startWaitOutcome(rig, []string{a, b}, true, 0)
	waitRegistered(t, rig)
	c.finish(a, nil)
	getWaitOutcome(t, ch)
	g := &rig.server.subagentSig
	g.mu.Lock()
	assert.NotContains(t, g.reported, b, "a running child carries no flag")
	g.mu.Unlock()
	c.finish(b, nil)
	require.Eventually(t, func() bool { return len(c.promptsOf(rigChat)) > 0 }, 5*time.Second, 10*time.Millisecond)
}

func TestEarlyWakeReleasesMarkAndRewaitTakesItBack(t *testing.T) {
	rig, c := reportRig(t)
	ctx := t.Context()
	relParent, err := rig.server.dirLocks.Acquire(ctx, lockReq(rigChat))
	require.NoError(t, err)
	defer relParent(false)
	child := spawnOne(t, rig, c)
	parentRunning(t, rig, func(string) (bool, error) { return true, nil })

	ch := startWaitOutcome(rig, []string{child}, false, 0)
	waitRegistered(t, rig)
	require.Eventually(t, func() bool { return rig.server.chatMarked(rigChat) }, 5*time.Second, 5*time.Millisecond)
	_, err = rig.server.ReplyToParent(context.Background(), child, "q")
	require.NoError(t, err)
	r := getWaitOutcome(t, ch)
	assert.Empty(t, r.o.CheckoutHeldBy)
	assert.False(t, rig.server.chatMarked(rigChat), "the mark is released after the wake")

	// A re-wait takes the mark again.
	ch = startWaitOutcome(rig, []string{child}, false, 0)
	waitRegistered(t, rig)
	require.Eventually(t, func() bool { return rig.server.chatMarked(rigChat) }, 5*time.Second, 5*time.Millisecond)
	c.finish(child, nil)
	getWaitOutcome(t, ch)
	assert.False(t, rig.server.chatMarked(rigChat))
}

func TestEarlyWakeReportsCheckoutHeldByWritableDescendant(t *testing.T) {
	rig, c := reportRig(t)
	ctx := t.Context()
	relParent, err := rig.server.dirLocks.Acquire(ctx, lockReq(rigChat))
	require.NoError(t, err)
	defer relParent(false)
	child := spawnOne(t, rig, c)
	parentRunning(t, rig, func(string) (bool, error) { return true, nil })

	ch := startWaitOutcome(rig, []string{child}, false, 0)
	waitRegistered(t, rig)
	require.Eventually(t, func() bool { return rig.server.chatMarked(rigChat) }, 5*time.Second, 5*time.Millisecond)
	// The writable child takes the checkout during the wait and keeps it.
	relChild := expectGranted(t, acquireAsync(rig, ctx, lockReq(child, rigChat)))
	_, err = rig.server.ReplyToParent(context.Background(), child, "q")
	require.NoError(t, err)
	r := getWaitOutcome(t, ch)
	assert.Equal(t, child, r.o.CheckoutHeldBy)
	assert.True(t, rig.server.chatMarked(rigChat), "the clear is pending while the child holds")

	// A re-wait takes the pending mark back over; the holder goes away at the end.
	ch = startWaitOutcome(rig, []string{child}, false, 0)
	waitRegistered(t, rig)
	relChild(false)
	c.finish(child, nil)
	assert.Empty(t, getWaitOutcome(t, ch).o.CheckoutHeldBy)
	require.Eventually(t, func() bool { return !rig.server.chatMarked(rigChat) }, 5*time.Second, 10*time.Millisecond)
}

func TestIdleHeldInjectIsNotRecordedAsUnread(t *testing.T) {
	rig, c, _ := holdRig(t)
	child := spawnOne(t, rig, c)
	// Mid-turn injection: unread.
	_, err := rig.server.ReplyToParent(context.Background(), child, "mid-turn")
	require.NoError(t, err)
	require.NotNil(t, unreadOf(rig, rigChat))
	rig.server.subagentSig.mu.Lock()
	delete(rig.server.subagentSig.unread, rigChat)
	rig.server.subagentSig.mu.Unlock()

	// The parent is idle between turns: the message is the prompt of its new
	// turn, which it reads for certain.
	rig.server.onTurnReport(rigChat, common.EntryTypeTurnIdle, 1, true)
	require.True(t, rig.server.isIdleHeld(rigChat))
	got, err := rig.server.ReplyToParent(context.Background(), child, "wake up")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryInjected, got)
	assert.False(t, rig.server.isIdleHeld(rigChat))
	assert.Nil(t, unreadOf(rig, rigChat))
}

func TestGrandchildFinishReportedByRootWaitIsStillPushedToItsParent(t *testing.T) {
	rig, c := reportRig(t)
	mid := spawnOne(t, rig, c)
	gc := spawnGrandchild(t, rig, c, mid)
	// The grandchild's guard is gone but its release is not handled yet.
	rig.server.activeExecutions.Delete(gc)
	r := getWaitOutcome(t, startWaitOutcome(rig, []string{gc}, false, 0))
	require.Len(t, r.o.Results, 1)
	assert.False(t, r.o.Results[0].StillRunning)
	g := &rig.server.subagentSig
	g.mu.Lock()
	assert.NotContains(t, g.reported, gc, "only the waiter's direct children are flagged")
	g.mu.Unlock()

	epoch := rig.server.bumpReleaseEpoch(gc)
	rig.server.onExecutionReleasedAt(gc, epoch)
	require.Eventually(t, func() bool {
		q, err := rig.repo.GetQueuedMessages(mid)
		if err != nil || len(q) == 0 {
			return false
		}
		return strings.Contains(q[0].Prompt, "[subagent "+gc)
	}, 5*time.Second, 10*time.Millisecond, "the middle chat hears of the finish")
}

func TestTimeoutReportsCheckoutHeldByWritableDescendant(t *testing.T) {
	rig, c := reportRig(t)
	ctx := t.Context()
	relParent, err := rig.server.dirLocks.Acquire(ctx, lockReq(rigChat))
	require.NoError(t, err)
	defer relParent(false)
	child := spawnOne(t, rig, c)

	ch := startWaitOutcome(rig, []string{child}, false, 300*time.Millisecond)
	waitRegistered(t, rig)
	require.Eventually(t, func() bool { return rig.server.chatMarked(rigChat) }, 5*time.Second, 5*time.Millisecond)
	relChild := expectGranted(t, acquireAsync(rig, ctx, lockReq(child, rigChat)))
	defer relChild(false)
	r := getWaitOutcome(t, ch)
	assert.Empty(t, r.o.WokenBy)
	require.Len(t, r.o.Results, 1)
	assert.True(t, r.o.Results[0].StillRunning)
	assert.Equal(t, child, r.o.CheckoutHeldBy)
}
