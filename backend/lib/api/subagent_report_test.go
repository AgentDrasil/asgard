package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// ctrlRuns makes every started run block until the test finishes it, and
// records the prompts each chat was started with.
type ctrlRuns struct {
	started chan string
	mu      sync.Mutex
	gates   map[string]chan error
	prompts map[string][]string
}

func newCtrlRuns(rig *callbackRig) *ctrlRuns {
	c := &ctrlRuns{started: make(chan string, 32), gates: map[string]chan error{}, prompts: map[string][]string{}}
	rig.server.runSingleAgentFn = func(ctx context.Context, _ *agentspec.Agent, chatID string, req TriggerMessageRequest) (string, string, error) {
		c.mu.Lock()
		c.prompts[chatID] = append(c.prompts[chatID], req.Prompt)
		g := c.gateLocked(chatID)
		c.mu.Unlock()
		c.started <- chatID
		select {
		case err := <-g:
			if err != nil {
				return "failed", "", err
			}
			return "completed", "", nil
		case <-ctx.Done():
			return "failed", "", ctx.Err()
		}
	}
	return c
}

func (c *ctrlRuns) gateLocked(chat string) chan error {
	g, ok := c.gates[chat]
	if !ok {
		g = make(chan error, 4)
		c.gates[chat] = g
	}
	return g
}

// finish ends the current run of chat with err.
func (c *ctrlRuns) finish(chat string, err error) {
	c.mu.Lock()
	g := c.gateLocked(chat)
	c.mu.Unlock()
	g <- err
}

func (c *ctrlRuns) promptsOf(chat string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.prompts[chat]...)
}

// noRunFor asserts that chat does not start a run for a short while.
func (c *ctrlRuns) noRunFor(t *testing.T, chat string) {
	t.Helper()
	select {
	case id := <-c.started:
		assert.NotEqual(t, chat, id, "unexpected run")
	case <-time.After(150 * time.Millisecond):
	}
}

func reportRig(t *testing.T) (*callbackRig, *ctrlRuns) {
	t.Helper()
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"), teamAgent("gamma", "t"))
	rig.server.dirLocks = dirlock.NewManager(func(string) { rig.server.subagentSig.locksChanged(rig.server) })
	c := newCtrlRuns(rig)
	return rig, c
}

func spawnTwo(t *testing.T, rig *callbackRig, c *ctrlRuns) (string, string) {
	t.Helper()
	a, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, c.started)
	b, err := rig.server.SpawnSubagent(context.Background(), spawnReq("gamma"))
	require.NoError(t, err)
	waitStarted(t, c.started)
	return a, b
}

type waitOutcome struct {
	res []SubagentWaitResult
	err error
}

func startWait(rig *callbackRig, ctx context.Context, ids []string, anyOf bool, timeout time.Duration) chan waitOutcome {
	out := make(chan waitOutcome, 1)
	go func() {
		res, err := rig.server.WaitSubagents(ctx, rigChat, ids, anyOf, timeout)
		out <- waitOutcome{res, err}
	}()
	return out
}

func expectNoOutcome(t *testing.T, ch chan waitOutcome) {
	t.Helper()
	select {
	case o := <-ch:
		t.Fatalf("wait returned early: %+v", o)
	case <-time.After(100 * time.Millisecond):
	}
}

func getOutcome(t *testing.T, ch chan waitOutcome) waitOutcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(5 * time.Second):
		t.Fatal("wait never returned")
		return waitOutcome{}
	}
}

func waitRegistered(t *testing.T, rig *callbackRig) {
	t.Helper()
	require.Eventually(t, func() bool {
		g := &rig.server.subagentSig
		g.mu.Lock()
		defer g.mu.Unlock()
		return len(g.waits) > 0
	}, 5*time.Second, 5*time.Millisecond)
}

func resultByID(res []SubagentWaitResult, id string) SubagentWaitResult {
	for _, r := range res {
		if r.Info.ID == id {
			return r
		}
	}
	return SubagentWaitResult{}
}

func TestWaitAllReturnsWhenEveryChildFinished(t *testing.T) {
	rig, c := reportRig(t)
	a, b := spawnTwo(t, rig, c)
	require.NoError(t, rig.repo.AppendMessage(a, dbmodels.ChatMessage{ID: "m1", Role: "assistant", Content: "result of a"}))

	ch := startWait(rig, context.Background(), nil, false, 0)
	waitRegistered(t, rig)
	c.finish(a, nil)
	expectNoOutcome(t, ch)
	c.finish(b, nil)
	o := getOutcome(t, ch)
	require.NoError(t, o.err)
	require.Len(t, o.res, 2)
	ra := resultByID(o.res, a)
	assert.False(t, ra.StillRunning)
	assert.Equal(t, SubagentStateDone, ra.Info.State)
	assert.Equal(t, "result of a", ra.Info.Summary)
	assert.Contains(t, ra.Notes, "/shared")
	assert.Equal(t, SubagentStateDone, resultByID(o.res, b).Info.State)

	// The wait delivered the results: no push to the parent.
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat))
}

