package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// fakeRunners is a RunnerRegistry with controllable online state.
type fakeRunners struct {
	mu     sync.Mutex
	online map[string]bool
	// offlineFor, when > 0, makes the next that many Nodes calls report every
	// runner offline (then the real state), to interleave a reconnect.
	offlineFor int
	// reconnectAfterForced makes every runner reconnect (new generation)
	// right after the last forced-offline observation.
	reconnectAfterForced bool
	gen                  map[string]uint64
}

func (f *fakeRunners) Nodes() []runnerhub.NodeInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	forced := f.offlineFor > 0
	var out []runnerhub.NodeInfo
	for id, on := range f.online {
		gen := f.gen[id]
		if gen == 0 {
			gen = 1
		}
		out = append(out, runnerhub.NodeInfo{NodeID: id, Online: on && !forced, OnlineGen: gen})
	}
	if forced {
		f.offlineFor--
		if f.offlineFor == 0 && f.reconnectAfterForced {
			for id := range f.online {
				f.bumpLocked(id)
			}
		}
	}
	return out
}

func (f *fakeRunners) bumpLocked(id string) {
	if f.gen == nil {
		f.gen = map[string]uint64{}
	}
	if f.gen[id] == 0 {
		f.gen[id] = 1
	}
	f.gen[id]++
}

// set changes the online state; going online is a reconnect (new generation).
func (f *fakeRunners) set(id string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if on && !f.online[id] {
		f.bumpLocked(id)
	}
	f.online[id] = on
}

// scopedSource is an agent source that also lists per-runner agents.
type scopedSource struct {
	global []*agentspec.Agent
	own    map[string][]*agentspec.Agent
}

func (s *scopedSource) LoadAll() ([]*agentspec.Agent, error) { return s.global, nil }
func (s *scopedSource) AgentsFor(node string) ([]*agentspec.Agent, error) {
	return append(append([]*agentspec.Agent(nil), s.global...), s.own[node]...), nil
}

type bindingFixture struct {
	server  *Server
	repo    *dbmodels.SessionRepository
	runners *fakeRunners
	mu      sync.Mutex
	ran     []string // prompts executed
}

func (f *bindingFixture) prompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ran...)
}

func setupBindingFixture(t *testing.T) *bindingFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)
	hub := NewSessionEventHubWithCapacity(20)
	t.Cleanup(hub.Close)

	global := &agentspec.Agent{Config: agentspec.AgentConfig{ID: "global-agent", Name: "Global", Type: "agent"}}
	own := &agentspec.Agent{Config: agentspec.AgentConfig{ID: "own-agent", Name: "Own", Type: "agent"}, NodeID: "r1"}
	src := &scopedSource{global: []*agentspec.Agent{global}, own: map[string][]*agentspec.Agent{"r1": {own}}}

	f := &bindingFixture{repo: repo, runners: &fakeRunners{online: map[string]bool{"r1": true, "r2": true}}}
	f.server = &Server{
		conf:        &config.Config{},
		repo:        repo,
		eventHub:    hub,
		agents:      []*agentspec.Agent{global},
		agentSource: src,
		runners:     f.runners,
	}
	f.server.runSingleAgentFn = func(_ context.Context, _ *agentspec.Agent, _ string, req TriggerMessageRequest) (string, string, error) {
		f.mu.Lock()
		f.ran = append(f.ran, req.Prompt)
		f.mu.Unlock()
		return "completed", "ok", nil
	}
	f.server.mux = f.server.buildMuxLocked()
	return f
}

func (f *bindingFixture) post(t *testing.T, agentID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/"+agentID+"/message", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	f.server.ServeHTTP(rr, req)
	return rr
}

func TestRunnerBinding_RequiredOnNewChat(t *testing.T) {
	f := setupBindingFixture(t)

	rr := f.post(t, "global-agent", map[string]any{"prompt": "hi", "chatId": "chat-new"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "runnerId is required")

	rr = f.post(t, "global-agent", map[string]any{"prompt": "hi", "chatId": "chat-new", "runnerId": "nope"})
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "unknown runner")

	sess, err := f.repo.GetSession("chat-new")
	require.NoError(t, err)
	assert.Nil(t, sess, "a rejected request must not create or bind the chat")

	rr = f.post(t, "global-agent", map[string]any{"prompt": "hi", "chatId": "chat-new", "runnerId": "r1"})
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
	sess, err = f.repo.GetSession("chat-new")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "r1", sess.RunnerNodeID)
	require.Eventually(t, func() bool { return len(f.prompts()) == 1 }, 5*time.Second, 10*time.Millisecond)
}

