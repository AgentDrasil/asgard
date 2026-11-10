package api

import (
	"context"
	"encoding/json"
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
	"github.com/AgentDrasil/asgard/backend/lib/auth"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

type fakeInjector struct {
	calls atomic.Int32
	mu    sync.Mutex
	got   []string
	fn    func(prompt string) (bool, error)
}

func (f *fakeInjector) Inject(_ context.Context, _, _, prompt string) (bool, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.got = append(f.got, prompt)
	f.mu.Unlock()
	return f.fn(prompt)
}

// sendNowFixture is a bound chat on r1 with agent global-agent, an active run
// registered for it and an injector.
type sendNowFixture struct {
	*bindingFixture
	chatID   string
	injector *fakeInjector
	runs     *dispatch.ActiveRuns
}

func setupSendNow(t *testing.T, inject func(string) (bool, error)) *sendNowFixture {
	t.Helper()
	f := setupBindingFixture(t)
	s := &sendNowFixture{bindingFixture: f, chatID: "chat-sendnow", injector: &fakeInjector{fn: inject}, runs: dispatch.NewActiveRuns()}
	require.NoError(t, f.repo.UpdateAgentSession(s.chatID, "global-agent", "", "", nil))
	_, err := f.repo.BindRunner(s.chatID, "r1")
	require.NoError(t, err)
	f.server.activeRuns = s.runs
	f.server.injector = s.injector
	return s
}

func (s *sendNowFixture) startRun() { s.runs.Register(s.chatID, "global-agent", "r1", "run-1") }

func (s *sendNowFixture) sendNow(msgID string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	s.server.ServeHTTP(rr, asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/"+s.chatID+"/queue/"+msgID+"/send-now", nil)))
	return rr
}

func (s *sendNowFixture) enqueue(t *testing.T, prompts ...string) []*dbmodels.QueuedMessage {
	t.Helper()
	var out []*dbmodels.QueuedMessage
	for _, p := range prompts {
		m, err := s.repo.EnqueueMessage(s.chatID, p, "")
		require.NoError(t, err)
		out = append(out, m)
	}
	return out
}

func (s *sendNowFixture) queue(t *testing.T) []dbmodels.QueuedMessage {
	t.Helper()
	q, err := s.repo.GetQueuedMessages(s.chatID)
	require.NoError(t, err)
	return q
}

func decode(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &m), rr.Body.String())
	return m
}

