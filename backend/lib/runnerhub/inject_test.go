package runnerhub

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// These tests tweak the package-level inject timing, so they must not run in
// parallel with anything (parallel tests only resume after all serial ones).
func setInjectTiming(t *testing.T, timeout, margin time.Duration) {
	t.Helper()
	oldT, oldM := InjectTimeout, InjectMargin
	InjectTimeout, InjectMargin = timeout, margin
	t.Cleanup(func() { InjectTimeout, InjectMargin = oldT, oldM })
}

type injectOutcome struct {
	injected bool
	err      error
}

func startInject(env *testEnv, runID, prompt string) <-chan injectOutcome {
	ch := make(chan injectOutcome, 1)
	go func() {
		ok, err := env.hub.Inject(context.Background(), "runner-a", runID, prompt)
		ch <- injectOutcome{ok, err}
	}()
	return ch
}

func awaitInject(t *testing.T, ch <-chan injectOutcome) injectOutcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Inject did not return")
		return injectOutcome{}
	}
}

func (f *fakeRunner) sendInjectResult(res *runnerpb.InjectResult) uint64 {
	f.t.Helper()
	return f.send(&runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_InjectResult{InjectResult: res}})
}

// nextInject returns the next InjectMessage the runner receives.
func (f *fakeRunner) nextInject() *runnerpb.InjectMessage {
	f.t.Helper()
	m := f.nextBackend()
	require.NotNil(f.t, m.GetInjectMessage(), "expected InjectMessage, got %v", m)
	return m.GetInjectMessage()
}

func injectEnv(t *testing.T) (*testEnv, *fakeRunner, *Run) {
	t.Helper()
	env := newTestEnv(t, nil, nil, "runner-a")
	f := env.newRunner("runner-a")
	f.mustConnect()
	run, err := env.hub.Dispatch(context.Background(), "runner-a", &runnerpb.StartRun{RunId: "r1"})
	require.NoError(t, err)
	f.nextBackend() // StartRun
	return env, f, run
}

func TestInjectOK(t *testing.T) {
	setInjectTiming(t, 2*time.Second, 200*time.Millisecond)
	env, f, _ := injectEnv(t)

	before := time.Now()
	out := startInject(env, "r1", "hello")
	im := f.nextInject()
	assert.Equal(t, "r1", im.GetRunId())
	assert.Equal(t, "hello", im.GetPrompt())
	assert.NotEmpty(t, im.GetInjectId())
	assert.GreaterOrEqual(t, im.GetDeadlineUnixMs(), before.Add(2*time.Second).UnixMilli())

	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Ok: true})
	o := awaitInject(t, out)
	require.NoError(t, o.err)
	assert.True(t, o.injected)

	env.hub.mu.Lock()
	assert.Empty(t, env.hub.injects, "waiter must be cleaned up")
	env.hub.mu.Unlock()
}

func TestInjectNotInjectable(t *testing.T) {
	setInjectTiming(t, 2*time.Second, 200*time.Millisecond)
	env, f, _ := injectEnv(t)

	out := startInject(env, "r1", "hello")
	im := f.nextInject()
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Reason: "not_injectable"})
	o := awaitInject(t, out)
	require.NoError(t, o.err)
	assert.False(t, o.injected)
}

func TestInjectRunnerError(t *testing.T) {
	setInjectTiming(t, 2*time.Second, 200*time.Millisecond)
	env, f, _ := injectEnv(t)

	out := startInject(env, "r1", "hello")
	im := f.nextInject()
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Reason: "error", Error: "boom"})
	o := awaitInject(t, out)
	require.ErrorIs(t, o.err, ErrInjectFailed)
	assert.False(t, o.injected)
}