func TestWaitAnyReturnsOnFirstFinish(t *testing.T) {
	rig, c := reportRig(t)
	a, b := spawnTwo(t, rig, c)
	ch := startWait(rig, context.Background(), []string{a, b}, true, 0)
	waitRegistered(t, rig)
	expectNoOutcome(t, ch)
	c.finish(b, errors.New("boom"))
	o := getOutcome(t, ch)
	require.NoError(t, o.err)
	require.Len(t, o.res, 2)
	assert.False(t, resultByID(o.res, b).StillRunning)
	assert.Equal(t, SubagentStateFailed, resultByID(o.res, b).Info.State)
	ra := resultByID(o.res, a)
	assert.True(t, ra.StillRunning)
	assert.Empty(t, ra.Notes)

	// a is not covered by the returned wait: its finish is pushed.
	c.finish(a, nil)
	require.Eventually(t, func() bool { return len(c.promptsOf(rigChat)) > 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, c.promptsOf(rigChat)[0], "[subagent "+a+" (work) finished: done]")
}

func TestWaitTimeoutReportsStillRunning(t *testing.T) {
	rig, c := reportRig(t)
	a, _ := spawnTwo(t, rig, c)
	start := time.Now()
	res, err := rig.server.WaitSubagents(context.Background(), rigChat, []string{a}, false, 50*time.Millisecond)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
	require.Len(t, res, 1)
	assert.True(t, res[0].StillRunning)
	assert.Equal(t, SubagentStateRunning, res[0].Info.State)
}

func TestWaitReturnsWhenCallerRunEnds(t *testing.T) {
	rig, c := reportRig(t)
	a, _ := spawnTwo(t, rig, c)
	ctx, cancel := context.WithCancel(context.Background())
	ch := startWait(rig, ctx, []string{a}, false, 0)
	waitRegistered(t, rig)
	cancel()
	o := getOutcome(t, ch)
	var se *SubagentError
	require.ErrorAs(t, o.err, &se)
	assert.Equal(t, SubagentDenied, se.Kind)
	g := &rig.server.subagentSig
	g.mu.Lock()
	defer g.mu.Unlock()
	assert.Empty(t, g.waits)
	assert.Empty(t, g.marked, "the mark is gone with the wait")
}

func TestWaitOwnershipAndEmpty(t *testing.T) {
	rig, c := reportRig(t)
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: "chat-other", CurrentAgent: "alpha"}))
	foreign := &dbmodels.Session{ChatID: "chat-foreign-child", CurrentAgent: "beta", ParentChatID: "chat-other"}
	require.NoError(t, rig.repo.SaveSession(foreign))

	var se *SubagentError
	_, err := rig.server.WaitSubagents(context.Background(), rigChat, []string{foreign.ChatID}, false, time.Second)
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentNotFound, se.Kind)

	// No running children: nothing to wait for.
	res, err := rig.server.WaitSubagents(context.Background(), rigChat, nil, false, time.Second)
	require.NoError(t, err)
	assert.Empty(t, res)

	// A grandchild is allowed.
	child, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, c.started)
	req := spawnReq("gamma")
	req.CallerChat = child
	grand, err := rig.server.SpawnSubagent(context.Background(), req)
	require.NoError(t, err)
	waitStarted(t, c.started)
	res, err = rig.server.WaitSubagents(context.Background(), rigChat, []string{grand}, false, 20*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.True(t, res[0].StillRunning)
}

func TestWaitAlreadyFinishedReturnsAtOnce(t *testing.T) {
	rig, c := reportRig(t)
	a, _ := spawnTwo(t, rig, c)
	c.finish(a, nil)
	waitIdle(t, rig, a)
	res, err := rig.server.WaitSubagents(context.Background(), rigChat, []string{a}, false, time.Minute)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.False(t, res[0].StillRunning)
}

// ---- waiting mark ---------------------------------------------------------

func lockReq(chat string, ancestors ...string) dirlock.AcquireRequest {
	return dirlock.AcquireRequest{ChatID: chat, Dirs: []string{"/work/repo"}, Mode: dirlock.ModeExclusive, Ancestors: ancestors}
}

// acquireAsync acquires in the background and reports the release func.
func acquireAsync(rig *callbackRig, ctx context.Context, req dirlock.AcquireRequest) chan func(bool) {
	got := make(chan func(bool), 1)
	go func() {
		if rel, err := rig.server.dirLocks.Acquire(ctx, req); err == nil {
			got <- rel
		}
	}()
	return got
}