func TestRunnerBinding_ImmutableAfterFirstRun(t *testing.T) {
	f := setupBindingFixture(t)
	require.Equal(t, http.StatusAccepted, f.post(t, "global-agent", map[string]any{"prompt": "one", "chatId": "chat-imm", "runnerId": "r1"}).Code)
	require.Eventually(t, func() bool { return len(f.prompts()) == 1 }, 5*time.Second, 10*time.Millisecond)
	// Wait for the guard release before the next message.
	require.Eventually(t, func() bool { _, busy := f.server.activeExecutions.Load("chat-imm"); return !busy }, 5*time.Second, 10*time.Millisecond)

	rr := f.post(t, "global-agent", map[string]any{"prompt": "two", "chatId": "chat-imm", "runnerId": "r2"})
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "bound to runner")

	// Omitting runnerId, or repeating the bound one, keeps working.
	assert.Equal(t, http.StatusAccepted, f.post(t, "global-agent", map[string]any{"prompt": "three", "chatId": "chat-imm"}).Code)
	require.Eventually(t, func() bool { return len(f.prompts()) == 2 }, 5*time.Second, 10*time.Millisecond)
	sess, err := f.repo.GetSession("chat-imm")
	require.NoError(t, err)
	assert.Equal(t, "r1", sess.RunnerNodeID)
}

func TestRunnerBinding_OfflineRunnerIs503(t *testing.T) {
	f := setupBindingFixture(t)
	f.runners.set("r1", false)

	rr := f.post(t, "global-agent", map[string]any{"prompt": "hi", "chatId": "chat-off", "runnerId": "r1"})
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Contains(t, rr.Body.String(), "offline")

	rr = f.post(t, "global-agent", map[string]any{"prompt": "hi", "chatId": "chat-off", "wait": true})
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Empty(t, f.prompts())
	_, busy := f.server.activeExecutions.Load("chat-off")
	assert.False(t, busy, "the execution guard must be released")

	f.runners.set("r1", true)
	assert.Equal(t, http.StatusAccepted, f.post(t, "global-agent", map[string]any{"prompt": "hi", "chatId": "chat-off"}).Code)
}

func TestRunnerBinding_RunnerOwnedAgentVisibleOnlyOnItsRunner(t *testing.T) {
	f := setupBindingFixture(t)

	rr := f.post(t, "own-agent", map[string]any{"prompt": "hi", "chatId": "chat-own", "runnerId": "r2"})
	assert.Equal(t, http.StatusNotFound, rr.Code)

	rr = f.post(t, "own-agent", map[string]any{"prompt": "hi", "chatId": "chat-own2", "runnerId": "r1"})
	assert.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
}

func TestHandleAgents_RunnerQuery(t *testing.T) {
	f := setupBindingFixture(t)
	ids := func(url string) []string {
		rr := httptest.NewRecorder()
		f.server.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, url, nil))
		require.Equal(t, http.StatusOK, rr.Code)
		var list []AgentInfo
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
		var out []string
		for _, a := range list {
			out = append(out, a.ID)
		}
		return out
	}
	assert.Equal(t, []string{"global-agent"}, ids("/api/agents"))
	assert.Equal(t, []string{"global-agent", "own-agent"}, ids("/api/agents?runner=r1"))
	assert.Equal(t, []string{"global-agent"}, ids("/api/agents?runner=r2"))
}

func TestCreateSession_RunnerRequiredAndBound(t *testing.T) {
	f := setupBindingFixture(t)
	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		f.server.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(body)))
		return rr
	}
	assert.Equal(t, http.StatusBadRequest, post(`{"currentAgent":"global-agent"}`).Code)

	rr := post(`{"currentAgent":"global-agent","runnerId":"r1"}`)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var created ChatSession
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Equal(t, "r1", created.RunnerID)
	sess, err := f.repo.GetSession(created.ChatID)
	require.NoError(t, err)
	assert.Equal(t, "r1", sess.RunnerNodeID)
}