func TestSendNow_Injected(t *testing.T) {
	s := setupSendNow(t, func(string) (bool, error) { return true, nil })
	s.startRun()
	msgs := s.enqueue(t, "first", "second")
	// An idle consumer would run "second" at once; it must run only that.
	sub, _, cancel := s.server.eventHub.Subscribe(s.chatID, 0)
	defer cancel()

	rr := s.sendNow(msgs[0].ID)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	body := decode(t, rr)
	assert.Equal(t, true, body["injected"])
	assert.Len(t, body["queue"], 1)
	assert.Equal(t, []string{"first"}, s.injector.got)

	q := s.queue(t)
	require.Len(t, q, 1)
	assert.Equal(t, "second", q[0].Prompt)

	sess, err := s.repo.GetSession(s.chatID)
	require.NoError(t, err)
	require.Len(t, sess.Messages, 1)
	assert.Equal(t, "user", sess.Messages[0].Role)
	assert.Equal(t, "first", sess.Messages[0].Content)
	assert.True(t, strings.HasPrefix(sess.Messages[0].ID, "msg-"))

	var sawMessage, sawQueue bool
	for len(sub) > 0 {
		ev := <-sub
		switch ev.Type {
		case "message":
			sawMessage = ev.Message != nil && ev.Message.Content == "first" && ev.Message.Role == "user"
		case EventTypeQueue:
			sawQueue = true
		}
	}
	assert.True(t, sawMessage, "message event published")
	assert.True(t, sawQueue, "queue event published")
	// Start a consumer: it may run what is still queued, never the injected message.
	s.server.startQueueConsumerIfIdle(s.chatID, s.server.agentSource.(*scopedSource).global[0], "")
	require.Eventually(t, func() bool { return len(s.prompts()) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, []string{"second"}, s.prompts(), "the consumer must not run an injected message")
}

func TestSendNow_NotInjectedRestoresPositionAndRestartsConsumer(t *testing.T) {
	s := setupSendNow(t, func(string) (bool, error) { return false, nil })
	s.startRun()
	msgs := s.enqueue(t, "first", "second", "third")

	rr := s.sendNow(msgs[1].ID)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	body := decode(t, rr)
	assert.Equal(t, false, body["injected"])
	assert.Equal(t, "not_supported", body["reason"])

	// The consumer started by the restore drains the queue in the original
	// order (the run registry is separate from the execution guard).
	require.Eventually(t, func() bool { return len(s.prompts()) == 3 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"first", "second", "third"}, s.prompts())
}

func TestSendNow_NotInjectedKeepsIDAndOrderWhileBusy(t *testing.T) {
	s := setupSendNow(t, func(string) (bool, error) { return false, nil })
	s.startRun()
	s.server.activeExecutions.Store(s.chatID, struct{}{}) // a run holds the guard
	msgs := s.enqueue(t, "first", "second")

	rr := s.sendNow(msgs[0].ID)
	require.Equal(t, http.StatusOK, rr.Code)
	q := s.queue(t)
	require.Len(t, q, 2)
	assert.Equal(t, msgs[0].ID, q[0].ID)
	assert.Equal(t, msgs[0].CreatedAt.UnixNano(), q[0].CreatedAt.UnixNano())
	assert.Equal(t, msgs[1].ID, q[1].ID)
}

func TestSendNow_NoActiveRun(t *testing.T) {
	s := setupSendNow(t, func(string) (bool, error) { return true, nil })
	s.server.activeExecutions.Store(s.chatID, struct{}{})
	msgs := s.enqueue(t, "first")

	rr := s.sendNow(msgs[0].ID)
	require.Equal(t, http.StatusOK, rr.Code)
	body := decode(t, rr)
	assert.Equal(t, false, body["injected"])
	assert.Equal(t, "not_running", body["reason"])
	assert.Zero(t, s.injector.calls.Load())
	assert.Len(t, s.queue(t), 1)

	// A run of a call-peer agent in the same chat is not the session's run.
	s.runs.Register(s.chatID, "peer-agent", "r1", "run-peer")
	assert.Equal(t, "not_running", decode(t, s.sendNow(msgs[0].ID))["reason"])
	assert.Zero(t, s.injector.calls.Load())
}

func TestSendNow_WorkflowAgentRejected(t *testing.T) {
	s := setupSendNow(t, func(string) (bool, error) { return true, nil })
	wf := "chat-wf"
	src := s.server.agentSource.(*scopedSource)
	src.global = append(src.global, &agentspec.Agent{Config: agentspec.AgentConfig{ID: "wf-agent", Name: "WF", Type: "workflow"}})
	require.NoError(t, s.repo.UpdateAgentSession(wf, "wf-agent", "", "", nil))
	_, err := s.repo.BindRunner(wf, "r1")
	require.NoError(t, err)
	m, err := s.repo.EnqueueMessage(wf, "x", "")
	require.NoError(t, err)
	s.runs.Register(wf, "wf-agent", "r1", "run-wf")

	rr := httptest.NewRecorder()
	s.server.ServeHTTP(rr, asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/"+wf+"/queue/"+m.ID+"/send-now", nil)))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Zero(t, s.injector.calls.Load())
	q, _ := s.repo.GetQueuedMessages(wf)
	assert.Len(t, q, 1)
}

func TestSendNow_Unauthorized(t *testing.T) {
	s := setupSendNow(t, func(string) (bool, error) { return true, nil })
	s.startRun()
	msgs := s.enqueue(t, "first")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+s.chatID+"/queue/"+msgs[0].ID+"/send-now", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{Subject: "stranger"}))
	s.server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)

	rr = httptest.NewRecorder()
	s.server.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/sessions/unknown-chat/queue/x/send-now", nil))
	assert.Contains(t, []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden}, rr.Code)

	assert.Zero(t, s.injector.calls.Load())
	assert.Len(t, s.queue(t), 1)
}

func TestSendNow_ConcurrentClaimExactlyOneWins(t *testing.T) {
	s := setupSendNow(t, func(string) (bool, error) { time.Sleep(30 * time.Millisecond); return true, nil })
	s.startRun()
	msgs := s.enqueue(t, "only")

	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = s.sendNow(msgs[0].ID).Code
		}()
	}
	wg.Wait()
	ok, gone := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusNotFound:
			gone++
		}
	}
	assert.Equal(t, 1, ok)
	assert.Equal(t, n-1, gone)
	assert.Equal(t, int32(1), s.injector.calls.Load(), "delivered at most once")
	assert.Empty(t, s.queue(t))
}

func TestSendNow_AlreadyConsumedByQueueConsumer(t *testing.T) {
	s := setupSendNow(t, func(string) (bool, error) { return true, nil })
	s.startRun()
	msgs := s.enqueue(t, "first")
	popped, err := s.repo.PopNextQueuedMessage(s.chatID)
	require.NoError(t, err)
	require.Equal(t, msgs[0].ID, popped.ID)

	rr := s.sendNow(msgs[0].ID)
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Zero(t, s.injector.calls.Load())
}

func TestSendNow_InjectErrorRestores(t *testing.T) {
	cases := map[string]struct {
		err    error
		reason string
	}{
		"failed":      {errors.New("boom"), "failed"},
		"unknown run": {runnerhub.ErrUnknownRun, "not_running"},
		"offline":     {runnerhub.ErrRunnerOffline, "not_running"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := setupSendNow(t, func(string) (bool, error) { return false, tc.err })
			s.startRun()
			s.server.activeExecutions.Store(s.chatID, struct{}{})
			msgs := s.enqueue(t, "first", "second")

			rr := s.sendNow(msgs[0].ID)
			require.Equal(t, http.StatusOK, rr.Code)
			body := decode(t, rr)
			assert.Equal(t, false, body["injected"])
			assert.Equal(t, tc.reason, body["reason"])
			q := s.queue(t)
			require.Len(t, q, 2)
			assert.Equal(t, msgs[0].ID, q[0].ID)
		})
	}
}