func expectGranted(t *testing.T, got chan func(bool)) func(bool) {
	t.Helper()
	select {
	case rel := <-got:
		return rel
	case <-time.After(5 * time.Second):
		t.Fatal("lock not granted")
		return nil
	}
}

func expectQueued(t *testing.T, rig *callbackRig, chat string) {
	t.Helper()
	require.Eventually(t, func() bool {
		st := rig.server.dirLocks.StatusForChat(chat)
		return st != nil && st.IsWaitingForLock
	}, 5*time.Second, 5*time.Millisecond)
}

func TestWaitMarksCallerWhileBlockedAndClearsAfter(t *testing.T) {
	rig, c := reportRig(t)
	ctx := t.Context()
	relParent, err := rig.server.dirLocks.Acquire(ctx, lockReq(rigChat))
	require.NoError(t, err)
	defer relParent(false)
	a, _ := spawnTwo(t, rig, c)

	// Before the wait the writable child queues behind its parent.
	pre := acquireAsync(rig, ctx, lockReq("child-pre", rigChat))
	expectQueued(t, rig, "child-pre")

	ch := startWait(rig, context.Background(), []string{a}, false, 0)
	waitRegistered(t, rig)
	relPre := expectGranted(t, pre) // the mark let the queued child through
	relPre(false)
	during := expectGranted(t, acquireAsync(rig, ctx, lockReq("child-during", rigChat)))
	during(false)

	c.finish(a, nil)
	getOutcome(t, ch)
	// The wait is over: the mark is cleared and children queue again.
	_ = acquireAsync(rig, ctx, lockReq("child-after", rigChat))
	expectQueued(t, rig, "child-after")
	g := &rig.server.subagentSig
	g.mu.Lock()
	assert.Empty(t, g.marked)
	g.mu.Unlock()
}

func TestWaitMarkKeptWhileDescendantHoldsAndRetried(t *testing.T) {
	rig, c := reportRig(t)
	ctx := t.Context()
	relParent, err := rig.server.dirLocks.Acquire(ctx, lockReq(rigChat))
	require.NoError(t, err)
	defer relParent(false)
	a, _ := spawnTwo(t, rig, c)

	ch := startWait(rig, context.Background(), []string{a}, false, 100*time.Millisecond)
	waitRegistered(t, rig)
	// A writable descendant takes the checkout during the wait and keeps it.
	var relChild func(bool)
	require.Eventually(t, func() bool {
		rel, err := rig.server.dirLocks.Acquire(ctx, lockReq("child-w", rigChat))
		relChild = rel
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	o := getOutcome(t, ch)
	require.NoError(t, o.err)
	assert.True(t, o.res[0].StillRunning, "timed out")

	// The clear was refused: the mark stays and children still bypass.
	g := &rig.server.subagentSig
	g.mu.Lock()
	_, stillMarked := g.marked[rigChat]
	g.mu.Unlock()
	assert.True(t, stillMarked)
	assert.EqualValues(t, 1, g.pending.Load())
	other := expectGranted(t, acquireAsync(rig, ctx, dirlock.AcquireRequest{ChatID: "child-w2", Dirs: []string{"/other"}, Mode: dirlock.ModeExclusive, Ancestors: []string{rigChat}}))
	other(false)

	// The run ends while the descendant still holds: still pending.
	rig.server.onExecutionReleased(rigChat)
	g.mu.Lock()
	_, stillMarked = g.marked[rigChat]
	g.mu.Unlock()
	assert.True(t, stillMarked)

	// The descendant lets go: the lock change retries and clears the mark.
	relChild(false)
	require.Eventually(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return len(g.marked) == 0
	}, 5*time.Second, 10*time.Millisecond)
	assert.Zero(t, g.pending.Load())
	_ = acquireAsync(rig, ctx, lockReq("child-late", rigChat))
	expectQueued(t, rig, "child-late")
}

func TestRunEndClearsMarkOfInFlightWait(t *testing.T) {
	rig, _ := reportRig(t)
	reg := &waitReg{chat: rigChat, ids: map[string]bool{}, wake: make(chan struct{}, 1), claimed: map[string]bool{}}
	rig.server.holdWaitMark(rigChat, reg)
	g := &rig.server.subagentSig
	g.mu.Lock()
	assert.Len(t, g.marked[rigChat], 1)
	g.mu.Unlock()

	rig.server.onExecutionReleased(rigChat)
	g.mu.Lock()
	assert.Empty(t, g.marked)
	g.mu.Unlock()
	// The late release of the wait is a no-op.
	rig.server.releaseWaitMark(rigChat, reg)
}

func TestWaitMarksAreCountedPerWait(t *testing.T) {
	rig, _ := reportRig(t)
	r1 := &waitReg{chat: rigChat}
	r2 := &waitReg{chat: rigChat}
	rig.server.holdWaitMark(rigChat, r1)
	rig.server.holdWaitMark(rigChat, r2)
	rig.server.releaseWaitMark(rigChat, r1)
	g := &rig.server.subagentSig
	g.mu.Lock()
	assert.Len(t, g.marked[rigChat], 1, "the other wait keeps the mark")
	g.mu.Unlock()
	rig.server.releaseWaitMark(rigChat, r2)
	g.mu.Lock()
	assert.Empty(t, g.marked)
	g.mu.Unlock()
}

// ---- reply ----------------------------------------------------------------

func spawnOne(t *testing.T, rig *callbackRig, c *ctrlRuns) string {
	t.Helper()
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, c.started)
	return id
}

