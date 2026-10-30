package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/kms"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/metrics"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

type testNodeKey struct{}

func withTestNode(ctx context.Context, nodeID string) context.Context {
	return context.WithValue(ctx, testNodeKey{}, nodeID)
}

// fakeRuns is a manually written run tracker.
type fakeRuns struct {
	mu   sync.Mutex
	runs map[string]runnerhub.RunInfo
}

func (f *fakeRuns) add(info runnerhub.RunInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runs == nil {
		f.runs = map[string]runnerhub.RunInfo{}
	}
	f.runs[info.RunID] = info
}

func (f *fakeRuns) remove(runID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.runs, runID)
}

func (f *fakeRuns) ActiveRun(runID, nodeID string) (runnerhub.RunInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.runs[runID]
	if !ok || info.NodeID != nodeID {
		return runnerhub.RunInfo{}, false
	}
	return info, true
}

type callbackRig struct {
	server *Server
	repo   *dbmodels.SessionRepository
	events *SessionEventHub
	runs   *fakeRuns
	svc    *callbackService
	store  *CompressionMetricsStore
	done   chan struct{}
	dir    string
}

const (
	rigNode = "runner-1"
	rigRun  = "run-1"
	rigChat = "chat-cb"
)

func newCallbackRig(t *testing.T, agents ...*agentspec.Agent) *callbackRig {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string { return filepath.Join(tempDir, chatID) })
	events := NewSessionEventHubWithCapacity(10)
	t.Cleanup(events.Close)
	store := NewCompressionMetricsStore(filepath.Join(tempDir, "metrics.json"))

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server := &Server{
		peerChains:         map[string][]string{},
		conf:               &config.Config{},
		repo:               repo,
		eventHub:           events,
		workflowEngine:     workflow.NewEngine(registry),
		agents:             agents,
		compressionMetrics: store,
		ctx:                ctx,
	}
	key := sha256.Sum256([]byte("callback-rig-cipher-key-32-bytes!"))
	c, _ := kms.NewAESGCMCipher(key[:])
	server.cipher = c
	server.userProxyKeyRepo = dbmodels.NewUserProxyKeyRepository(testDB, c)
	server.mux = server.buildMuxLocked()

	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "alpha"}))

	runs := &fakeRuns{}
	done := make(chan struct{})
	runs.add(runnerhub.RunInfo{RunID: rigRun, NodeID: rigNode, ChatID: rigChat, AgentID: "alpha", UserID: "user-42", Done: done})
	svc := newCallbackService(server, runs)
	svc.nodeID = func(ctx context.Context) (string, bool) {
		id, ok := ctx.Value(testNodeKey{}).(string)
		return id, ok
	}
	return &callbackRig{server: server, repo: repo, events: events, runs: runs, svc: svc, store: store, done: done, dir: tempDir}
}

func (r *callbackRig) messages(t *testing.T, chatID string) dbmodels.Messages {
	t.Helper()
	msgs, err := dbmodels.ReadMessages(filepath.Join(r.dir, chatID))
	require.NoError(t, err)
	return msgs
}

func (r *callbackRig) ctx() context.Context { return withTestNode(context.Background(), rigNode) }

func meta(runID string, seq uint64) *runnerpb.CallMeta {
	return &runnerpb.CallMeta{RunId: runID, CallSeq: seq}
}

func replyToAsk(t *testing.T, s *Server, chatID, messageID, text string) {
	t.Helper()
	body, err := json.Marshal(AskUserReplyRequest{ChatID: chatID, MessageID: messageID, ReplyText: text})
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/ask-user/reply", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rr.Code)
}

func waitAskMessage(t *testing.T, ch <-chan SessionEvent) *dbmodels.ChatMessage {
	t.Helper()
	select {
	case ev := <-ch:
		require.NotNil(t, ev.Message)
		return ev.Message
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the ask_user message")
		return nil
	}
}

