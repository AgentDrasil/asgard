package common

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

type fakeWC struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closes int
	err    error
}

func (f *fakeWC) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	return f.buf.Write(p)
}

func (f *fakeWC) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	return nil
}

func lineFmt(p string) ([]byte, error) { return []byte(p + "\n"), nil }

// closesOf waits for the writer goroutine to finish, then returns how often
// stdin was closed.
func closesOf(t *testing.T, inj *Injector, w *fakeWC) int {
	t.Helper()
	select {
	case <-inj.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("writer goroutine did not finish")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closes
}

func TestInjectorClosesWhenAllAnswered(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, false)
	require.NoError(t, inj.Send("a"))
	require.NoError(t, inj.Send("b"))

	inj.OnResult()
	require.NoError(t, inj.Send("c"), "one result for two messages must keep stdin open")
	inj.OnResult()
	require.NoError(t, inj.Send("d"))
	inj.OnResult()
	inj.OnResult()

	assert.ErrorIs(t, inj.Send("late"), types.ErrNotInjectable)
	inj.OnResult()
	inj.Close()
	assert.Equal(t, 1, closesOf(t, inj, w), "stdin must be closed exactly once")
	assert.Equal(t, "a\nb\nc\nd\n", w.buf.String(), "accepted messages are flushed in order before the close")
}

func TestInjectorWriteErrorMeansNotInjectable(t *testing.T) {
	w := &fakeWC{err: errors.New("broken pipe")}
	inj := NewInjector(w, lineFmt, false)
	_ = inj.Send("a") // accepted; the failure shows up asynchronously
	assert.Equal(t, 1, closesOf(t, inj, w))
	assert.ErrorIs(t, inj.Send("b"), types.ErrNotInjectable)
}

// A write that blocks (CLI not reading, message bigger than the pipe buffer)
// must not stall the stream hooks.
func TestInjectorBlockedWriteDoesNotBlockHooks(t *testing.T) {
	release := make(chan struct{})
	w := &blockingWC{release: release}
	inj := NewInjector(w, lineFmt, true)
	require.NoError(t, inj.Send("big"))

	hooks := make(chan struct{})
	go func() {
		inj.OnEcho()
		inj.OnBackgroundTasks(0)
		inj.OnResult()
		close(hooks)
	}()
	select {
	case <-hooks:
	case <-time.After(5 * time.Second):
		t.Fatal("stream hooks blocked behind a pending pipe write")
	}
	close(release)
	select {
	case <-inj.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not finish after the write was released")
	}
	assert.Equal(t, "big\n", w.buf.String())
}

type blockingWC struct {
	release chan struct{}
	buf     bytes.Buffer
}

func (b *blockingWC) Write(p []byte) (int, error) {
	<-b.release
	return b.buf.Write(p)
}

func (b *blockingWC) Close() error { return nil }

func TestInjectorConcurrentSendsAreAllFlushedOrRejected(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, false)
	require.NoError(t, inj.Send("first"))
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 1
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if inj.Send("x") == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	inj.OnResult()
	wg.Wait()
	inj.Close()
	assert.LessOrEqual(t, closesOf(t, inj, w), 1)
	w.mu.Lock()
	defer w.mu.Unlock()
	assert.Equal(t, accepted, strings.Count(w.buf.String(), "\n"), "exactly the accepted messages are written")
}

func TestInjectorReplayEchoMergedMessagesCloseOnSingleResult(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, true)
	require.NoError(t, inj.Send("a"))
	inj.OnEcho()
	require.NoError(t, inj.Send("b"))
	inj.OnEcho()
	inj.OnResult() // one result covers both merged messages
	assert.ErrorIs(t, inj.Send("c"), types.ErrNotInjectable)
}

func TestInjectorReplayEchoLateMessageKeepsStdinOpen(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, true)
	require.NoError(t, inj.Send("a"))
	inj.OnEcho()
	require.NoError(t, inj.Send("b")) // not echoed before the result: next turn
	inj.OnResult()
	require.NoError(t, inj.Send("c"), "stdin must stay open for the pending message")
	inj.OnEcho()
	inj.OnEcho()
	inj.OnResult()
	assert.ErrorIs(t, inj.Send("d"), types.ErrNotInjectable)
}

func TestInjectorBackgroundTasksKeepStdinOpenUntilFinalResult(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, true)
	require.NoError(t, inj.Send("a"))
	inj.OnEcho()
	inj.OnBackgroundTasks(1)
	inj.OnResult() // interim result while a background subagent runs
	require.NoError(t, inj.Send("b"), "injection must still work while background tasks run")
	inj.OnBackgroundTasks(0)
	inj.OnEcho()
	inj.OnResult() // final result
	assert.ErrorIs(t, inj.Send("c"), types.ErrNotInjectable)
}

