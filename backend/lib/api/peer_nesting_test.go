package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func peerTestAgent(id string) *agentspec.Agent {
	return &agentspec.Agent{Config: agentspec.AgentConfig{ID: id, Name: id}}
}

// holdChat makes the rig's caller run (agent alpha) hold the chat guard, as a
// running agent does while it waits for call-peer.
func holdChat(t *testing.T, rig *callbackRig) {
	t.Helper()
	_, handle, loaded := rig.server.beginExecution(rigChat, "alpha", false)
	require.False(t, loaded)
	t.Cleanup(func() {
		rig.server.activeExecutions.Delete(rigChat)
		handle.cancel()
	})
}

func callPeerReq(runID string, seq uint64, agent string) *runnerpb.CallPeerRequest {
	return &runnerpb.CallPeerRequest{Meta: meta(runID, seq), AgentId: agent, Prompt: "hi"}
}

func TestCallPeerIntoOwnChatSucceeds(t *testing.T) {
	rig := newCallbackRig(t, peerTestAgent("alpha"), peerTestAgent("beta"))
	holdChat(t, rig)
	rig.server.runSingleAgentFn = func(_ context.Context, a *agentspec.Agent, chatID string, _ TriggerMessageRequest) (string, string, error) {
		assert.Equal(t, rigChat, chatID)
		return "completed", "from " + a.Config.ID, nil
	}

	resp, err := rig.svc.CallPeer(rig.ctx(), callPeerReq(rigRun, 1, "beta"))
	require.NoError(t, err)
	assert.Equal(t, "completed", resp.GetStatus(), resp.GetError())
	assert.Equal(t, "from beta", resp.GetOutput())

	// The caller still holds the guard and the call chain is cleaned up.
	_, running := rig.server.activeExecutions.Load(rigChat)
	assert.True(t, running)
	assert.Zero(t, rig.server.peerChainCount())
}

func TestCallPeerCycleAndDepthRejected(t *testing.T) {
	rig := newCallbackRig(t, peerTestAgent("alpha"), peerTestAgent("beta"), peerTestAgent("gamma"), peerTestAgent("delta"), peerTestAgent("eps"))
	holdChat(t, rig)

	// Self-call.
	resp, err := rig.svc.CallPeer(rig.ctx(), callPeerReq(rigRun, 1, "alpha"))
	require.NoError(t, err)
	assert.Equal(t, "failed", resp.GetStatus())
	assert.Contains(t, resp.GetError(), "call-peer cycle")
	assert.NotContains(t, resp.GetError(), "already running")

	// alpha -> beta -> gamma -> delta -> (alpha: cycle | eps: too deep).
	var inner []string
	var seq uint64
	rig.server.runSingleAgentFn = func(_ context.Context, a *agentspec.Agent, _ string, _ TriggerMessageRequest) (string, string, error) {
		next := map[string]string{"beta": "gamma", "gamma": "delta", "delta": "alpha"}[a.Config.ID]
		if a.Config.ID == "delta" && len(inner) == 1 {
			next = "eps"
		}
		runID := "run-" + a.Config.ID
		rig.runs.add(runnerhub.RunInfo{RunID: runID, NodeID: rigNode, ChatID: rigChat, AgentID: a.Config.ID, Done: make(chan struct{})})
		r, err := rig.svc.CallPeer(rig.ctx(), callPeerReq(runID, atomic.AddUint64(&seq, 1), next))
		require.NoError(t, err)
		if next == "alpha" || next == "eps" {
			inner = append(inner, r.GetError())
			return "completed", "end", nil
		}
		return "completed", r.GetOutput(), nil
	}
	resp, err = rig.svc.CallPeer(rig.ctx(), callPeerReq(rigRun, 2, "beta"))
	require.NoError(t, err)
	assert.Equal(t, "completed", resp.GetStatus(), resp.GetError())
	require.Len(t, inner, 1)
	assert.Contains(t, inner[0], "call-peer cycle")
	assert.Contains(t, inner[0], "alpha -> beta -> gamma -> delta -> alpha")

	inner = []string{"x"}
	resp, err = rig.svc.CallPeer(rig.ctx(), callPeerReq(rigRun, 3, "beta"))
	require.NoError(t, err)
	assert.Equal(t, "completed", resp.GetStatus(), resp.GetError())
	require.Len(t, inner, 2)
	assert.Contains(t, inner[1], "too deep")
	assert.Zero(t, rig.server.peerChainCount())
}

