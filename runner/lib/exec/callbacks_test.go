package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/metrics"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/runner/lib/conn"
)

// fakeCallbacks is a manually written AgentCallbackServiceClient. Each method
// first runs its hook (if set) and otherwise answers with the canned reply.
type fakeCallbacks struct {
	mu       sync.Mutex
	asks     []*runnerpb.AskUserRequest
	peers    []*runnerpb.CallPeerRequest
	finds    []*runnerpb.FindPeerRequest
	records  []*runnerpb.RecordMetricsRequest
	askHook  func(n int, req *runnerpb.AskUserRequest) (*runnerpb.AskUserResponse, error)
	peerResp *runnerpb.CallPeerResponse
	peerErr  error
	findResp *runnerpb.FindPeerResponse
	findErr  error
}

func (f *fakeCallbacks) AskUser(ctx context.Context, req *runnerpb.AskUserRequest, _ ...grpc.CallOption) (*runnerpb.AskUserResponse, error) {
	f.mu.Lock()
	f.asks = append(f.asks, req)
	n := len(f.asks)
	hook := f.askHook
	f.mu.Unlock()
	if hook != nil {
		return hook(n, req)
	}
	return &runnerpb.AskUserResponse{Reply: "yes"}, nil
}

func (f *fakeCallbacks) CallPeer(_ context.Context, req *runnerpb.CallPeerRequest, _ ...grpc.CallOption) (*runnerpb.CallPeerResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peers = append(f.peers, req)
	return f.peerResp, f.peerErr
}

func (f *fakeCallbacks) FindPeer(_ context.Context, req *runnerpb.FindPeerRequest, _ ...grpc.CallOption) (*runnerpb.FindPeerResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finds = append(f.finds, req)
	return f.findResp, f.findErr
}

func (f *fakeCallbacks) RecordMetrics(_ context.Context, req *runnerpb.RecordMetricsRequest, _ ...grpc.CallOption) (*runnerpb.RecordMetricsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, req)
	return &runnerpb.RecordMetricsResponse{}, nil
}

func (f *fakeCallbacks) askCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asks)
}

// callbackRig serves a statusServer with fake callbacks on a unix socket.
type callbackRig struct {
	srv    *statusServer
	client *http.Client
	fake   *fakeCallbacks
}

func newCallbackRig(t *testing.T, ctx context.Context, fake *fakeCallbacks, cb CallbacksFunc) *callbackRig {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	srv := newStatusServer(ctx, baseStartRun(), testAgent("a", "h1", ""), t.TempDir(), newRecordingSink())
	srv.retryMin, srv.retryMax = time.Millisecond, 5*time.Millisecond
	if cb == nil {
		cb = func() (runnerpb.AgentCallbackServiceClient, error) { return fake, nil }
	}
	srv.callbacks = cb
	dir := shortSockDir(t)
	stop, err := srv.serve(dir)
	require.NoError(t, err)
	t.Cleanup(stop)
	return &callbackRig{srv: srv, client: unixClient(filepath.Join(dir, SocketName)), fake: fake}
}