func TestCallbackValidatesRunOwnership(t *testing.T) {
	rig := newCallbackRig(t)
	rig.runs.add(runnerhub.RunInfo{RunID: "run-other", NodeID: "runner-2", ChatID: "chat-other", Done: make(chan struct{})})

	tests := []struct {
		name string
		ctx  context.Context
		meta *runnerpb.CallMeta
		want codes.Code
	}{
		{"no runner identity", context.Background(), meta(rigRun, 1), codes.Unauthenticated},
		{"unknown run", rig.ctx(), meta("run-ghost", 1), codes.FailedPrecondition},
		{"run of another runner", rig.ctx(), meta("run-other", 1), codes.FailedPrecondition},
		{"missing call_seq", rig.ctx(), meta(rigRun, 0), codes.InvalidArgument},
		{"missing meta", rig.ctx(), nil, codes.InvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rig.svc.FindPeer(tt.ctx, &runnerpb.FindPeerRequest{Meta: tt.meta})
			assert.Equal(t, tt.want, status.Code(err))
			_, err = rig.svc.RecordMetrics(tt.ctx, &runnerpb.RecordMetricsRequest{Meta: tt.meta})
			assert.Equal(t, tt.want, status.Code(err))
		})
	}

	// A run that finished is no longer accepted.
	rig.runs.remove(rigRun)
	_, err := rig.svc.FindPeer(rig.ctx(), &runnerpb.FindPeerRequest{Meta: meta(rigRun, 1)})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestCallbackAskUserUsesRunIdentityAndResolvesOnReply(t *testing.T) {
	rig := newCallbackRig(t)
	sub, _, cancel := rig.events.Subscribe(rigChat, 0)
	t.Cleanup(cancel)

	type result struct {
		resp *runnerpb.AskUserResponse
		err  error
	}
	got := make(chan result, 1)
	go func() {
		// The request names another chat in its (informational) chat_id; the
		// run's chat wins.
		resp, err := rig.svc.AskUser(rig.ctx(), &runnerpb.AskUserRequest{
			Meta: meta(rigRun, 1), ChatId: "evil-chat", AgentName: "Alpha", Question: "ship it?", MessageId: "ask-1", Model: "m1",
		})
		got <- result{resp, err}
	}()

	msg := waitAskMessage(t, sub)
	assert.Equal(t, "ask-1", msg.ID)
	assert.Equal(t, "ask_user", msg.Role)
	assert.Equal(t, "ship it?", msg.Content)
	assert.Equal(t, "Alpha", msg.AgentName)
	assert.Equal(t, "m1", msg.Model)

	replyToAsk(t, rig.server, rigChat, "ask-1", "yes")
	select {
	case r := <-got:
		require.NoError(t, r.err)
		assert.Equal(t, "yes", r.resp.GetReply())
		assert.Empty(t, r.resp.GetError())
	case <-time.After(5 * time.Second):
		t.Fatal("AskUser did not return after the reply")
	}

	assert.Empty(t, rig.messages(t, "evil-chat"))
	assert.Len(t, rig.messages(t, rigChat), 1)
}

func TestCallbackAskUserDerivesMessageID(t *testing.T) {
	rig := newCallbackRig(t)
	sub, _, cancel := rig.events.Subscribe(rigChat, 0)
	t.Cleanup(cancel)

	go func() {
		_, _ = rig.svc.AskUser(rig.ctx(), &runnerpb.AskUserRequest{Meta: meta(rigRun, 7), Question: "q"})
	}()
	msg := waitAskMessage(t, sub)
	assert.Equal(t, "ask-run-1-7", msg.ID)
	replyToAsk(t, rig.server, rigChat, msg.ID, "ok")
}

func TestCallbackRetryAttachesToInFlightCallAndCachesResult(t *testing.T) {
	rig := newCallbackRig(t)
	sub, _, cancel := rig.events.Subscribe(rigChat, 0)
	t.Cleanup(cancel)

	req := &runnerpb.AskUserRequest{Meta: meta(rigRun, 1), Question: "q", MessageId: "ask-dedup"}
	type result struct {
		resp *runnerpb.AskUserResponse
		err  error
	}
	first := make(chan result, 1)
	// The first attempt's connection drops (its context ends) while the call
	// is blocked on the user.
	firstCtx, dropConn := context.WithCancel(rig.ctx())
	go func() {
		resp, err := rig.svc.AskUser(firstCtx, req)
		first <- result{resp, err}
	}()
	waitAskMessage(t, sub)
	dropConn()
	r := <-first
	assert.Equal(t, codes.Canceled, status.Code(r.err))

	// The retry (same run_id and call_seq) attaches to the in-flight call.
	second := make(chan result, 1)
	go func() {
		resp, err := rig.svc.AskUser(rig.ctx(), req)
		second <- result{resp, err}
	}()
	select {
	case <-second:
		t.Fatal("the retry returned before the user replied")
	case <-time.After(50 * time.Millisecond):
	}
	replyToAsk(t, rig.server, rigChat, "ask-dedup", "answer")
	select {
	case r := <-second:
		require.NoError(t, r.err)
		assert.Equal(t, "answer", r.resp.GetReply())
	case <-time.After(5 * time.Second):
		t.Fatal("the retry did not receive the reply")
	}

	// A later retry gets the cached result without asking again.
	resp, err := rig.svc.AskUser(rig.ctx(), req)
	require.NoError(t, err)
	assert.Equal(t, "answer", resp.GetReply())
	askCount := 0
	for _, m := range rig.messages(t, rigChat) {
		if m.Role == "ask_user" {
			askCount++
		}
	}
	assert.Equal(t, 1, askCount, "the question was recorded exactly once")
}