func TestTriggerIntoBusyChatStill409(t *testing.T) {
	rig := newCallbackRig(t, peerTestAgent("alpha"), peerTestAgent("beta"))
	holdChat(t, rig)

	body, err := json.Marshal(TriggerMessageRequest{Prompt: "hi", ChatID: rigChat})
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	rig.server.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/agents/beta/message?wait=true", bytes.NewReader(body)))
	assert.Equal(t, http.StatusConflict, rr.Code)

	// A call-peer whose guard holder changed meanwhile is a 409 too.
	pc := &peerCall{chatID: rigChat, holder: struct{}{}, callerAgentID: "alpha"}
	rr = httptest.NewRecorder()
	rig.server.triggerMessage(context.WithValue(context.Background(), peerCallKey{}, pc), rr, "beta", nil, TriggerMessageRequest{Prompt: "hi", ChatID: rigChat}, true)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestCallPeerCancelledWhenCallerStopped(t *testing.T) {
	rig := newCallbackRig(t, peerTestAgent("alpha"), peerTestAgent("beta"))
	holdChat(t, rig)
	started := make(chan struct{})
	rig.server.runSingleAgentFn = func(ctx context.Context, _ *agentspec.Agent, _ string, _ TriggerMessageRequest) (string, string, error) {
		close(started)
		<-ctx.Done()
		return "failed", "", ctx.Err()
	}

	got := make(chan *runnerpb.CallPeerResponse, 1)
	go func() {
		resp, _ := rig.svc.CallPeer(rig.ctx(), callPeerReq(rigRun, 1, "beta"))
		got <- resp
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("peer never started")
	}

	code, msg := rig.server.stopSessionExecution(rigChat)
	require.Equal(t, http.StatusOK, code, msg)
	select {
	case resp := <-got:
		require.NotNil(t, resp)
		assert.Equal(t, "failed", resp.GetStatus())
	case <-time.After(5 * time.Second):
		t.Fatal("stopping the caller did not cancel the peer")
	}
}

func (s *Server) peerChainCount() int {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	return len(s.peerChains)
}

func TestCallPeerRejectedWhenHolderIsNotTheCaller(t *testing.T) {
	rig := newCallbackRig(t, peerTestAgent("alpha"), peerTestAgent("beta"))
	// Another agent's execution holds the chat, not the calling run's.
	_, handle, loaded := rig.server.beginExecution(rigChat, "other", false)
	require.False(t, loaded)
	t.Cleanup(handle.cancel)
	rig.server.runSingleAgentFn = func(context.Context, *agentspec.Agent, string, TriggerMessageRequest) (string, string, error) {
		t.Error("peer must not run")
		return "completed", "", nil
	}
	resp, err := rig.svc.CallPeer(rig.ctx(), callPeerReq(rigRun, 1, "beta"))
	require.NoError(t, err)
	assert.Equal(t, "failed", resp.GetStatus())
	assert.Contains(t, resp.GetError(), "already running")
}

func TestNestedPeerDoesNotAnnounceDone(t *testing.T) {
	rig := newCallbackRig(t, peerTestAgent("alpha"), peerTestAgent("beta"))
	ch, _, unsub := rig.events.Subscribe(rigChat, 0)
	defer unsub()
	exec := NewSingleAgentExecutor(peerTestAgent("beta"), rig.server.conf, rig.repo, rig.server, nil)

	nested := context.WithValue(context.Background(), nestedPeerKey{}, true)
	exec.markAgentCompleted(nested, rigChat, nil)
	select {
	case ev := <-ch:
		t.Fatalf("nested peer published %s", ev.Type)
	case <-time.After(200 * time.Millisecond):
	}

	exec.markAgentCompleted(context.Background(), rigChat, nil)
	select {
	case ev := <-ch:
		assert.Equal(t, "status", ev.Type)
	case <-time.After(2 * time.Second):
		t.Fatal("a root run must announce completion")
	}
}