func TestInjectTimeoutWaitsPastDeadline(t *testing.T) {
	setInjectTiming(t, 200*time.Millisecond, 300*time.Millisecond)
	env, f, _ := injectEnv(t)

	out := startInject(env, "r1", "hello")
	im := f.nextInject()

	// A result that arrives after the deadline but inside the margin is still
	// honoured: the runner (not our give-up) decided.
	time.Sleep(300 * time.Millisecond)
	select {
	case <-out:
		require.FailNow(t, "Inject gave up at the deadline")
	default:
	}
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Ok: true})
	o := awaitInject(t, out)
	require.NoError(t, o.err)
	assert.True(t, o.injected)

	// No result at all: gives up only after deadline + margin.
	start := time.Now()
	out = startInject(env, "r1", "again")
	f.nextInject()
	o = awaitInject(t, out)
	require.NoError(t, o.err)
	assert.False(t, o.injected)
	assert.GreaterOrEqual(t, time.Since(start), 450*time.Millisecond)
}

func TestInjectTimeoutDropsQueuedMessage(t *testing.T) {
	setInjectTiming(t, 100*time.Millisecond, 50*time.Millisecond)
	env, f, _ := injectEnv(t)

	// The runner processed the StartRun and then goes away.
	f.lastBackendSeq = 1
	f.active = []*runnerpb.RunRef{{RunId: "r1"}}
	f.disconnect()
	out := startInjectOffline(env)
	o := awaitInject(t, out)
	require.ErrorIs(t, o.err, ErrRunnerOffline)
	assert.False(t, o.injected)

	// Connected but unanswered: after giving up, the unacked InjectMessage must
	// not be replayed on a later reconnect.
	f.mustConnect()
	out = startInject(env, "r1", "hello")
	f.nextInject()
	o = awaitInject(t, out)
	require.NoError(t, o.err)
	assert.False(t, o.injected)
	f.disconnect()
	f.mustConnect()
	f.expectNoBackend()
}

func startInjectOffline(env *testEnv) <-chan injectOutcome { return startInject(env, "r1", "x") }

func TestInjectRunnerDisconnect(t *testing.T) {
	setInjectTiming(t, 2*time.Second, 200*time.Millisecond)
	env, f, _ := injectEnv(t)

	// Disconnect before the runner processed the InjectMessage: it is replayed
	// after the reconnect and answered in time.
	out := startInject(env, "r1", "hello")
	first := f.nextInject()
	f.lastBackendSeq = 1 // processed the StartRun only
	f.active = []*runnerpb.RunRef{{RunId: "r1"}}
	f.disconnect()
	f.mustConnect()
	replay := f.nextInject()
	assert.Equal(t, first.GetInjectId(), replay.GetInjectId())
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: replay.GetInjectId(), Ok: true})
	o := awaitInject(t, out)
	require.NoError(t, o.err)
	assert.True(t, o.injected)
}

func TestInjectRunnerDisconnectNeverReturns(t *testing.T) {
	setInjectTiming(t, 200*time.Millisecond, 100*time.Millisecond)
	env, f, _ := injectEnv(t)

	out := startInject(env, "r1", "hello")
	im := f.nextInject()
	f.lastBackendSeq = 2
	f.active = []*runnerpb.RunRef{{RunId: "r1"}}
	f.disconnect()
	// Runner stays away past deadline + margin.
	o := awaitInject(t, out)
	require.NoError(t, o.err)
	assert.False(t, o.injected)

	// A late result for the abandoned inject is ignored.
	f.mustConnect()
	last := f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Ok: true})
	f.waitAck(last)
}

func TestInjectDuplicateResultIgnored(t *testing.T) {
	setInjectTiming(t, 2*time.Second, 200*time.Millisecond)
	env, f, _ := injectEnv(t)

	out := startInject(env, "r1", "hello")
	im := f.nextInject()
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Ok: true})
	o := awaitInject(t, out)
	require.NoError(t, o.err)
	assert.True(t, o.injected)

	// The same result again (new seq) and a result for another id: ignored.
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Reason: "error"})
	last := f.sendInjectResult(&runnerpb.InjectResult{InjectId: "other", Ok: true})
	f.waitAck(last)

	// The connection is still healthy.
	out = startInject(env, "r1", "second")
	im2 := f.nextInject()
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im2.GetInjectId(), Ok: true})
	assert.True(t, awaitInject(t, out).injected)
}