func TestCallbackCallSeqKindMismatch(t *testing.T) {
	rig := newCallbackRig(t)
	_, err := rig.svc.RecordMetrics(rig.ctx(), &runnerpb.RecordMetricsRequest{Meta: meta(rigRun, 1)})
	require.NoError(t, err)
	_, err = rig.svc.FindPeer(rig.ctx(), &runnerpb.FindPeerRequest{Meta: meta(rigRun, 1)})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCallbackAskUserEndsWithRun(t *testing.T) {
	rig := newCallbackRig(t)
	sub, _, cancel := rig.events.Subscribe(rigChat, 0)
	t.Cleanup(cancel)

	got := make(chan error, 1)
	go func() {
		_, err := rig.svc.AskUser(rig.ctx(), &runnerpb.AskUserRequest{Meta: meta(rigRun, 1), Question: "q", MessageId: "ask-end"})
		got <- err
	}()
	waitAskMessage(t, sub)
	close(rig.done)
	select {
	case err := <-got:
		assert.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(5 * time.Second):
		t.Fatal("AskUser stayed blocked after its run ended")
	}
	// The waiter was released.
	assert.Eventually(t, func() bool {
		askWaitersMu.Lock()
		defer askWaitersMu.Unlock()
		_, exists := askWaiters["ask-end"]
		return !exists
	}, 5*time.Second, 10*time.Millisecond)
}

func TestCallbackAskUserRequiresQuestion(t *testing.T) {
	rig := newCallbackRig(t)
	resp, err := rig.svc.AskUser(rig.ctx(), &runnerpb.AskUserRequest{Meta: meta(rigRun, 1)})
	require.NoError(t, err)
	assert.Equal(t, "question is required", resp.GetError())
}

func TestCallbackFindPeer(t *testing.T) {
	agents := []*agentspec.Agent{
		{Config: agentspec.AgentConfig{ID: "alpha", Name: "Alpha", Team: "t1"}},
		{Config: agentspec.AgentConfig{ID: "beta", Name: "Beta", Team: "t1"}},
		{Config: agentspec.AgentConfig{ID: "gamma", Name: "Gamma", Team: "t2"}},
	}
	rig := newCallbackRig(t, agents...)

	resp, err := rig.svc.FindPeer(rig.ctx(), &runnerpb.FindPeerRequest{Meta: meta(rigRun, 1), ChatId: "evil-chat"})
	require.NoError(t, err)
	assert.Equal(t, []string{"beta"}, resp.GetPeers(), "peers of the run's chat, not the claimed one")

	// A chat whose current agent is not loaded is reported as a failure.
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "ghost"}))
	_, err = rig.svc.FindPeer(rig.ctx(), &runnerpb.FindPeerRequest{Meta: meta(rigRun, 2)})
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestCallbackRecordMetrics(t *testing.T) {
	rig := newCallbackRig(t)
	_, err := rig.svc.RecordMetrics(rig.ctx(), &runnerpb.RecordMetricsRequest{
		Meta: meta(rigRun, 1),
		Events: []*runnerpb.MetricsEvent{
			{Kind: string(metrics.KindCompass), Tokens: 11},
			{Kind: string(metrics.KindShowOutput), Bytes: 100},
		},
	})
	require.NoError(t, err)
	snap := rig.store.Snapshot()
	assert.Equal(t, int64(1), snap.CompassCalls)
	assert.Equal(t, int64(11), snap.CompassTokens)
	assert.Equal(t, int64(1), snap.ShowOutputCalls)
	assert.Equal(t, int64(100), snap.ShowOutputBytes)

	// A retry of the same call is not applied twice.
	_, err = rig.svc.RecordMetrics(rig.ctx(), &runnerpb.RecordMetricsRequest{
		Meta:   meta(rigRun, 1),
		Events: []*runnerpb.MetricsEvent{{Kind: string(metrics.KindCompass), Tokens: 11}},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rig.store.Snapshot().CompassCalls)

	_, err = rig.svc.RecordMetrics(rig.ctx(), &runnerpb.RecordMetricsRequest{
		Meta: meta(rigRun, 2), Events: make([]*runnerpb.MetricsEvent, maxMetricsEvents+1),
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCallbackResultCacheExpires(t *testing.T) {
	rig := newCallbackRig(t)
	now := time.Now()
	rig.svc.now = func() time.Time { return now }
	call := func(seq uint64) {
		_, err := rig.svc.RecordMetrics(rig.ctx(), &runnerpb.RecordMetricsRequest{
			Meta: meta(rigRun, seq), Events: []*runnerpb.MetricsEvent{{Kind: string(metrics.KindCompass)}},
		})
		require.NoError(t, err)
	}
	call(1)
	call(1)
	assert.Equal(t, int64(1), rig.store.Snapshot().CompassCalls)

	// Past the TTL, a new call triggers the sweep and the entry is forgotten.
	now = now.Add(callbackResultTTL + 2*time.Minute)
	call(2)
	rig.svc.mu.Lock()
	_, kept := rig.svc.calls[callKey{runID: rigRun, seq: 1}]
	rig.svc.mu.Unlock()
	assert.False(t, kept)
}

func TestCallbackCallPeerRunsPeerInRunChat(t *testing.T) {
	tempDir := t.TempDir()
	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, fmt.Appendf(nil, `
name: peer-wf
tmp_dir: "%s/tmp/${session_id}"
nodes:
  - id: step1
    type: command
    command: "echo peer-done"
`, tempDir), 0o644))
	peer := &agentspec.Agent{
		Config:       agentspec.AgentConfig{ID: "peer-wf", Name: "Peer", Type: "workflow"},
		WorkflowPath: wfFile,
	}
	rig := newCallbackRig(t, peer)

	resp, err := rig.svc.CallPeer(rig.ctx(), &runnerpb.CallPeerRequest{
		Meta: meta(rigRun, 1), AgentId: "peer-wf", Prompt: "do it", ChatId: "evil-chat",
		MetadataJson: []byte(`{"caller_agent_id":"forged","note":"kept"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "completed", resp.GetStatus(), resp.GetError())
	assert.Contains(t, resp.GetOutput(), "step1: SUCCEEDED")

	// The peer ran in the caller's chat.
	other, err := rig.repo.GetSession("evil-chat")
	require.NoError(t, err)
	assert.Nil(t, other)
}

func TestCallbackCallPeerFailures(t *testing.T) {
	rig := newCallbackRig(t)
	tests := []struct {
		name string
		req  *runnerpb.CallPeerRequest
		want string
	}{
		{"missing agent", &runnerpb.CallPeerRequest{Meta: meta(rigRun, 1), Prompt: "x"}, "agent_id is required"},
		{"bad metadata", &runnerpb.CallPeerRequest{Meta: meta(rigRun, 2), AgentId: "ghost", Prompt: "x", MetadataJson: []byte("{")}, "invalid metadata_json"},
		{"unknown agent", &runnerpb.CallPeerRequest{Meta: meta(rigRun, 3), AgentId: "ghost", Prompt: "x"}, "agent not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := rig.svc.CallPeer(rig.ctx(), tt.req)
			require.NoError(t, err)
			assert.Equal(t, "failed", resp.GetStatus())
			assert.Contains(t, resp.GetError(), tt.want)
		})
	}
}

func TestCapText(t *testing.T) {
	assert.Equal(t, "short", capText("short"))
	long := strings.Repeat("é", maxCallbackText) // 2 bytes per rune
	got := capText(long)
	assert.True(t, utf8.ValidString(got))
	assert.LessOrEqual(t, len(got), maxCallbackText+len("\n[truncated]"))
	assert.True(t, strings.HasSuffix(got, "\n[truncated]"))
}

func TestCallbackCallPeerNullMetadata(t *testing.T) {
	rig := newCallbackRig(t)
	resp, err := rig.svc.CallPeer(rig.ctx(), &runnerpb.CallPeerRequest{Meta: meta(rigRun, 1), AgentId: "ghost", Prompt: "x", MetadataJson: []byte("null")})
	require.NoError(t, err)
	assert.Contains(t, resp.GetError(), "agent not found")
}

func TestCallbackResultCacheIsByteBounded(t *testing.T) {
	rig := newCallbackRig(t)
	now := time.Now()
	rig.svc.now = func() time.Time { now = now.Add(time.Second); return now }
	big := strings.Repeat("x", 5<<20)
	for seq := uint64(1); seq <= 20; seq++ {
		_, err := rig.svc.run(rig.ctx(), meta(rigRun, seq), "big", func(context.Context, runnerhub.RunInfo) (proto.Message, error) {
			return &runnerpb.AskUserResponse{Reply: big}, nil
		})
		require.NoError(t, err)
	}
	rig.svc.mu.Lock()
	defer rig.svc.mu.Unlock()
	assert.LessOrEqual(t, rig.svc.bytes, callbackMaxBytes)
	assert.Less(t, len(rig.svc.calls), 20)
	_, newest := rig.svc.calls[callKey{runID: rigRun, seq: 20}]
	_, oldest := rig.svc.calls[callKey{runID: rigRun, seq: 1}]
	assert.True(t, newest)
	assert.False(t, oldest, "oldest completed entries are evicted first")
}

func TestCallbackHandlerPanicBecomesInternal(t *testing.T) {
	rig := newCallbackRig(t)
	_, err := rig.svc.run(rig.ctx(), meta(rigRun, 1), "boom", func(context.Context, runnerhub.RunInfo) (proto.Message, error) {
		panic("bad request")
	})
	assert.Equal(t, codes.Internal, status.Code(err))
}

func TestCallbackInFlightCaps(t *testing.T) {
	rig := newCallbackRig(t)
	release := make(chan struct{})
	block := func(context.Context, runnerhub.RunInfo) (proto.Message, error) {
		<-release
		return &runnerpb.RecordMetricsResponse{}, nil
	}
	var wg sync.WaitGroup
	t.Cleanup(func() { wg.Wait() })
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)

	for seq := uint64(1); seq <= maxInFlightPerRun; seq++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = rig.svc.run(rig.ctx(), meta(rigRun, seq), "blocked", block)
		}()
	}
	require.Eventually(t, func() bool {
		rig.svc.mu.Lock()
		defer rig.svc.mu.Unlock()
		return rig.svc.inFlight[rigRun] == maxInFlightPerRun
	}, 5*time.Second, 5*time.Millisecond)

	_, err := rig.svc.run(rig.ctx(), meta(rigRun, 100), "blocked", block)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))

	// Another run is unaffected until the global cap.
	rig.runs.add(runnerhub.RunInfo{RunID: "run-2", NodeID: rigNode, ChatID: rigChat, Done: make(chan struct{})})
	rig.svc.mu.Lock()
	rig.svc.inTotal = maxInFlightTotal
	rig.svc.mu.Unlock()
	_, err = rig.svc.run(rig.ctx(), meta("run-2", 1), "blocked", block)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	rig.svc.mu.Lock()
	rig.svc.inTotal = maxInFlightPerRun
	rig.svc.mu.Unlock()

	releaseAll()
	wg.Wait()
	_, err = rig.svc.run(rig.ctx(), meta(rigRun, 101), "blocked", block)
	require.NoError(t, err)
	rig.svc.mu.Lock()
	defer rig.svc.mu.Unlock()
	assert.Zero(t, rig.svc.inTotal)
	assert.Empty(t, rig.svc.inFlight)
}

func TestCallbackFindPeerDegradedModeIsPermanent(t *testing.T) {
	rig := newCallbackRig(t)
	rig.server.repo = nil
	_, err := rig.svc.FindPeer(rig.ctx(), &runnerpb.FindPeerRequest{Meta: meta(rigRun, 1)})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestCallbackEndsWhenRunEndsEvenIfHandlerIgnoresContext(t *testing.T) {
	rig := newCallbackRig(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	started := make(chan struct{})
	got := make(chan error, 1)
	go func() {
		_, err := rig.svc.run(rig.ctx(), meta(rigRun, 1), "stuck", func(context.Context, runnerhub.RunInfo) (proto.Message, error) {
			close(started)
			<-release // ignores its context
			return &runnerpb.RecordMetricsResponse{}, nil
		})
		got <- err
	}()
	<-started
	close(rig.done)
	select {
	case err := <-got:
		assert.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(5 * time.Second):
		t.Fatal("call stayed in flight after its run ended")
	}
	rig.svc.mu.Lock()
	defer rig.svc.mu.Unlock()
	assert.Zero(t, rig.svc.inTotal)
	assert.Empty(t, rig.svc.inFlight)
	e := rig.svc.calls[callKey{runID: rigRun, seq: 1}]
	require.NotNil(t, e)
	assert.False(t, e.finishedAt.IsZero())
	assert.Equal(t, codes.Canceled, status.Code(e.err))
}

func TestCallbackCallPeerStopsPeerWhenCallerRunEnds(t *testing.T) {
	tempDir := t.TempDir()
	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, fmt.Appendf(nil, `
name: slow-wf
tmp_dir: "%s/tmp/${session_id}"
nodes:
  - id: step1
    type: command
    command: "sleep 60"
`, tempDir), 0o644))
	peer := &agentspec.Agent{
		Config:       agentspec.AgentConfig{ID: "slow-wf", Name: "Slow", Type: "workflow"},
		WorkflowPath: wfFile,
	}
	rig := newCallbackRig(t, peer)

	got := make(chan error, 1)
	go func() {
		_, err := rig.svc.CallPeer(rig.ctx(), &runnerpb.CallPeerRequest{Meta: meta(rigRun, 1), AgentId: "slow-wf", Prompt: "go"})
		got <- err
	}()
	require.Eventually(t, func() bool {
		_, running := rig.server.activeExecutions.Load(rigChat)
		return running
	}, 5*time.Second, 10*time.Millisecond, "the peer never started")

	close(rig.done) // the caller's run ends
	select {
	case err := <-got:
		assert.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(5 * time.Second):
		t.Fatal("call_peer stayed in flight after the caller's run ended")
	}
	rig.svc.mu.Lock()
	assert.Zero(t, rig.svc.inTotal)
	assert.Empty(t, rig.svc.inFlight)
	rig.svc.mu.Unlock()

	assert.Eventually(t, func() bool {
		_, running := rig.server.activeExecutions.Load(rigChat)
		return !running
	}, 10*time.Second, 20*time.Millisecond, "the peer run was not stopped")
}

func TestCallbackService_GetUserProxyKeys(t *testing.T) {
	rig := newCallbackRig(t)
	ctx := context.Background()

	require.NotNil(t, rig.server.userProxyKeyRepo)
	require.NoError(t, rig.server.userProxyKeyRepo.SaveKey(ctx, "user-42", "GEMINI_API_KEY", "gemini-secret-123"))
	require.NoError(t, rig.server.userProxyKeyRepo.SaveKey(ctx, "user-42", "OPENAI_API_KEY", "sk-openai-456"))

	t.Run("authorized runner and matching user returns keys", func(t *testing.T) {
		resp, err := rig.svc.GetUserProxyKeys(rig.ctx(), &runnerpb.GetUserProxyKeysRequest{
			Meta:   &runnerpb.CallMeta{RunId: rigRun},
			UserId: "user-42",
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "gemini-secret-123", resp.GetKeys()["GEMINI_API_KEY"])
		assert.Equal(t, "sk-openai-456", resp.GetKeys()["OPENAI_API_KEY"])
	})

	t.Run("unauthenticated runner returns Unauthenticated", func(t *testing.T) {
		_, err := rig.svc.GetUserProxyKeys(context.Background(), &runnerpb.GetUserProxyKeysRequest{
			Meta:   &runnerpb.CallMeta{RunId: rigRun},
			UserId: "user-42",
		})
		require.Error(t, err)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("missing run_id returns InvalidArgument", func(t *testing.T) {
		_, err := rig.svc.GetUserProxyKeys(rig.ctx(), &runnerpb.GetUserProxyKeysRequest{
			UserId: "user-42",
		})
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("missing user_id returns InvalidArgument", func(t *testing.T) {
		_, err := rig.svc.GetUserProxyKeys(rig.ctx(), &runnerpb.GetUserProxyKeysRequest{
			Meta: &runnerpb.CallMeta{RunId: rigRun},
		})
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("inactive run returns FailedPrecondition", func(t *testing.T) {
		_, err := rig.svc.GetUserProxyKeys(rig.ctx(), &runnerpb.GetUserProxyKeysRequest{
			Meta:   &runnerpb.CallMeta{RunId: "run-nonexistent"},
			UserId: "user-42",
		})
		require.Error(t, err)
		assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	})

	t.Run("user identity mismatch returns PermissionDenied", func(t *testing.T) {
		_, err := rig.svc.GetUserProxyKeys(rig.ctx(), &runnerpb.GetUserProxyKeysRequest{
			Meta:   &runnerpb.CallMeta{RunId: rigRun},
			UserId: "user-impersonator",
		})
		require.Error(t, err)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	})
}