func TestQueuedMessagesWaitForRunnerAndResumeWhenItReturns(t *testing.T) {
	f := setupBindingFixture(t)
	chatID := "chat-queued"
	require.NoError(t, f.repo.UpdateAgentSession(chatID, "global-agent", "", "", nil))
	_, err := f.repo.BindRunner(chatID, "r1")
	require.NoError(t, err)
	f.runners.set("r1", false)
	_, err = f.repo.EnqueueMessage(chatID, "queued while offline", "")
	require.NoError(t, err)

	// A consumer started while the runner is offline leaves the queue alone.
	f.server.recoverOrphanQueuedSessions()
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, f.prompts())
	q, err := f.repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Len(t, q, 1)
	require.Eventually(t, func() bool { _, busy := f.server.activeExecutions.Load(chatID); return !busy }, 5*time.Second, 10*time.Millisecond)

	// The runner reconnecting drains it.
	f.runners.set("r1", true)
	f.server.RunnerOnline("r1")
	require.Eventually(t, func() bool { return len(f.prompts()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"queued while offline"}, f.prompts())
	require.Eventually(t, func() bool {
		q, _ := f.repo.GetQueuedMessages(chatID)
		return len(q) == 0
	}, 5*time.Second, 10*time.Millisecond)
}

func TestQueuedMessageRequeuedWhenRunnerDropsBeforeDispatch(t *testing.T) {
	f := setupBindingFixture(t)
	chatID := "chat-drop"
	require.NoError(t, f.repo.UpdateAgentSession(chatID, "global-agent", "", "", nil))
	_, err := f.repo.BindRunner(chatID, "r1")
	require.NoError(t, err)
	_, err = f.repo.EnqueueMessage(chatID, "must not be lost", "")
	require.NoError(t, err)

	var calls atomic.Int32
	f.server.runSingleAgentFn = func(context.Context, *agentspec.Agent, string, TriggerMessageRequest) (string, string, error) {
		calls.Add(1)
		return "failed", "", runnerhub.ErrRunnerOffline
	}
	f.server.RunnerOnline("r1")
	require.Eventually(t, func() bool { _, busy := f.server.activeExecutions.Load(chatID); return !busy }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	q, err := f.repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	require.Len(t, q, 1)
	assert.Equal(t, "must not be lost", q[0].Prompt)
	assert.Equal(t, int32(1), calls.Load(), "no reconnect happened, so no retry spin")

	// A real reconnect resumes it.
	f.server.runSingleAgentFn = func(_ context.Context, _ *agentspec.Agent, _ string, req TriggerMessageRequest) (string, string, error) {
		f.mu.Lock()
		f.ran = append(f.ran, req.Prompt)
		f.mu.Unlock()
		return "completed", "ok", nil
	}
	f.runners.set("r1", false)
	f.runners.set("r1", true)
	f.server.RunnerOnline("r1")
	require.Eventually(t, func() bool { return len(f.prompts()) == 1 }, 5*time.Second, 10*time.Millisecond)
}

func TestConsumerExitRaceWithReconnectDoesNotStrandMessages(t *testing.T) {
	f := setupBindingFixture(t)
	chatID := "chat-race"
	require.NoError(t, f.repo.UpdateAgentSession(chatID, "global-agent", "", "", nil))
	_, err := f.repo.BindRunner(chatID, "r1")
	require.NoError(t, err)
	_, err = f.repo.EnqueueMessage(chatID, "stranded?", "")
	require.NoError(t, err)

	// The runner reconnects while the consumer still holds the guard:
	// RunnerOnline cannot start a second consumer.
	f.server.activeExecutions.Store(chatID, struct{}{})
	f.server.RunnerOnline("r1")
	assert.Empty(t, f.prompts())

	// The consumer's offline check (first Nodes call) still sees the runner
	// offline; by the time it released the guard the runner is back.
	f.runners.mu.Lock()
	f.runners.offlineFor = 1
	f.runners.reconnectAfterForced = true
	f.runners.mu.Unlock()
	f.server.runQueueConsumerLoop(context.Background(), f.server.agents[0], chatID, "")

	require.Eventually(t, func() bool { return len(f.prompts()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"stranded?"}, f.prompts())
}

func TestStartupRecoveryBeforeRunnerConnectsThenResume(t *testing.T) {
	f := setupBindingFixture(t)
	chatID := "chat-startup"
	require.NoError(t, f.repo.UpdateAgentSession(chatID, "global-agent", "", "", nil))
	_, err := f.repo.BindRunner(chatID, "r1")
	require.NoError(t, err)
	_, err = f.repo.EnqueueMessage(chatID, "after restart", "")
	require.NoError(t, err)
	f.runners.set("r1", false)

	f.server.recoverOrphanQueuedSessions()
	require.Eventually(t, func() bool { _, busy := f.server.activeExecutions.Load(chatID); return !busy }, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, f.prompts())

	// The runner registered before the hub notification reached the server:
	// the explicit sweep picks it up.
	f.runners.set("r1", true)
	f.server.ResumeQueuesOfOnlineRunners()
	require.Eventually(t, func() bool { return len(f.prompts()) == 1 }, 5*time.Second, 10*time.Millisecond)
}

func TestRequeuedMessageKeepsIDAndHeadPosition(t *testing.T) {
	f := setupBindingFixture(t)
	chatID := "chat-head"
	require.NoError(t, f.repo.UpdateAgentSession(chatID, "global-agent", "", "", nil))
	_, err := f.repo.BindRunner(chatID, "r1")
	require.NoError(t, err)
	first, err := f.repo.EnqueueMessage(chatID, "first", "")
	require.NoError(t, err)
	_, err = f.repo.EnqueueMessage(chatID, "second", "")
	require.NoError(t, err)

	var metas []map[string]any
	var mu sync.Mutex
	f.server.runSingleAgentFn = func(_ context.Context, _ *agentspec.Agent, _ string, req TriggerMessageRequest) (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		metas = append(metas, req.Metadata)
		return "failed", "", runnerhub.ErrRunnerOffline
	}
	f.runners.set("r1", true)
	f.server.RunnerOnline("r1")
	require.Eventually(t, func() bool { _, busy := f.server.activeExecutions.Load(chatID); return !busy }, 5*time.Second, 10*time.Millisecond)

	q, err := f.repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	require.Len(t, q, 2)
	assert.Equal(t, first.ID, q[0].ID, "the original message is the head again")
	assert.Equal(t, first.CreatedAt.UnixNano(), q[0].CreatedAt.UnixNano())
	assert.Equal(t, "second", q[1].Prompt)
	mu.Lock()
	defer mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	require.Len(t, metas, 1, "an offline failure without a reconnect must not spin")
	assert.Equal(t, "msg-"+first.ID, metas[0]["message_id"], "a stable transcript id makes the retry idempotent")
}

func TestEnqueueEndpoint_UsesChatsRunnerAgentsAndBinding(t *testing.T) {
	f := setupBindingFixture(t)
	post := func(chatID string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		f.server.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/sessions/"+chatID+"/queue", bytes.NewBufferString(`{"prompt":"later"}`)))
		return rr
	}

	// A runner-owned current agent is found through the chat's runner.
	require.NoError(t, f.repo.UpdateAgentSession("chat-own-q", "own-agent", "", "", nil))
	_, err := f.repo.BindRunner("chat-own-q", "r1")
	require.NoError(t, err)
	f.runners.set("r1", false) // offline: accepted, stays queued
	assert.Equal(t, http.StatusCreated, post("chat-own-q").Code)
	time.Sleep(50 * time.Millisecond)
	q, err := f.repo.GetQueuedMessages("chat-own-q")
	require.NoError(t, err)
	assert.Len(t, q, 1)
	assert.Empty(t, f.prompts())

	// Same agent through another runner's view is not found.
	require.NoError(t, f.repo.UpdateAgentSession("chat-other", "own-agent", "", "", nil))
	_, err = f.repo.BindRunner("chat-other", "r2")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, post("chat-other").Code)

	// A chat without a runner is rejected once binding is enforced.
	require.NoError(t, f.repo.UpdateAgentSession("chat-unbound", "global-agent", "", "", nil))
	rr := post("chat-unbound")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "not bound to a runner")
}