func TestSendNow_PanicDuringInjectStillRestores(t *testing.T) {
	s := setupSendNow(t, func(string) (bool, error) { panic("boom") })
	s.startRun()
	s.server.activeExecutions.Store(s.chatID, struct{}{})
	msgs := s.enqueue(t, "first")

	func() {
		defer func() { _ = recover() }()
		s.sendNow(msgs[0].ID)
	}()
	q := s.queue(t)
	require.Len(t, q, 1)
	assert.Equal(t, msgs[0].ID, q[0].ID)
}

func TestSendNow_RestoreSucceedsOnRetry(t *testing.T) {
	old := sendNowRestoreRetryDelay
	sendNowRestoreRetryDelay = time.Millisecond
	t.Cleanup(func() { sendNowRestoreRetryDelay = old })

	s := setupSendNow(t, func(string) (bool, error) { return false, nil })
	s.startRun()
	s.server.activeExecutions.Store(s.chatID, struct{}{})
	msgs := s.enqueue(t, "first")
	var attempts int
	s.server.requeueFn = func(m *dbmodels.QueuedMessage) error {
		attempts++
		if attempts < 3 {
			return errors.New("database is locked")
		}
		return s.repo.RequeueFront(m)
	}

	rr := s.sendNow(msgs[0].ID)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, 3, attempts)
	q := s.queue(t)
	require.Len(t, q, 1)
	assert.Equal(t, msgs[0].ID, q[0].ID)
}

func TestSendNow_RestoreFailsPermanently(t *testing.T) {
	old := sendNowRestoreRetryDelay
	sendNowRestoreRetryDelay = time.Millisecond
	t.Cleanup(func() { sendNowRestoreRetryDelay = old })

	s := setupSendNow(t, func(string) (bool, error) { return false, nil })
	s.startRun()
	s.server.activeExecutions.Store(s.chatID, struct{}{})
	msgs := s.enqueue(t, "do the thing")
	s.server.requeueFn = func(*dbmodels.QueuedMessage) error { return errors.New("database is locked") }
	sub, _, cancel := s.server.eventHub.Subscribe(s.chatID, 0)
	defer cancel()

	rr := s.sendNow(msgs[0].ID)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), "could not be restored")
	assert.Empty(t, s.queue(t))

	sess, err := s.repo.GetSession(s.chatID)
	require.NoError(t, err)
	require.Len(t, sess.Messages, 1)
	assert.Equal(t, "error", sess.Messages[0].Role)
	assert.Contains(t, sess.Messages[0].Content, "do the thing")

	var sawErr, sawQueue bool
	for len(sub) > 0 {
		ev := <-sub
		if ev.Type == "message" && ev.Message != nil && ev.Message.Role == "error" {
			sawErr = true
		}
		if ev.Type == EventTypeQueue {
			sawQueue = true
		}
	}
	assert.True(t, sawErr)
	assert.True(t, sawQueue)
}

// A chat that waits between turns for its subagents must not get a turn
// injected by send-now while a writable subagent holds the checkout.
func TestSendNow_IdleHeldChatWithWritableChildStaysQueued(t *testing.T) {
	s := setupSendNow(t, func(string) (bool, error) { return true, nil })
	s.startRun()
	srv := s.server
	srv.dirLocks = dirlock.NewManager(func(string) { srv.subagentSig.locksChanged(srv) })
	ctx := t.Context()
	relParent, err := srv.dirLocks.Acquire(ctx, lockReq(s.chatID))
	require.NoError(t, err)
	defer relParent(false)

	st := &holdState{runID: "run-1", agentID: "global-agent", holding: true, idle: true, reg: &waitReg{chat: s.chatID}}
	srv.holds.mu.Lock()
	srv.holds.m = map[string]*holdState{s.chatID: st}
	srv.holds.mu.Unlock()
	srv.holdWaitMark(s.chatID, st.reg)
	got := make(chan func(bool), 1)
	go func() {
		if rel, aerr := srv.dirLocks.Acquire(ctx, lockReq("child-w", s.chatID)); aerr == nil {
			got <- rel
		}
	}()
	select {
	case rel := <-got:
		defer rel(false)
	case <-time.After(5 * time.Second):
		t.Fatal("writable child not granted")
	}

	msgs := s.enqueue(t, "first")
	rr := s.sendNow(msgs[0].ID)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	body := decode(t, rr)
	assert.Equal(t, false, body["injected"])
	assert.Equal(t, sendNowHeldByChild, body["reason"])
	assert.Zero(t, s.injector.calls.Load())
	q := s.queue(t)
	require.Len(t, q, 1)
	assert.Equal(t, "first", q[0].Prompt)
	assert.True(t, srv.chatMarked(s.chatID), "mark unchanged")
	assert.True(t, srv.isIdleHeld(s.chatID))
}