func notClosed(t *testing.T, inj *Injector) {
	t.Helper()
	select {
	case <-inj.Done():
		t.Fatal("stdin was closed")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestInjectorHoldKeepsStdinOpenPastTurn(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, false)
	require.NoError(t, inj.Send("a"))
	require.NoError(t, inj.SetHold(true, 0))
	inj.OnResult()
	notClosed(t, inj)

	// A push is injected as a new turn of the same process.
	require.NoError(t, inj.Send("push"))
	inj.OnResult()
	notClosed(t, inj)

	require.NoError(t, inj.SetHold(false, 0), "release of an idle injector closes stdin")
	assert.Equal(t, 1, closesOf(t, inj, w))
	assert.Equal(t, "a\npush\n", w.buf.String())
	assert.ErrorIs(t, inj.Send("late"), types.ErrNotInjectable)
	assert.ErrorIs(t, inj.SetHold(true, 0), types.ErrNotInjectable)
}

func TestInjectorHoldAfterResultStartsIdle(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, false)
	var mu sync.Mutex
	var events []bool
	inj.SetIdleNotify(func(idle bool, _ uint64) { mu.Lock(); events = append(events, idle); mu.Unlock() })
	require.NoError(t, inj.Send("a"))
	inj.OnResult() // not held yet: this closes stdin
	closesOf(t, inj, w)
	assert.ErrorIs(t, inj.SetHold(true, 0), types.ErrNotInjectable)

	w2 := &fakeWC{}
	inj2 := NewInjector(w2, lineFmt, false)
	inj2.SetIdleNotify(func(idle bool, _ uint64) { mu.Lock(); events = append(events, idle); mu.Unlock() })
	require.NoError(t, inj2.Send("a"))
	require.NoError(t, inj2.SetHold(true, 0))
	inj2.OnResult()
	require.NoError(t, inj2.Send("b"))
	inj2.OnResult()
	require.NoError(t, inj2.SetHold(false, 0))
	closesOf(t, inj2, w2)
	assert.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(events) == 3 }, 2*time.Second, 5*time.Millisecond)
	mu.Lock()
	assert.Equal(t, []bool{true, false, true}, events, "idle, active (message sent), idle; no event for the close")
	mu.Unlock()
}

func TestInjectorReleaseMidTurnClosesAtResult(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, false)
	require.NoError(t, inj.Send("a"))
	require.NoError(t, inj.SetHold(true, 0))
	require.NoError(t, inj.SetHold(false, 0))
	notClosed(t, inj)
	inj.OnResult()
	assert.Equal(t, 1, closesOf(t, inj, w))
}

func TestInjectorIdleCapClosesHeldStdin(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, false)
	require.NoError(t, inj.Send("a"))
	require.NoError(t, inj.SetHold(true, 50*time.Millisecond))
	inj.OnResult()
	assert.Equal(t, 1, closesOf(t, inj, w), "an idle hold must not keep the process forever")
}

func TestInjectorIdleCapRestartsOnActivity(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, false)
	require.NoError(t, inj.Send("a"))
	require.NoError(t, inj.SetHold(true, 300*time.Millisecond))
	inj.OnResult()
	time.Sleep(150 * time.Millisecond)
	require.NoError(t, inj.Send("b")) // a turn is running: the cap does not apply
	time.Sleep(300 * time.Millisecond)
	notClosed(t, inj)
	inj.OnResult() // idle again: the cap starts over
	assert.Equal(t, 1, closesOf(t, inj, w))
}

func TestInjectorBackgroundTasksEndIdle(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, false)
	require.NoError(t, inj.Send("a"))
	require.NoError(t, inj.SetHold(true, 80*time.Millisecond))
	inj.OnResult()
	inj.OnBackgroundTasks(1) // woke up for background work: not idle
	notClosed(t, inj)
	inj.OnBackgroundTasks(0)
	inj.OnResult()
	assert.Equal(t, 1, closesOf(t, inj, w))
}

// Background tasks that end after the last result re-settle a hold, so the
// idle cap is armed even if no later result comes.
func TestInjectorBackgroundTasksEndWithoutResultSettles(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, false)
	require.NoError(t, inj.Send("a"))
	require.NoError(t, inj.SetHold(true, 80*time.Millisecond))
	inj.OnBackgroundTasks(1)
	inj.OnResult() // interim result while tasks run: not settled
	notClosed(t, inj)
	inj.OnBackgroundTasks(0) // no result follows
	assert.Equal(t, 1, closesOf(t, inj, w), "the idle cap closes the settled hold")
}

// The idle notifications carry a generation that grows with every change, and
// a hold that finds the injector idle already repeats the state (same gen).
func TestInjectorIdleNotifyGenerations(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, false)
	type ev struct {
		idle bool
		gen  uint64
	}
	var mu sync.Mutex
	var events []ev
	inj.SetIdleNotify(func(idle bool, gen uint64) { mu.Lock(); events = append(events, ev{idle, gen}); mu.Unlock() })
	get := func(n int) []ev {
		t.Helper()
		require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(events) >= n }, 2*time.Second, 5*time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return append([]ev(nil), events...)
	}
	require.NoError(t, inj.Send("a"))
	require.NoError(t, inj.SetHold(true, 0))
	inj.OnResult()
	require.NoError(t, inj.Send("b"))
	inj.OnResult()
	evs := get(3)
	assert.Equal(t, []bool{true, false, true}, []bool{evs[0].idle, evs[1].idle, evs[2].idle})
	assert.Less(t, evs[0].gen, evs[1].gen)
	assert.Less(t, evs[1].gen, evs[2].gen)

	require.NoError(t, inj.SetHold(true, 0)) // already idle: tell again
	evs = get(4)
	assert.Equal(t, evs[2], evs[3])
}

// SendGen returns the generation of the turn_active report its message
// causes, so reports from before it can be told stale.
func TestSendGenReturnsGenOfActiveReport(t *testing.T) {
	w := &fakeWC{}
	inj := NewInjector(w, lineFmt, false)
	var mu sync.Mutex
	gens := map[bool][]uint64{}
	inj.SetIdleNotify(func(idle bool, gen uint64) { mu.Lock(); gens[idle] = append(gens[idle], gen); mu.Unlock() })
	require.NoError(t, inj.Send("a"))
	require.NoError(t, inj.SetHold(true, 0))
	inj.OnResult() // idle
	gen, err := inj.SendGen("b")
	require.NoError(t, err)
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(gens[false]) == 1 }, 2*time.Second, 5*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, gens[false][0], gen)
	assert.Greater(t, gen, gens[true][0])
}