func TestInjectUnknownRunAndOffline(t *testing.T) {
	setInjectTiming(t, 200*time.Millisecond, 50*time.Millisecond)
	env, _, _ := injectEnv(t)

	ok, err := env.hub.Inject(context.Background(), "runner-a", "nope", "x")
	require.ErrorIs(t, err, ErrUnknownRun)
	assert.False(t, ok)
	ok, err = env.hub.Inject(context.Background(), "runner-b", "r1", "x")
	require.ErrorIs(t, err, ErrUnknownRun)
	assert.False(t, ok)
}

// The runner queues its RunResult before the InjectResult, so the run is
// finished by the time an ok arrives: it must still count as injected.
func TestInjectOKAfterRunResult(t *testing.T) {
	setInjectTiming(t, 2*time.Second, 200*time.Millisecond)
	env, f, run := injectEnv(t)

	// Once the run is finished it is untracked, so one inject per env; the
	// package is run with -count/-cpu=1 to vary the interleaving.
	out := startInject(env, "r1", "hello")
	im := f.nextInject()
	f.sendResult(&runnerpb.RunResult{RunId: "r1"})
	mustWait(t, run)
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Ok: true})
	o := awaitInject(t, out)
	require.NoError(t, o.err)
	require.True(t, o.injected)
}

func TestInjectRunFinishedThenNotInjectable(t *testing.T) {
	setInjectTiming(t, 2*time.Second, 200*time.Millisecond)
	env, f, _ := injectEnv(t)

	start := time.Now()
	out := startInject(env, "r1", "hello")
	im := f.nextInject()
	f.sendResult(&runnerpb.RunResult{RunId: "r1"})
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Reason: "not_injectable"})
	o := awaitInject(t, out)
	require.NoError(t, o.err)
	assert.False(t, o.injected)
	assert.Less(t, time.Since(start), time.Second)
}

// A result inside the margin after the run ended is still honoured, and
// without any result Inject gives up only at deadline + margin.
func TestInjectRunFinishedWaitsForResult(t *testing.T) {
	setInjectTiming(t, 300*time.Millisecond, 300*time.Millisecond)
	env, f, run := injectEnv(t)

	out := startInject(env, "r1", "hello")
	im := f.nextInject()
	f.sendResult(&runnerpb.RunResult{RunId: "r1"})
	mustWait(t, run)
	time.Sleep(400 * time.Millisecond) // past the deadline, inside the margin
	select {
	case <-out:
		require.FailNow(t, "Inject returned before deadline + margin")
	default:
	}
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Ok: true})
	o := awaitInject(t, out)
	require.NoError(t, o.err)
	assert.True(t, o.injected)
}

// Exits other than a result must still honour a result that already sits in
// the waiter's channel (select picks randomly among ready arms).
func TestInjectResultAlreadyBufferedWins(t *testing.T) {
	h := New(Options{})
	defer h.Stop()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	fired := make(chan time.Time, 1)

	for i := range 200 {
		select { // refill: a previous round may have left it unread
		case fired <- time.Now():
		default:
		}
		w := &injectWaiter{ch: make(chan *runnerpb.InjectResult, 1)}
		w.ch <- &runnerpb.InjectResult{InjectId: "x", Ok: true}
		ok, _, err := h.awaitInject(cancelled, w, fired)
		require.NoError(t, err)
		require.True(t, ok, "iteration %d: ctx cancelled", i)

		select {
		case fired <- time.Now():
		default:
		}
		w.ch <- &runnerpb.InjectResult{InjectId: "x", Ok: true}
		ok, _, err = h.awaitInject(context.Background(), w, fired)
		require.NoError(t, err)
		require.True(t, ok, "iteration %d: timer fired", i)
	}

	w := &injectWaiter{ch: make(chan *runnerpb.InjectResult, 1)}
	w.ch <- &runnerpb.InjectResult{InjectId: "x", Ok: true}
	h.Stop()
	ok, _, err := h.awaitInject(context.Background(), w, nil)
	require.NoError(t, err)
	assert.True(t, ok, "hub stopped")
}