func parentRunning(t *testing.T, rig *callbackRig, inject func(string) (bool, error)) *fakeInjector {
	t.Helper()
	holdChat(t, rig)
	inj := &fakeInjector{fn: inject}
	runs := dispatch.NewActiveRuns()
	runs.Register(rigChat, "alpha", "node", "run-parent")
	rig.server.injector = inj
	rig.server.activeRuns = runs
	return inj
}

func TestReplyInjectsIntoRunningParent(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	inj := parentRunning(t, rig, func(string) (bool, error) { return true, nil })

	got, err := rig.server.ReplyToParent(context.Background(), child, "which branch?")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryInjected, got)
	assert.Equal(t, []string{"[message from subagent " + child + " (work)]\nwhich branch?"}, inj.got)
	full, err := rig.repo.GetSession(rigChat)
	require.NoError(t, err)
	assert.Contains(t, full.Messages[len(full.Messages)-1].Content, "which branch?")
}

func TestReplyQueuesWhenParentCannotInject(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	parentRunning(t, rig, func(string) (bool, error) { return false, nil })

	got, err := rig.server.ReplyToParent(context.Background(), child, "need input")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryQueued, got)
	q, err := rig.repo.GetQueuedMessages(rigChat)
	require.NoError(t, err)
	require.Len(t, q, 1)
	assert.Equal(t, "[message from subagent "+child+" (work)]\nneed input", q[0].Prompt)
}

func TestReplyStartsIdleParent(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	got, err := rig.server.ReplyToParent(context.Background(), child, "done early")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryStarted, got)
	assert.Equal(t, rigChat, waitStarted(t, c.started))
	assert.Equal(t, []string{"[message from subagent " + child + " (work)]\ndone early"}, c.promptsOf(rigChat))
}

func TestReplyErrors(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	var se *SubagentError

	// A chat that is not a subagent has nobody to reply to.
	_, err := rig.server.ReplyToParent(context.Background(), rigChat, "hello?")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentInvalid, se.Kind)
	assert.Contains(t, se.Msg, "no parent")

	_, err = rig.server.ReplyToParent(context.Background(), child, "  ")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentInvalid, se.Kind)

	// The parent chat is gone.
	orphan := &dbmodels.Session{ChatID: "chat-orphan", CurrentAgent: "beta", ParentChatID: "chat-missing"}
	require.NoError(t, rig.repo.SaveSession(orphan))
	_, err = rig.server.ReplyToParent(context.Background(), orphan.ChatID, "hi")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentNotFound, se.Kind)
}

func TestReplyDoesNotRestartStoppedParent(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)

	// The caller's run is over.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var se *SubagentError
	_, err := rig.server.ReplyToParent(ctx, child, "late")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentDenied, se.Kind)

	// The user stopped the parent (marker still set).
	rig.server.cancelledExecutions.Store(rigChat, time.Now())
	_, err = rig.server.ReplyToParent(context.Background(), child, "parent was stopped")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentDenied, se.Kind)
	c.noRunFor(t, rigChat)
	q, _ := rig.repo.GetQueuedMessages(rigChat)
	assert.Empty(t, q)
	assert.Empty(t, c.promptsOf(rigChat))
}

// ---- push on finish -------------------------------------------------------

func TestPushStartsIdleParent(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	require.NoError(t, rig.repo.AppendMessage(child, dbmodels.ChatMessage{ID: "m1", Role: "assistant", Content: "all\ndone here"}))
	c.finish(child, nil)

	assert.Equal(t, rigChat, waitStarted(t, c.started))
	assert.Equal(t, []string{"[subagent " + child + " (work) finished: done] all done here; full output: see `subagent status " + child + "` or /shared"}, c.promptsOf(rigChat))
}

