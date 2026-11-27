package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
)

type fakeCompass struct {
	mu        sync.Mutex
	classify  []sandboxsock.CompassClassifyRequest
	summarize []sandboxsock.CompassSummarizeRequest
	block     chan struct{} // when set, calls wait for it or ctx
}

func (f *fakeCompass) wait(ctx context.Context) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
		}
	}
}

func (f *fakeCompass) Classify(ctx context.Context, req sandboxsock.CompassClassifyRequest) sandboxsock.CompassClassifyResponse {
	f.mu.Lock()
	f.classify = append(f.classify, req)
	f.mu.Unlock()
	f.wait(ctx)
	return sandboxsock.CompassClassifyResponse{Enabled: true, Strategy: "summarize", Usage: sandboxsock.CompassUsage{Issued: true, Tokens: 7}}
}

func (f *fakeCompass) Summarize(ctx context.Context, req sandboxsock.CompassSummarizeRequest) sandboxsock.CompassSummarizeResponse {
	f.mu.Lock()
	f.summarize = append(f.summarize, req)
	f.mu.Unlock()
	f.wait(ctx)
	return sandboxsock.CompassSummarizeResponse{Enabled: true, Summary: "short", Usage: sandboxsock.CompassUsage{Issued: true, Tokens: 3}}
}

func TestCompassHappyPath(t *testing.T) {
	rig := newCallbackRig(t, context.Background(), &fakeCallbacks{}, nil)
	fc := &fakeCompass{}
	rig.srv.compass = fc

	code, body := rig.post(t, sandboxsock.PathCompassClassify, sandboxsock.CompassClassifyRequest{Command: "go test", ExitCode: 1, TotalBytes: 5000, OutputSample: "boom"})
	require.Equal(t, http.StatusOK, code)
	var cr sandboxsock.CompassClassifyResponse
	require.NoError(t, json.Unmarshal(body, &cr))
	assert.True(t, cr.Enabled)
	assert.Equal(t, "summarize", cr.Strategy)
	assert.Equal(t, int64(7), cr.Usage.Tokens)
	require.Len(t, fc.classify, 1)
	assert.Equal(t, "go test", fc.classify[0].Command)

	code, body = rig.post(t, sandboxsock.PathCompassSummarize, sandboxsock.CompassSummarizeRequest{Command: "go test", ExitCode: 1, Strategy: "summarize", Log: "log"})
	require.Equal(t, http.StatusOK, code)
	var sr sandboxsock.CompassSummarizeResponse
	require.NoError(t, json.Unmarshal(body, &sr))
	assert.Equal(t, "short", sr.Summary)
	require.Len(t, fc.summarize, 1)
	assert.Equal(t, "log", fc.summarize[0].Log)
}

func TestCompassDisabledWithoutBackend(t *testing.T) {
	rig := newCallbackRig(t, context.Background(), &fakeCallbacks{}, nil)
	code, body := rig.post(t, sandboxsock.PathCompassClassify, sandboxsock.CompassClassifyRequest{Command: "x"})
	require.Equal(t, http.StatusOK, code)
	var cr sandboxsock.CompassClassifyResponse
	require.NoError(t, json.Unmarshal(body, &cr))
	assert.False(t, cr.Enabled)
	code, body = rig.post(t, sandboxsock.PathCompassSummarize, sandboxsock.CompassSummarizeRequest{Log: "x"})
	require.Equal(t, http.StatusOK, code)
	var sr sandboxsock.CompassSummarizeResponse
	require.NoError(t, json.Unmarshal(body, &sr))
	assert.False(t, sr.Enabled)
}