func (r *callbackRig) post(t *testing.T, path string, body any) (int, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := r.client.Post("http://asgard"+path, "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func TestCallbacksStampIdentityAndSequence(t *testing.T) {
	fake := &fakeCallbacks{
		peerResp: &runnerpb.CallPeerResponse{Status: "completed", Output: "peer out"},
		findResp: &runnerpb.FindPeerResponse{Peers: []string{"bob"}},
	}
	rig := newCallbackRig(t, context.Background(), fake, nil)

	// The sandbox claims other ids; they are ignored.
	code, body := rig.post(t, sandboxsock.PathAskUser, map[string]any{
		"chat_id": "evil-chat", "run_id": "evil-run", "call_seq": 99, "agent_id": "evil",
		"question": "color?", "agent_name": "ignored", "model": "m1",
	})
	require.Equal(t, http.StatusOK, code)
	var ask sandboxsock.AskUserResponse
	require.NoError(t, json.Unmarshal(body, &ask))
	assert.Equal(t, "yes", ask.Reply)

	code, body = rig.post(t, sandboxsock.PathCallPeer, map[string]any{"chat_id": "evil-chat", "agent_id": "bob", "prompt": "hi"})
	require.Equal(t, http.StatusOK, code)
	var peer sandboxsock.CallPeerResponse
	require.NoError(t, json.Unmarshal(body, &peer))
	assert.Equal(t, sandboxsock.CallPeerResponse{Status: "completed", Output: "peer out"}, peer)

	resp, err := rig.client.Get("http://asgard" + sandboxsock.PathFindPeer)
	require.NoError(t, err)
	var peers []string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&peers))
	_ = resp.Body.Close()
	assert.Equal(t, []string{"bob"}, peers)

	code, _ = rig.post(t, sandboxsock.PathMetrics, []metrics.Event{{Kind: metrics.KindJev, Tokens: 5}})
	require.Equal(t, http.StatusNoContent, code)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.asks, 1)
	a := fake.asks[0]
	assert.Equal(t, "run-1", a.GetMeta().GetRunId())
	assert.Equal(t, uint64(1), a.GetMeta().GetCallSeq())
	assert.Equal(t, "chat-1", a.GetChatId())
	assert.Equal(t, "a", a.GetAgentName(), "the runner's agent name, not the sandbox's")
	assert.Equal(t, "ask-run-1-1", a.GetMessageId(), "derived from run_id and call_seq")
	assert.Equal(t, "m1", a.GetModel())

	require.Len(t, fake.peers, 1)
	p := fake.peers[0]
	assert.Equal(t, uint64(2), p.GetMeta().GetCallSeq())
	assert.Equal(t, "run-1", p.GetMeta().GetRunId())
	assert.Equal(t, "chat-1", p.GetChatId())
	assert.Equal(t, "bob", p.GetAgentId())
	var meta map[string]any
	require.NoError(t, json.Unmarshal(p.GetMetadataJson(), &meta))
	assert.Equal(t, map[string]any{"internal": true, "source": "call-peer", "caller_agent_id": "a", "caller_agent_name": "a"}, meta)

	require.Len(t, fake.finds, 1)
	assert.Equal(t, uint64(3), fake.finds[0].GetMeta().GetCallSeq())
	assert.Equal(t, "chat-1", fake.finds[0].GetChatId())

	require.Len(t, fake.records, 1)
	assert.Equal(t, uint64(4), fake.records[0].GetMeta().GetCallSeq())
	assert.Equal(t, []*runnerpb.MetricsEvent{{Kind: "jev", Tokens: 5}}, fake.records[0].GetEvents())
}

func TestAskUserMessageID(t *testing.T) {
	tests := []struct {
		name string
		sent string
		want string
	}{
		{"sandbox id kept", "ask-123_x.y", "ask-123_x.y"},
		{"missing id derived", "", "ask-run-1-1"},
		{"bad characters derived", "../../x", "ask-run-1-1"},
		{"too long derived", strings.Repeat("a", 129), "ask-run-1-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeCallbacks{}
			rig := newCallbackRig(t, context.Background(), fake, nil)
			code, _ := rig.post(t, sandboxsock.PathAskUser, map[string]any{"question": "q", "message_id": tt.sent})
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, 1, fake.askCount())
			assert.Equal(t, tt.want, fake.asks[0].GetMessageId())
		})
	}
}