func TestPushInjectsIntoRunningParent(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	inj := parentRunning(t, rig, func(string) (bool, error) { return true, nil })
	c.finish(child, errors.New("boom"))

	require.Eventually(t, func() bool {
		inj.mu.Lock()
		defer inj.mu.Unlock()
		return len(inj.got) == 1
	}, 5*time.Second, 10*time.Millisecond)
	inj.mu.Lock()
	defer inj.mu.Unlock()
	assert.Contains(t, inj.got[0], "[subagent "+child+" (work) finished: failed]")
}

func TestPushQueuesForNonInjectableParentAndCoalesces(t *testing.T) {
	rig, c := reportRig(t)
	a, b := spawnTwo(t, rig, c)
	parentRunning(t, rig, func(string) (bool, error) { return false, nil })

	c.finish(a, nil)
	require.Eventually(t, func() bool {
		q, _ := rig.repo.GetQueuedMessages(rigChat)
		return len(q) == 1
	}, 5*time.Second, 10*time.Millisecond)
	c.finish(b, nil)
	require.Eventually(t, func() bool {
		q, _ := rig.repo.GetQueuedMessages(rigChat)
		return len(q) == 1 && strings.Contains(q[0].Prompt, "[subagent "+b+" ") && strings.Contains(q[0].Prompt, "[subagent "+a+" ")
	}, 5*time.Second, 10*time.Millisecond)
	q, _ := rig.repo.GetQueuedMessages(rigChat)
	assert.Len(t, q, 1, "both finishes share one queue slot")
}

func TestPushSkippedWhenParentWaits(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	inj := parentRunning(t, rig, func(string) (bool, error) { return true, nil })
	ch := startWait(rig, context.Background(), []string{child}, false, 0)
	waitRegistered(t, rig)
	c.finish(child, nil)
	o := getOutcome(t, ch)
	require.NoError(t, o.err)
	assert.False(t, o.res[0].StillRunning)
	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, inj.calls.Load(), "the wait result is the notification")
	q, _ := rig.repo.GetQueuedMessages(rigChat)
	assert.Empty(t, q)
}

func TestPushSkippedWhenParentStopped(t *testing.T) {
	rig, c := reportRig(t)
	_ = spawnOne(t, rig, c)
	// The user stops the (idle) parent: its running child is stopped with it.
	code, msg := rig.server.stopSessionExecution(rigChat)
	require.Equal(t, 200, code, msg)
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat))
	q, _ := rig.repo.GetQueuedMessages(rigChat)
	assert.Empty(t, q)
}

func TestPushSkippedForGrandchildrenOfKilledChild(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	req := spawnReq("gamma")
	req.CallerChat = child
	grand, err := rig.server.SpawnSubagent(context.Background(), req)
	require.NoError(t, err)
	waitStarted(t, c.started)

	_, err = rig.server.KillSubagent(rigChat, child)
	require.NoError(t, err)
	waitIdle(t, rig, grand)
	// The killer hears about neither the child nor the grandchild.
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat))
	assert.Len(t, c.promptsOf(child), 1, "the killed child is not restarted by its grandchild")
}

func TestPushSkippedWhenParentChatIsGone(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	require.NoError(t, rig.repo.DeleteSessionRecord(rigChat))
	c.finish(child, nil)
	waitIdle(t, rig, child)
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat))
}

func TestPushNotSentWhileChildHasQueuedWork(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	_, err := rig.repo.EnqueueMessage(child, "more", "")
	require.NoError(t, err)
	c.finish(child, nil)
	// The queue consumer runs the queued message; the child is not finished.
	assert.Equal(t, child, waitStarted(t, c.started))
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat))
}

func TestPushOnUserStopOfChild(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	// Someone other than the parent stops the child chat: the parent hears.
	code, msg := rig.server.stopSessionExecution(child)
	require.Equal(t, 200, code, msg)
	assert.Equal(t, rigChat, waitStarted(t, c.started))
	assert.Contains(t, c.promptsOf(rigChat)[0], "[subagent "+child+" (work) finished: killed]")
}

func TestQuietKeptByReleaseThatDoesNotFinishChat(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	g := &rig.server.subagentSig
	g.mu.Lock()
	g.initLocked()
	g.quiet[child] = true
	g.mu.Unlock()

	// A stray release while the child still runs does not use the marker up.
	rig.server.onExecutionReleased(child)
	g.mu.Lock()
	assert.True(t, g.quiet[child])
	g.mu.Unlock()

	// The release that ends the chat does, and pushes nothing.
	c.finish(child, nil)
	require.Eventually(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return !g.quiet[child]
	}, 5*time.Second, 10*time.Millisecond)
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat))
}