// Cancelling the caller's ctx must not end the wait: the runner may still
// inject until its deadline.
func TestInjectContextCancelDoesNotPreempt(t *testing.T) {
	setInjectTiming(t, 400*time.Millisecond, 200*time.Millisecond)
	env, f, _ := injectEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	run := func() chan injectOutcome {
		done := make(chan injectOutcome, 1)
		go func() {
			ok, err := env.hub.Inject(ctx, "runner-a", "r1", "hello")
			done <- injectOutcome{ok, err}
		}()
		return done
	}

	// An ok after the cancel, past the deadline but inside the margin, wins.
	done := run()
	im := f.nextInject()
	cancel()
	time.Sleep(500 * time.Millisecond)
	select {
	case <-done:
		require.FailNow(t, "Inject returned early after ctx cancel")
	default:
	}
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Ok: true})
	o := awaitInject(t, done)
	require.NoError(t, o.err)
	assert.True(t, o.injected)

	// Already-cancelled ctx is refused up front.
	ok, err := env.hub.Inject(ctx, "runner-a", "r1", "x")
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, ok)

	// Cancel and no result: gives up only at deadline + margin.
	ctx2, cancel2 := context.WithCancel(context.Background())
	start := time.Now()
	done2 := make(chan injectOutcome, 1)
	go func() {
		ok, err := env.hub.Inject(ctx2, "runner-a", "r1", "again")
		done2 <- injectOutcome{ok, err}
	}()
	f.nextInject()
	cancel2()
	o = awaitInject(t, done2)
	assert.False(t, o.injected)
	if o.err != nil {
		require.ErrorIs(t, o.err, context.Canceled)
	}
	assert.GreaterOrEqual(t, time.Since(start), 550*time.Millisecond)

	env.hub.mu.Lock()
	assert.Empty(t, env.hub.injects)
	env.hub.mu.Unlock()
}

func TestControlRunQueuesHoldAndRelease(t *testing.T) {
	env, f, _ := injectEnv(t)

	require.NoError(t, env.hub.ControlRun("runner-a", "r1", RunControlHold, 90*time.Second))
	rc := f.nextBackend().GetRunControl()
	require.NotNil(t, rc)
	assert.Equal(t, "r1", rc.GetRunId())
	assert.Equal(t, "hold", rc.GetAction())
	assert.EqualValues(t, 90, rc.GetHoldIdleSeconds())

	require.NoError(t, env.hub.ControlRun("runner-a", "r1", RunControlRelease, 0))
	assert.Equal(t, "release", f.nextBackend().GetRunControl().GetAction())

	assert.ErrorIs(t, env.hub.ControlRun("runner-a", "nope", RunControlHold, 0), ErrUnknownRun)
}

func TestInjectGenReturnsRunnersIdleGen(t *testing.T) {
	setInjectTiming(t, 2*time.Second, 200*time.Millisecond)
	env, f, _ := injectEnv(t)
	type out struct {
		ok  bool
		gen uint64
		err error
	}
	ch := make(chan out, 1)
	go func() {
		ok, gen, err := env.hub.InjectGen(context.Background(), "runner-a", "r1", "hello")
		ch <- out{ok, gen, err}
	}()
	im := f.nextInject()
	f.sendInjectResult(&runnerpb.InjectResult{InjectId: im.GetInjectId(), Ok: true, IdleGen: 9})
	o := <-ch
	require.NoError(t, o.err)
	assert.True(t, o.ok)
	assert.Equal(t, uint64(9), o.gen)
}
