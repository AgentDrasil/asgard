package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
)

func TestSubagentRoutesRelayWithRunIdentity(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	runID := rig.srv.sr.GetRunId()

	// Identity fields smuggled in the body are not part of the protocol.
	code, body := rig.post(t, sandboxsock.PathSubagentSpawn, map[string]any{
		"agent_id": "beta", "model": "flash", "readonly": true, "label": "scan", "prompt": "look",
		"chat_id": "evil-chat", "caller_agent_id": "evil",
	})
	require.Equal(t, http.StatusOK, code, string(body))
	var spawn sandboxsock.SubagentSpawnResponse
	require.NoError(t, json.Unmarshal(body, &spawn))
	assert.Equal(t, "child-1", spawn.ID)
	require.Len(t, fake.spawns, 1)
	got := fake.spawns[0]
	assert.Equal(t, runID, got.GetMeta().GetRunId())
	assert.NotZero(t, got.GetMeta().GetCallSeq())
	assert.Equal(t, "beta", got.GetAgentId())
	assert.Equal(t, "flash", got.GetModel())
	assert.True(t, got.GetReadonly())
	assert.Equal(t, "scan", got.GetLabel())
	assert.Equal(t, "look", got.GetPrompt())

	resp, err := rig.client.Get("http://asgard" + sandboxsock.PathSubagentList)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list sandboxsock.SubagentListResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	assert.Equal(t, []sandboxsock.SubagentInfo{{ID: "child-1", Label: "l", Agent: "beta", Model: "m", Readonly: true, State: "running", LastActivityMs: 42}}, list.Subagents)
	require.Len(t, fake.lists, 1)
	assert.Equal(t, runID, fake.lists[0].GetMeta().GetRunId())

	code, body = rig.post(t, sandboxsock.PathSubagentStatus, sandboxsock.SubagentIDRequest{ID: "child-1"})
	require.Equal(t, http.StatusOK, code, string(body))
	var st sandboxsock.SubagentStatusResponse
	require.NoError(t, json.Unmarshal(body, &st))
	assert.Equal(t, "all good", st.Subagent.Summary)
	require.Len(t, fake.stats, 1)
	assert.Equal(t, "child-1", fake.stats[0].GetId())

	code, body = rig.post(t, sandboxsock.PathSubagentKill, sandboxsock.SubagentIDRequest{ID: "child-1"})
	require.Equal(t, http.StatusOK, code, string(body))
	var kill sandboxsock.SubagentKillResponse
	require.NoError(t, json.Unmarshal(body, &kill))
	assert.Equal(t, 2, kill.Stopped)
	require.Len(t, fake.kills, 1)
	assert.Equal(t, "child-1", fake.kills[0].GetId())
}

func TestSubagentSendRelaysTextWithRunIdentity(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	code, body := rig.post(t, sandboxsock.PathSubagentSend, map[string]any{"id": "child-1", "text": "hello", "chat_id": "evil"})
	require.Equal(t, http.StatusOK, code, string(body))
	var out sandboxsock.SubagentSendResponse
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, "queued", out.Delivery)
	require.Len(t, fake.sends, 1)
	assert.Equal(t, rig.srv.sr.GetRunId(), fake.sends[0].GetMeta().GetRunId())
	assert.Equal(t, "child-1", fake.sends[0].GetId())
	assert.Equal(t, "hello", fake.sends[0].GetText())

	code, _ = rig.post(t, sandboxsock.PathSubagentSend, sandboxsock.SubagentSendRequest{ID: "child-1", Text: " "})
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestSubagentRoutesMapBackendCodes(t *testing.T) {
	tests := []struct {
		code codes.Code
		want int
	}{
		{codes.InvalidArgument, http.StatusBadRequest},
		{codes.PermissionDenied, http.StatusForbidden},
		{codes.NotFound, http.StatusNotFound},
		{codes.ResourceExhausted, http.StatusTooManyRequests},
		{codes.FailedPrecondition, http.StatusServiceUnavailable},
		{codes.Internal, http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.code.String(), func(t *testing.T) {
			fake := &fakeCallbacks{subErr: status.Error(tt.code, "the reason")}
			rig := newCallbackRig(t, context.Background(), fake, nil)
			code, body := rig.post(t, sandboxsock.PathSubagentKill, sandboxsock.SubagentIDRequest{ID: "c"})
			assert.Equal(t, tt.want, code)
			var e sandboxsock.ErrorResponse
			require.NoError(t, json.Unmarshal(body, &e))
			assert.Equal(t, "the reason", e.Error)
			assert.Len(t, fake.kills, 1, "a permanent backend answer is not retried")
		})
	}
}

func TestSubagentRoutesRejectBadRequests(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	for name, tt := range map[string]struct {
		path string
		body any
	}{
		"spawn without agent": {sandboxsock.PathSubagentSpawn, sandboxsock.SubagentSpawnRequest{Prompt: "x"}},
		"spawn blank prompt":  {sandboxsock.PathSubagentSpawn, sandboxsock.SubagentSpawnRequest{AgentID: "a", Prompt: "  "}},
		"status without id":   {sandboxsock.PathSubagentStatus, sandboxsock.SubagentIDRequest{}},
		"kill with oversized": {sandboxsock.PathSubagentKill, sandboxsock.SubagentIDRequest{ID: string(make([]byte, 500))}},
		"kill with empty id":  {sandboxsock.PathSubagentKill, map[string]any{"id": ""}},
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := rig.post(t, tt.path, tt.body)
			assert.Equal(t, http.StatusBadRequest, code)
		})
	}
	assert.Empty(t, fake.spawns)
	assert.Empty(t, fake.stats)
	assert.Empty(t, fake.kills)
}