func TestQuietClearedWhenNewRunBegins(t *testing.T) {
	rig, _ := reportRig(t)
	g := &rig.server.subagentSig
	g.mu.Lock()
	g.initLocked()
	g.quiet["chat-x"] = true
	g.mu.Unlock()
	_, handle, loaded := rig.server.beginExecution("chat-x", "beta", false)
	require.False(t, loaded)
	defer rig.server.releaseExecution("chat-x")
	defer handle.cancel()
	g.mu.Lock()
	assert.False(t, g.quiet["chat-x"])
	g.mu.Unlock()
}

func TestQuietClearedWhenStopFails(t *testing.T) {
	rig, _ := reportRig(t)
	code, _ := rig.server.stopDescendant("idle-chat")
	assert.Equal(t, 409, code)
	g := &rig.server.subagentSig
	g.mu.Lock()
	assert.False(t, g.quiet["idle-chat"])
	g.mu.Unlock()
}

func TestPushDedupAcrossOutOfOrderReleases(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	c.finish(child, nil)
	assert.Equal(t, rigChat, waitStarted(t, c.started))
	waitIdle(t, rig, child)

	// Two more releases of the same finish, processed late and out of order.
	g := &rig.server.subagentSig
	g.mu.Lock()
	epoch := g.releaseEpoch[child]
	g.mu.Unlock()
	require.NotZero(t, epoch)
	rig.server.onExecutionReleasedAt(child, epoch)
	rig.server.onExecutionReleasedAt(child, epoch-1)
	time.Sleep(150 * time.Millisecond)
	assert.Len(t, c.promptsOf(rigChat), 1, "one notification for the finish")
	q, _ := rig.repo.GetQueuedMessages(rigChat)
	assert.Empty(t, q)
}

func TestPushDedupSkipsOvertakenRelease(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	g := &rig.server.subagentSig
	// Releases 5 and 6 happened; 6 is processed first and pushes, then the
	// lagging 5 finds the child finished too.
	g.mu.Lock()
	g.initLocked()
	g.releaseEpoch[child] = 6
	g.mu.Unlock()
	rig.server.activeExecutions.Delete(child)
	rig.server.onExecutionReleasedAt(child, 6)
	assert.Equal(t, rigChat, waitStarted(t, c.started))
	rig.server.onExecutionReleasedAt(child, 5)
	c.noRunFor(t, rigChat)
	assert.Len(t, c.promptsOf(rigChat), 1)
}

// toggleRunners is a runner registry whose node can go offline and online.
type toggleRunners struct{ online atomic.Bool }

func (r *toggleRunners) Nodes() []runnerhub.NodeInfo {
	return []runnerhub.NodeInfo{{NodeID: "runner-x", Online: r.online.Load()}}
}

func TestPushRetriesWhileParentRunnerUnavailable(t *testing.T) {
	rig, c := reportRig(t)
	rig.server.pushRetryDelays = []time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond}
	reg := &toggleRunners{}
	reg.online.Store(true)
	rig.server.runners = reg
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "alpha", RunnerNodeID: "runner-x"}))
	child := spawnOne(t, rig, c)

	reg.online.Store(false)
	c.finish(child, nil)
	waitIdle(t, rig, child)
	c.noRunFor(t, rigChat)
	reg.online.Store(true)
	assert.Equal(t, rigChat, waitStarted(t, c.started))
	assert.Contains(t, c.promptsOf(rigChat)[0], "[subagent "+child+" (work) finished: done]")
}

func TestPushQueueFullWarnsInParentChat(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	parentRunning(t, rig, func(string) (bool, error) { return false, nil })
	for i := 0; i < 10; i++ {
		_, _ = rig.repo.EnqueueMessage(rigChat, "fill", "")
	}
	c.finish(child, nil)
	require.Eventually(t, func() bool {
		full, _ := rig.repo.GetSession(rigChat)
		if full == nil {
			return false
		}
		for _, m := range full.Messages {
			if m.ActivityType == "SUBAGENT_NOTICE" && strings.Contains(m.Content, child) {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
}

// ---- stopped chats stay stopped -------------------------------------------

func hasNotice(rig *callbackRig, chat, substr string) bool {
	full, _ := rig.repo.GetSession(chat)
	if full == nil {
		return false
	}
	for _, m := range full.Messages {
		if m.ActivityType == "SUBAGENT_NOTICE" && strings.Contains(m.Content, substr) {
			return true
		}
	}
	return false
}

func TestStoppedParentNotRestartedAfterMarkerConsumed(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	holdChat(t, rig)
	// The user stops the running parent; its run settles, consuming the marker.
	code, msg := rig.server.stopOneExecution(rigChat)
	require.Equal(t, 200, code, msg)
	rig.server.releaseExecution(rigChat)
	require.False(t, rig.server.sessionStopInProgress(rigChat))

	// The child finishes naturally afterwards: its push does not restart the parent.
	c.finish(child, nil)
	waitIdle(t, rig, child)
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat))
	require.Eventually(t, func() bool { return hasNotice(rig, rigChat, "finished: done") }, 5*time.Second, 10*time.Millisecond)

	// A reply is held back the same way.
	var se *SubagentError
	_, err := rig.server.ReplyToParent(context.Background(), child, "late reply")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentDenied, se.Kind)
	c.noRunFor(t, rigChat)
	assert.True(t, hasNotice(rig, rigChat, "late reply"))
	q, _ := rig.repo.GetQueuedMessages(rigChat)
	assert.Empty(t, q)
	assert.True(t, rig.server.isStopped(rigChat))
}

func TestIdleParentStopHoldsRetryingPush(t *testing.T) {
	rig, c := reportRig(t)
	rig.server.pushRetryDelays = []time.Duration{300 * time.Millisecond, 300 * time.Millisecond}
	reg := &toggleRunners{}
	reg.online.Store(true)
	rig.server.runners = reg
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "alpha", RunnerNodeID: "runner-x"}))
	child := spawnOne(t, rig, c)

	// The child finishes while the parent's runner is away: its push retries.
	reg.online.Store(false)
	c.finish(child, nil)
	waitIdle(t, rig, child)
	// The user stops the idle parent, then its runner returns.
	_, _ = rig.server.stopSessionExecution(rigChat)
	assert.True(t, rig.server.isStopped(rigChat))
	reg.online.Store(true)

	require.Eventually(t, func() bool { return hasNotice(rig, rigChat, "finished: done") }, 5*time.Second, 10*time.Millisecond)
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat))
}

