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