func TestCallbacksRetryAcrossReconnectKeepsCallSeq(t *testing.T) {
	fake := &fakeCallbacks{}
	// The connection drops under the first two attempts; in between the
	// runner has no connection at all.
	fake.askHook = func(n int, _ *runnerpb.AskUserRequest) (*runnerpb.AskUserResponse, error) {
		if n <= 2 {
			return nil, status.Error(codes.Unavailable, "transport is closing")
		}
		return &runnerpb.AskUserResponse{Reply: "finally"}, nil
	}
	var mu sync.Mutex
	polls := 0
	rig := newCallbackRig(t, context.Background(), fake, func() (runnerpb.AgentCallbackServiceClient, error) {
		mu.Lock()
		defer mu.Unlock()
		polls++
		if polls == 2 {
			return nil, conn.ErrNotConnected
		}
		return fake, nil
	})

	code, body := rig.post(t, sandboxsock.PathAskUser, map[string]any{"question": "q"})
	require.Equal(t, http.StatusOK, code)
	var ask sandboxsock.AskUserResponse
	require.NoError(t, json.Unmarshal(body, &ask))
	assert.Equal(t, "finally", ask.Reply)

	require.Equal(t, 3, fake.askCount())
	for _, a := range fake.asks {
		assert.Equal(t, uint64(1), a.GetMeta().GetCallSeq(), "retries reuse the call_seq")
		assert.Equal(t, "ask-run-1-1", a.GetMessageId())
	}

	// The next sandbox call is a new call.
	code, _ = rig.post(t, sandboxsock.PathAskUser, map[string]any{"question": "q2"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, uint64(2), fake.asks[3].GetMeta().GetCallSeq())
}

func TestCallbacksPermanentErrorIsNotRetried(t *testing.T) {
	for _, code := range []codes.Code{codes.FailedPrecondition, codes.ResourceExhausted, codes.Internal, codes.InvalidArgument, codes.NotFound, codes.Unauthenticated} {
		t.Run(code.String(), func(t *testing.T) {
			fake := &fakeCallbacks{}
			fake.askHook = func(int, *runnerpb.AskUserRequest) (*runnerpb.AskUserResponse, error) {
				return nil, status.Error(code, "backend said no")
			}
			rig := newCallbackRig(t, context.Background(), fake, nil)

			code, body := rig.post(t, sandboxsock.PathAskUser, map[string]any{"question": "q"})
			assert.Equal(t, http.StatusBadGateway, code)
			assert.Contains(t, string(body), "backend said no")
			assert.Equal(t, 1, fake.askCount())
		})
	}
}

func TestFindPeerPermanentErrorReturnsToSandbox(t *testing.T) {
	fake := &fakeCallbacks{findErr: status.Error(codes.FailedPrecondition, "database unavailable in degraded mode")}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	resp, err := rig.client.Get("http://asgard" + sandboxsock.PathFindPeer)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Len(t, fake.finds, 1)
}

func TestCallbacksWaitForRunCancellation(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	fake := &fakeCallbacks{}
	started := make(chan struct{})
	fake.askHook = func(_ int, _ *runnerpb.AskUserRequest) (*runnerpb.AskUserResponse, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		// Blocks like the backend waiting for the user; the transport reports
		// the run's cancellation as a Canceled status.
		<-runCtx.Done()
		return nil, status.Error(codes.Canceled, "context canceled")
	}
	rig := newCallbackRig(t, runCtx, fake, nil)

	done := make(chan int, 1)
	go func() {
		code, _ := rig.post(t, sandboxsock.PathAskUser, map[string]any{"question": "q"})
		done <- code
	}()
	<-started
	select {
	case <-done:
		t.Fatal("ask-user returned before the user replied or the run ended")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case code := <-done:
		assert.Equal(t, http.StatusServiceUnavailable, code)
	case <-time.After(5 * time.Second):
		t.Fatal("ask-user did not return after the run was cancelled")
	}
	assert.Equal(t, 1, fake.askCount(), "a cancelled run is not retried")
}

func TestCallbacksNotConnectedWaitsForReconnect(t *testing.T) {
	fake := &fakeCallbacks{}
	var mu sync.Mutex
	connected := false
	rig := newCallbackRig(t, context.Background(), fake, func() (runnerpb.AgentCallbackServiceClient, error) {
		mu.Lock()
		defer mu.Unlock()
		if !connected {
			return nil, conn.ErrNotConnected
		}
		return fake, nil
	})

	done := make(chan int, 1)
	go func() {
		code, _ := rig.post(t, sandboxsock.PathAskUser, map[string]any{"question": "q"})
		done <- code
	}()
	time.Sleep(30 * time.Millisecond)
	assert.Zero(t, fake.askCount())
	mu.Lock()
	connected = true
	mu.Unlock()
	select {
	case code := <-done:
		assert.Equal(t, http.StatusOK, code)
	case <-time.After(5 * time.Second):
		t.Fatal("ask-user did not resume after reconnect")
	}
}

func TestCallbacksRejectBadRequests(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)

	tests := []struct {
		name string
		path string
		body any
		want int
	}{
		{"ask without question", sandboxsock.PathAskUser, map[string]any{"question": ""}, http.StatusBadRequest},
		{"ask oversized", sandboxsock.PathAskUser, map[string]any{"question": strings.Repeat("x", maxCallText+1)}, http.StatusRequestEntityTooLarge},
		{"peer without agent", sandboxsock.PathCallPeer, map[string]any{"prompt": "x"}, http.StatusBadRequest},
		{"peer oversized", sandboxsock.PathCallPeer, map[string]any{"agent_id": "b", "prompt": strings.Repeat("x", maxCallText+1)}, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _ := rig.post(t, tt.path, tt.body)
			assert.Equal(t, tt.want, code)
		})
	}
	assert.Zero(t, fake.askCount())
}

func TestCallbacksWithoutBackendClient(t *testing.T) {
	rig := newCallbackRig(t, context.Background(), &fakeCallbacks{}, nil)
	rig.srv.callbacks = nil
	code, body := rig.post(t, sandboxsock.PathAskUser, map[string]any{"question": "q"})
	assert.Equal(t, http.StatusBadGateway, code)
	assert.Contains(t, string(body), "not available")
}