func TestIdleParentStopHoldsHandshakeKilledPush(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	// The child is between runs with a message about to run.
	rig.server.activeExecutions.Delete(child)
	_, err := rig.repo.EnqueueMessage(child, "more", "")
	require.NoError(t, err)

	code, msg := rig.server.stopSessionExecution(rigChat)
	require.Equal(t, 409, code, msg)
	require.True(t, rig.server.isStopped(rigChat))
	rig.server.onExecutionReleasedAt(child, 1)
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat))
	require.Eventually(t, func() bool { return hasNotice(rig, rigChat, "finished: killed") }, 5*time.Second, 10*time.Millisecond)
	c.finish(child, nil)
}

func TestIdleParentStopHoldsParkedChildResume(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	_, _ = rig.server.stopSessionExecution(rigChat)
	require.True(t, rig.server.isStopped(rigChat))
	waitIdle(t, rig, child)
	rig.server.parkQueue(child)
	_, err := rig.repo.EnqueueMessage(child, "later", "")
	require.NoError(t, err)

	// The parked child resumes and finishes: the stopped parent stays stopped.
	rig.server.unparkQueue(child)
	rig.server.startQueueConsumerIfIdle(child, rig.server.agents[1], "")
	waitStarted(t, c.started)
	c.finish(child, nil)
	waitIdle(t, rig, child)
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat))
	require.Eventually(t, func() bool { return hasNotice(rig, rigChat, "finished: ") }, 5*time.Second, 10*time.Millisecond)
}

func TestStoppedMarkClearedByUserMessage(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	rig.server.markStopped(rigChat)

	// A push/reply keeps the hold ...
	_, err := rig.server.ReplyToParent(context.Background(), child, "hello")
	require.Error(t, err)
	assert.True(t, rig.server.isStopped(rigChat))

	// ... a user message ends it and runs the chat.
	rec := newResponseCapture()
	target, err := rig.server.findAgentFor("", "alpha")
	require.NoError(t, err)
	rig.server.triggerMessage(context.Background(), nil, rec, "alpha", target, TriggerMessageRequest{Prompt: "continue", ChatID: rigChat}, false)
	assert.Equal(t, rigChat, waitStarted(t, c.started))
	assert.False(t, rig.server.isStopped(rigChat))

	// The parent runs again: a reply is injected/queued normally.
	_, err = rig.server.ReplyToParent(context.Background(), child, "now fine")
	require.NoError(t, err)
}

func TestCascadeClearsQueueOfChildMissedInDrainHandshake(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	holdChat(t, rig)
	// The child is between runs with a message about to run (guard released
	// in the consumer's handshake).
	rig.server.activeExecutions.Delete(child)
	_, err := rig.repo.EnqueueMessage(child, "more", "")
	require.NoError(t, err)

	code, msg := rig.server.stopSessionExecution(rigChat)
	require.Equal(t, 200, code, msg)
	q, _ := rig.repo.GetQueuedMessages(child)
	assert.Empty(t, q)
	sess, _ := rig.repo.GetSessionMeta(child)
	assert.Equal(t, dbmodels.SpawnOutcomeKilled, sess.SpawnOutcome)
	assert.True(t, rig.server.isStopped(child))
	c.finish(child, nil)
}