func TestRecordStatusUpdate_ArtifactsWithoutContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)
	require.NoError(t, repo.UpdateAgentSession("chat-art", "a", "", "", nil))

	recordStatusUpdate(server, repo, "chat-art", AgentStatusUpdate{
		EntryType: "tool_call", ArtifactsKnown: true, Artifacts: []string{"/tmp/a.md", "/tmp/b.md"},
	}, &agentspec.AgentConfig{ID: "a"}, "")

	sess, err := repo.GetSession("chat-art")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"/tmp/a.md", "/tmp/b.md"}, []string(sess.Artifacts))
	assert.Empty(t, sess.Messages, "no message without content")
}

func TestQueuePreservedOnPreDispatchFailures(t *testing.T) {
	for name, cause := range map[string]error{
		"no runner configured": dispatch.NotDispatched(errors.New("no runner configured")),
		"chat not bound":       dispatch.NotDispatched(errors.New("chat c is not bound to a runner")),
	} {
		t.Run(name, func(t *testing.T) {
			f := setupBindingFixture(t)
			chatID := "chat-pre"
			require.NoError(t, f.repo.UpdateAgentSession(chatID, "global-agent", "", "", nil))
			_, err := f.repo.BindRunner(chatID, "r1")
			require.NoError(t, err)
			for _, p := range []string{"one", "two"} {
				_, err = f.repo.EnqueueMessage(chatID, p, "")
				require.NoError(t, err)
			}
			f.server.runSingleAgentFn = func(context.Context, *agentspec.Agent, string, TriggerMessageRequest) (string, string, error) {
				return "failed", "", cause
			}
			f.server.RunnerOnline("r1")
			require.Eventually(t, func() bool { _, busy := f.server.activeExecutions.Load(chatID); return !busy }, 5*time.Second, 10*time.Millisecond)

			q, err := f.repo.GetQueuedMessages(chatID)
			require.NoError(t, err)
			require.Len(t, q, 2, "the queue must survive a failure before execution")
			assert.Equal(t, "one", q[0].Prompt)
			sess, err := f.repo.GetSession(chatID)
			require.NoError(t, err)
			var notice bool
			for _, m := range sess.Messages {
				notice = notice || (m.Role == "error" && strings.Contains(m.Content, "not executed"))
			}
			assert.True(t, notice, "the user is told, without clearing anything")
		})
	}
}