func TestCompassOversizeRejected(t *testing.T) {
	rig := newCallbackRig(t, context.Background(), &fakeCallbacks{}, nil)
	fc := &fakeCompass{}
	rig.srv.compass = fc
	code, _ := rig.post(t, sandboxsock.PathCompassSummarize, sandboxsock.CompassSummarizeRequest{Log: strings.Repeat("x", maxCompassBody+1)})
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Empty(t, fc.summarize)
	code, _ = rig.post(t, sandboxsock.PathCompassClassify, map[string]any{"command": 5})
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestCompassTimeout(t *testing.T) {
	rig := newCallbackRig(t, context.Background(), &fakeCallbacks{}, nil)
	rig.srv.compass = &fakeCompass{block: make(chan struct{})}
	rig.srv.compassTimeout = 50 * time.Millisecond
	code, _ := rig.post(t, sandboxsock.PathCompassClassify, sandboxsock.CompassClassifyRequest{Command: "x"})
	assert.Equal(t, http.StatusGatewayTimeout, code)
}

func TestCompassOversizeLogNotSentUpstream(t *testing.T) {
	rig := newCallbackRig(t, context.Background(), &fakeCallbacks{}, nil)
	fc := &fakeCompass{}
	rig.srv.compass = fc
	code, body := rig.post(t, sandboxsock.PathCompassSummarize, sandboxsock.CompassSummarizeRequest{Strategy: "summarize", Log: strings.Repeat("x", maxCompassLog+1)})
	require.Equal(t, http.StatusOK, code)
	var sr sandboxsock.CompassSummarizeResponse
	require.NoError(t, json.Unmarshal(body, &sr))
	assert.True(t, sr.Enabled)
	assert.Empty(t, sr.Summary)
	assert.Empty(t, fc.summarize, "no model call for an oversize log")

	code, _ = rig.post(t, sandboxsock.PathCompassSummarize, sandboxsock.CompassSummarizeRequest{Strategy: "summarize", Log: strings.Repeat("x", maxCompassLog)})
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, fc.summarize, 1, "a log at the cap is summarized")
}