func TestIdleStopDropsQueuedPushOfParkedParent(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	// The parent's finish push is queued while the runner is away.
	rig.server.parkQueue(rigChat)
	_, err := rig.repo.EnqueueMessage(rigChat, "[subagent "+child+" (work) finished: done]", "")
	require.NoError(t, err)

	_, _ = rig.server.stopSessionExecution(rigChat)
	q, _ := rig.repo.GetQueuedMessages(rigChat)
	assert.Empty(t, q)
	assert.True(t, rig.server.isStopped(rigChat))

	// The runner returns: nothing is left to run the stopped parent.
	rig.server.unparkQueue(rigChat)
	rig.server.resumeQueuedChat(rigChat)
	c.noRunFor(t, rigChat)
	assert.Empty(t, c.promptsOf(rigChat))
}

func TestIdleStopDropsQueueOfTargetMissedInDrainHandshake(t *testing.T) {
	rig, c := reportRig(t)
	// The target is between runs with a message about to run (guard released
	// in the consumer's handshake); it has no running subagent.
	_, err := rig.repo.EnqueueMessage(rigChat, "more", "")
	require.NoError(t, err)

	code, _ := rig.server.stopSessionExecution(rigChat)
	assert.Equal(t, http.StatusConflict, code)
	q, _ := rig.repo.GetQueuedMessages(rigChat)
	assert.Empty(t, q)
	assert.True(t, rig.server.isStopped(rigChat))
	rig.server.startQueueConsumerIfIdle(rigChat, rig.server.agents[0], "")
	c.noRunFor(t, rigChat)
}

func TestIdleStopRecordsKilledOnlyForSubagentTarget(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	c.finish(child, nil)
	waitIdle(t, rig, child)
	_, err := rig.repo.EnqueueMessage(child, "more", "")
	require.NoError(t, err)
	rig.server.parkQueue(child)

	code, _ := rig.server.stopSessionExecution(child)
	assert.Equal(t, http.StatusConflict, code)
	q, _ := rig.repo.GetQueuedMessages(child)
	assert.Empty(t, q)
	sess, _ := rig.repo.GetSessionMeta(child)
	assert.Equal(t, dbmodels.SpawnOutcomeKilled, sess.SpawnOutcome)
}

func TestSubagentSigForget(t *testing.T) {
	g := &subagentSignals{}
	g.mu.Lock()
	g.initLocked()
	g.releaseEpoch["x"] = 3
	g.lastPushed["x"] = 3
	g.queuedPush["x"] = "m"
	g.quiet["x"] = true
	g.stopped["x"] = true
	g.releaseEpoch["y"] = 1
	g.mu.Unlock()
	g.forget("x")
	assert.NotContains(t, g.releaseEpoch, "x")
	assert.NotContains(t, g.lastPushed, "x")
	assert.NotContains(t, g.queuedPush, "x")
	assert.NotContains(t, g.quiet, "x")
	assert.NotContains(t, g.stopped, "x")
	assert.Contains(t, g.releaseEpoch, "y")
}

func TestDeleteAndArchiveForgetSubagentState(t *testing.T) {
	for _, op := range []string{"delete", "archive"} {
		t.Run(op, func(t *testing.T) {
			rig, _ := reportRig(t)
			rig.server.bumpReleaseEpoch(rigChat)
			rig.server.markStopped(rigChat)
			g := &rig.server.subagentSig
			g.mu.Lock()
			g.queuedPush[rigChat] = "m"
			g.mu.Unlock()

			w := httptest.NewRecorder()
			if op == "delete" {
				rig.server.handleDeleteSession(w, httptest.NewRequest(http.MethodDelete, "/api/sessions?chat_id="+rigChat, nil))
			} else {
				r := httptest.NewRequest(http.MethodPost, "/api/sessions/"+rigChat+"/archive", nil)
				r.SetPathValue("id", rigChat)
				rig.server.handleArchiveSession(w, r)
			}
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			g.mu.Lock()
			defer g.mu.Unlock()
			assert.NotContains(t, g.releaseEpoch, rigChat)
			assert.NotContains(t, g.queuedPush, rigChat)
			assert.NotContains(t, g.stopped, rigChat)
		})
	}
}

func TestPushFinalRetryFailureWarnsInParentChat(t *testing.T) {
	rig, c := reportRig(t)
	rig.server.pushRetryDelays = []time.Duration{30 * time.Millisecond}
	reg := &toggleRunners{}
	reg.online.Store(true)
	rig.server.runners = reg
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "alpha", RunnerNodeID: "runner-x"}))
	child := spawnOne(t, rig, c)

	reg.online.Store(false)
	c.finish(child, nil)
	require.Eventually(t, func() bool { return hasNotice(rig, rigChat, child) }, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, c.promptsOf(rigChat))
}