func TestSingleAgentExecutor_NotDispatchedKeepsQueue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)
	server.dispatcher = dispatch.Unavailable{Err: errors.New("no runner configured")}
	exec := NewSingleAgentExecutor(dispatchTestAgent(), server.conf, repo, server, nil)
	_, err := exec.Execute(context.Background(), SingleAgentRunParams{ChatID: "chat-un", Prompt: "p", RunDir: "/work"})
	require.ErrorIs(t, err, dispatch.ErrNotDispatched)
	var runErr *agentRunError
	assert.NotErrorAs(t, err, &runErr)
}

func TestSubWorkflowResolutionUsesChatsRunnerAgents(t *testing.T) {
	f := setupBindingFixture(t)
	dir := t.TempDir()
	wf := filepath.Join(dir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wf, []byte("name: private-sub\nnodes:\n  - id: c\n    type: command\n    command: \"true\"\n"), 0o644))
	own := &agentspec.Agent{Config: agentspec.AgentConfig{ID: "private-sub", Name: "private-sub", Type: "workflow"}, NodeID: "r1", WorkflowPath: wf}
	f.server.agentSource.(*scopedSource).own["r1"] = append(f.server.agentSource.(*scopedSource).own["r1"], own)

	require.NoError(t, f.repo.UpdateAgentSession("chat-sub-r1", "global-agent", "", "", nil))
	_, err := f.repo.BindRunner("chat-sub-r1", "r1")
	require.NoError(t, err)
	require.NoError(t, f.repo.UpdateAgentSession("chat-sub-r2", "global-agent", "", "", nil))
	_, err = f.repo.BindRunner("chat-sub-r2", "r2")
	require.NoError(t, err)

	defn, err := f.server.resolveWorkflowDefinitionFor("chat-sub-r1", "private-sub")
	require.NoError(t, err)
	assert.Equal(t, "private-sub", defn.Name)

	_, err = f.server.resolveWorkflowDefinitionFor("chat-sub-r2", "private-sub")
	assert.Error(t, err, "another runner's workflow is not visible")
	_, err = f.server.resolveWorkflowDefinition("private-sub")
	assert.Error(t, err, "nor in the global view")
}