func TestCompassLimiterWaitsForSlot(t *testing.T) {
	l := newCompassLimiter(2, 1000, 1000)
	l.wait = 2 * time.Second
	r1, err := l.acquire(context.Background())
	require.NoError(t, err)
	r2, err := l.acquire(context.Background())
	require.NoError(t, err)

	got := make(chan error, 1)
	go func() {
		rel, err := l.acquire(context.Background())
		if err == nil {
			rel()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("acquired without a free slot: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	r1()
	select {
	case err := <-got:
		require.NoError(t, err, "the waiter gets the freed slot")
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not get the slot")
	}
	r2()
}

func TestCompassLimiterBusyAndCancel(t *testing.T) {
	l := newCompassLimiter(1, 1000, 1000)
	l.wait = 30 * time.Millisecond
	rel, err := l.acquire(context.Background())
	require.NoError(t, err)
	_, err = l.acquire(context.Background())
	assert.ErrorIs(t, err, errCompassBusy)

	l.wait = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = l.acquire(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	rel()
	// The failed attempts did not leak a slot.
	rel, err = l.acquire(context.Background())
	require.NoError(t, err)
	rel()
}

func TestCompassLimiterTokenBucket(t *testing.T) {
	l := newCompassLimiter(8, 1, 3) // 1/s, burst 3
	now := time.Now()
	l.now = func() time.Time { return now }
	for range 3 {
		rel, err := l.acquire(context.Background())
		require.NoError(t, err)
		rel()
	}
	_, err := l.acquire(context.Background())
	assert.ErrorIs(t, err, errCompassRate, "burst used up")
	_, err = l.acquire(context.Background())
	assert.ErrorIs(t, err, errCompassRate)

	now = now.Add(1500 * time.Millisecond) // one token refilled, no window boundary
	rel, err := l.acquire(context.Background())
	require.NoError(t, err)
	rel()
	_, err = l.acquire(context.Background())
	assert.ErrorIs(t, err, errCompassRate, "only one token per second")
}

func TestCompassGlobalLimiterSharedAcrossServers(t *testing.T) {
	global := newCompassLimiter(1, 1000, 1000)
	global.wait = 20 * time.Millisecond
	a := newStatusServer(context.Background(), baseStartRun(), testAgent("a", "h1", ""), t.TempDir(), newRecordingSink())
	b := newStatusServer(context.Background(), baseStartRun(), testAgent("a", "h1", ""), t.TempDir(), newRecordingSink())
	assert.Same(t, globalCompass, a.compassGlobal, "servers default to the runner-wide limiter")
	assert.Same(t, a.compassGlobal, b.compassGlobal)
	assert.NotSame(t, a.compassLimit, b.compassLimit, "per-run limiters are separate")
	a.compassGlobal, b.compassGlobal = global, global

	rel, err := acquireBoth(context.Background(), a.compassLimit, a.compassGlobal, 0)
	require.NoError(t, err)
	_, err = acquireBoth(context.Background(), b.compassLimit, b.compassGlobal, 0)
	assert.ErrorIs(t, err, errCompassBusy, "another run is held back by the shared cap")
	rel()
	rel, err = acquireBoth(context.Background(), b.compassLimit, b.compassGlobal, 0)
	require.NoError(t, err)
	rel()
	// The failed attempt did not leak b's per-run slot.
	assert.Len(t, b.compassLimit.slots, 0)
}

// The slot stays taken until the upstream call returns, even after the handler
// answered 504, so the in-flight cap is never overshot.
func TestCompassSlotHeldUntilUpstreamReturns(t *testing.T) {
	rig := newCallbackRig(t, context.Background(), &fakeCallbacks{}, nil)
	fc := &stuckCompass{release: make(chan struct{}), started: make(chan struct{}, 1)}
	rig.srv.compass = fc
	rig.srv.compassTimeout = 30 * time.Millisecond
	rig.srv.compassLimit = newCompassLimiter(1, 1000, 1000)
	rig.srv.compassLimit.wait = 30 * time.Millisecond

	code, _ := rig.post(t, sandboxsock.PathCompassClassify, sandboxsock.CompassClassifyRequest{Command: "x"})
	assert.Equal(t, http.StatusGatewayTimeout, code)
	<-fc.started
	code, _ = rig.post(t, sandboxsock.PathCompassClassify, sandboxsock.CompassClassifyRequest{Command: "y"})
	assert.Equal(t, http.StatusTooManyRequests, code, "slot still held by the abandoned upstream call")

	close(fc.release)
	require.Eventually(t, func() bool { return len(rig.srv.compassLimit.slots) == 0 }, 2*time.Second, 5*time.Millisecond)
	code, _ = rig.post(t, sandboxsock.PathCompassClassify, sandboxsock.CompassClassifyRequest{Command: "z"})
	assert.Equal(t, http.StatusOK, code)
}

// stuckCompass ignores its context until release is closed.
type stuckCompass struct {
	release chan struct{}
	started chan struct{}
}

func (f *stuckCompass) Classify(_ context.Context, _ sandboxsock.CompassClassifyRequest) sandboxsock.CompassClassifyResponse {
	select {
	case f.started <- struct{}{}:
	default:
	}
	<-f.release
	return sandboxsock.CompassClassifyResponse{Enabled: true, Strategy: "keep_raw"}
}

func (f *stuckCompass) Summarize(context.Context, sandboxsock.CompassSummarizeRequest) sandboxsock.CompassSummarizeResponse {
	return sandboxsock.CompassSummarizeResponse{}
}

func TestCompassRateLimitHTTP(t *testing.T) {
	rig := newCallbackRig(t, context.Background(), &fakeCallbacks{}, nil)
	rig.srv.compass = &fakeCompass{}
	rig.srv.compassLimit = newCompassLimiter(4, 0, 1)
	code, _ := rig.post(t, sandboxsock.PathCompassClassify, sandboxsock.CompassClassifyRequest{Command: "x"})
	assert.Equal(t, http.StatusOK, code)
	code, _ = rig.post(t, sandboxsock.PathCompassClassify, sandboxsock.CompassClassifyRequest{Command: "x"})
	assert.Equal(t, http.StatusTooManyRequests, code)
}

func TestCompassGlobalSaturationKeepsRunTokens(t *testing.T) {
	run := newCompassLimiter(8, 1, 3)
	global := newCompassLimiter(1, 1000, 1000)
	global.wait = 0
	now := time.Now()
	run.now = func() time.Time { return now }
	hold, err := acquireBoth(context.Background(), newCompassLimiter(1, 1000, 1000), global, 0)
	require.NoError(t, err)
	for range 20 {
		_, err = acquireBoth(context.Background(), run, global, 0)
		assert.ErrorIs(t, err, errCompassBusy)
	}
	hold()
	assert.Len(t, run.slots, 0)
	for range 3 {
		rel, err := acquireBoth(context.Background(), run, global, 0)
		require.NoError(t, err, "burst intact after global refusals")
		rel()
	}
}

func TestCompassRunTokenNotSpentWhileWaitingOnGlobal(t *testing.T) {
	run := newCompassLimiter(8, 1, 1)
	global := newCompassLimiter(1, 1000, 1000)
	global.wait = 2 * time.Second
	now := time.Now()
	run.now = func() time.Time { return now }
	hold, err := acquireBoth(context.Background(), newCompassLimiter(1, 1000, 1000), global, 0)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		rel, err := acquireBoth(context.Background(), run, global, 0)
		if err == nil {
			rel()
		}
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	assert.InDelta(t, 1, run.rate.TokensAt(now), 0.01, "no token spent while waiting")
	hold()
	require.NoError(t, <-done)
}

func TestCompassRateRefusedReturnsOtherToken(t *testing.T) {
	run := newCompassLimiter(8, 1, 2)
	global := newCompassLimiter(8, 1, 1)
	now := time.Now()
	run.now = func() time.Time { return now }
	global.now = run.now
	rel, err := acquireBoth(context.Background(), run, global, 0)
	require.NoError(t, err)
	rel()
	_, err = acquireBoth(context.Background(), run, global, 0)
	assert.ErrorIs(t, err, errCompassRate)
	assert.InDelta(t, 1, run.rate.TokensAt(now), 0.01, "run token handed back")
}

func TestCompassDeadlineBudget(t *testing.T) {
	post := func(rig *callbackRig, ms string) int {
		req, err := http.NewRequest(http.MethodPost, "http://asgard"+sandboxsock.PathCompassClassify, strings.NewReader(`{"command":"x"}`))
		require.NoError(t, err)
		req.Header.Set(sandboxsock.HeaderDeadlineMs, ms)
		resp, err := rig.client.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	rig := newCallbackRig(t, context.Background(), &fakeCallbacks{}, nil)
	fc := &fakeCompass{}
	rig.srv.compass = fc
	assert.Equal(t, http.StatusTooManyRequests, post(rig, "500"), "budget below a model call is refused")
	assert.Empty(t, fc.classify, "no upstream call")
	assert.Equal(t, http.StatusOK, post(rig, "3000"))
	assert.Len(t, fc.classify, 1)

	// A busy slot is waited for at most a quarter of the budget.
	rig.srv.compassLimit = newCompassLimiter(1, 1000, 1000)
	rig.srv.compassLimit.slots <- struct{}{}
	start := time.Now()
	assert.Equal(t, http.StatusTooManyRequests, post(rig, "1200"))
	assert.Less(t, time.Since(start), time.Second)
}

func testRefundWithClock(t *testing.T, runNow func() time.Time) {
	t.Helper()
	run := newCompassLimiter(8, 0.001, 2)
	global := newCompassLimiter(8, 0.001, 1)
	if runNow != nil {
		run.now = runNow
	}
	rel, err := acquireBoth(context.Background(), run, global, 0)
	require.NoError(t, err)
	rel()
	_, err = acquireBoth(context.Background(), run, global, 0)
	assert.ErrorIs(t, err, errCompassRate)
	assert.InDelta(t, 1, run.rate.TokensAt(run.now()), 0.05, "run token restored")
}

func TestCompassRefundWithAdvancingClock(t *testing.T) {
	now := time.Now()
	testRefundWithClock(t, func() time.Time {
		now = now.Add(time.Nanosecond)
		return now
	})
}

func TestCompassRefundWithRealClock(t *testing.T) {
	testRefundWithClock(t, nil)
}