func TestInitialRunOfflineFailureRestartsQueueWhenRunnerBack(t *testing.T) {
	f := setupBindingFixture(t)
	chatID := "chat-init-race"
	require.NoError(t, f.repo.UpdateAgentSession(chatID, "global-agent", "", "", nil))
	_, err := f.repo.BindRunner(chatID, "r1")
	require.NoError(t, err)
	_, err = f.repo.EnqueueMessage(chatID, "queued", "")
	require.NoError(t, err)

	f.server.runSingleAgentFn = func(_ context.Context, _ *agentspec.Agent, _ string, req TriggerMessageRequest) (string, string, error) {
		if req.Prompt == "initial" {
			// The dispatch lost the runner, which reconnected (new
			// generation) by the time the guard is released.
			f.runners.set("r1", false)
			f.runners.set("r1", true)
			return "failed", "", runnerhub.ErrRunnerOffline
		}
		f.mu.Lock()
		f.ran = append(f.ran, req.Prompt)
		f.mu.Unlock()
		return "completed", "ok", nil
	}
	// A reconnect while the guard is held cannot start a consumer.
	f.server.activeExecutions.Store(chatID, struct{}{})
	f.server.RunnerOnline("r1")
	assert.Empty(t, f.prompts())

	f.server.runSingleAgentWithQueue(context.Background(), f.server.agents[0], chatID, TriggerMessageRequest{Prompt: "initial"})

	require.Eventually(t, func() bool { return len(f.prompts()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"queued"}, f.prompts())
}

func TestOfflineFailureWithoutReconnectDoesNotRestartOnRelease(t *testing.T) {
	f := setupBindingFixture(t)
	chatID := "chat-nospin"
	require.NoError(t, f.repo.UpdateAgentSession(chatID, "global-agent", "", "", nil))
	_, err := f.repo.BindRunner(chatID, "r1")
	require.NoError(t, err)
	_, err = f.repo.EnqueueMessage(chatID, "q", "")
	require.NoError(t, err)
	var calls atomic.Int32
	f.server.runSingleAgentFn = func(context.Context, *agentspec.Agent, string, TriggerMessageRequest) (string, string, error) {
		calls.Add(1)
		return "failed", "", runnerhub.ErrRunnerOffline
	}

	f.server.runSingleAgentWithQueue(context.Background(), f.server.agents[0], chatID, TriggerMessageRequest{Prompt: "initial"})
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, int32(1), calls.Load(), "the queued message must wait for a reconnect, not spin")
	q, err := f.repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Len(t, q, 1)
}

func TestLastReleaserDrainsQueueAfterConcurrentAcquirer(t *testing.T) {
	f := setupBindingFixture(t)
	chatID := "chat-last"
	require.NoError(t, f.repo.UpdateAgentSession(chatID, "global-agent", "", "", nil))
	_, err := f.repo.BindRunner(chatID, "r1")
	require.NoError(t, err)
	_, err = f.repo.EnqueueMessage(chatID, "waiting", "")
	require.NoError(t, err)

	// The consumer gives up on an offline runner; before its release is
	// followed by anything else, a sync request grabs the guard.
	f.runners.set("r1", false)
	f.server.activeExecutions.Store(chatID, struct{}{})
	f.server.runQueueConsumerLoop(context.Background(), f.server.agents[0], chatID, "")
	f.server.activeExecutions.Store(chatID, struct{}{}) // the concurrent acquirer
	f.runners.set("r1", true)                           // reconnect; RunnerOnline loses to the guard
	f.server.RunnerOnline("r1")
	assert.Empty(t, f.prompts())

	// Whoever releases last drains.
	f.server.releaseExecution(chatID)
	require.Eventually(t, func() bool { return len(f.prompts()) == 1 }, 5*time.Second, 10*time.Millisecond)
}

func TestHardParkedQueueIsNotAutoRestartedUntilNewActivity(t *testing.T) {
	f := setupBindingFixture(t)
	chatID := "chat-hard"
	require.NoError(t, f.repo.UpdateAgentSession(chatID, "global-agent", "", "", nil))
	_, err := f.repo.BindRunner(chatID, "r1")
	require.NoError(t, err)
	_, err = f.repo.EnqueueMessage(chatID, "q1", "")
	require.NoError(t, err)
	var calls atomic.Int32
	f.server.runSingleAgentFn = func(context.Context, *agentspec.Agent, string, TriggerMessageRequest) (string, string, error) {
		calls.Add(1)
		return "failed", "", dispatch.NotDispatched(errors.New("chat not bound"))
	}
	f.server.RunnerOnline("r1")
	require.Eventually(t, func() bool { _, busy := f.server.activeExecutions.Load(chatID); return !busy }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, int32(1), calls.Load(), "a non-retryable failure parks the queue")

	// A new enqueue lifts the park.
	rr := httptest.NewRecorder()
	f.server.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/sessions/"+chatID+"/queue", bytes.NewBufferString(`{"prompt":"q2"}`)))
	require.Equal(t, http.StatusCreated, rr.Code)
	require.Eventually(t, func() bool { return calls.Load() >= 2 }, 5*time.Second, 10*time.Millisecond)
}
