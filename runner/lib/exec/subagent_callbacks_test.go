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

func TestSubagentWaitRelaysRequestAndResults(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	code, body := rig.post(t, sandboxsock.PathSubagentWait, map[string]any{"ids": []string{"c1", "c2"}, "any": true, "timeout_ms": 90000, "chat_id": "evil"})
	require.Equal(t, http.StatusOK, code, string(body))
	var out sandboxsock.SubagentWaitResponse
	require.NoError(t, json.Unmarshal(body, &out))
	require.Len(t, out.Results, 2)
	assert.Equal(t, "done", out.Results[0].Subagent.State)
	assert.Equal(t, "see /shared", out.Results[0].Notes)
	assert.True(t, out.Results[1].StillRunning)
	assert.True(t, out.WokenByMessage)
	assert.Equal(t, []string{"subagent c1 (l)"}, out.MessageFrom)
	assert.Equal(t, "c2", out.CheckoutHeldBy)
	require.Len(t, fake.waits, 1)
	assert.Equal(t, rig.srv.sr.GetRunId(), fake.waits[0].GetMeta().GetRunId())
	assert.Equal(t, []string{"c1", "c2"}, fake.waits[0].GetIds())
	assert.True(t, fake.waits[0].GetAny())
	assert.EqualValues(t, 90000, fake.waits[0].GetTimeoutMs())

	code, _ = rig.post(t, sandboxsock.PathSubagentWait, sandboxsock.SubagentWaitRequest{IDs: []string{" "}})
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = rig.post(t, sandboxsock.PathSubagentWait, sandboxsock.SubagentWaitRequest{TimeoutMs: -1})
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestSubagentTreeRelaysRunIdentity(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	resp, err := rig.client.Get("http://asgard" + sandboxsock.PathSubagentTree + "?chat_id=evil")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var tree sandboxsock.SubagentTreeResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tree))
	assert.Equal(t, []sandboxsock.SubagentTreeNode{
		{ID: "root-1", Agent: "alpha", State: "running"},
		{ID: "child-1", ParentID: "root-1", Agent: "beta", Label: "l", State: "done", Readonly: true, Depth: 1, Self: true},
	}, tree.Nodes)
	assert.True(t, tree.Truncated)
	require.Len(t, fake.trees, 1)
	assert.Equal(t, rig.srv.sr.GetRunId(), fake.trees[0].GetMeta().GetRunId())
}

func TestSubagentReplyRelaysTextWithRunIdentity(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	code, body := rig.post(t, sandboxsock.PathSubagentReply, map[string]any{"text": "question?", "chat_id": "evil"})
	require.Equal(t, http.StatusOK, code, string(body))
	var out sandboxsock.SubagentReplyResponse
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, "injected", out.Delivery)
	require.Len(t, fake.replies, 1)
	assert.Equal(t, rig.srv.sr.GetRunId(), fake.replies[0].GetMeta().GetRunId())
	assert.Equal(t, "question?", fake.replies[0].GetText())

	code, _ = rig.post(t, sandboxsock.PathSubagentReply, sandboxsock.SubagentReplyRequest{Text: " "})
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestSubagentAskRelaysWithRunIdentity(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	code, body := rig.post(t, sandboxsock.PathSubagentAsk, map[string]any{"to": "c-2", "text": "which branch?", "timeout_ms": 90000, "chat_id": "evil"})
	require.Equal(t, http.StatusOK, code, string(body))
	var out sandboxsock.SubagentAskResponse
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, sandboxsock.SubagentAskResponse{QID: "q-1", State: sandboxsock.QuestionPending}, out)
	require.Len(t, fake.asksSub, 1)
	got := fake.asksSub[0]
	assert.Equal(t, rig.srv.sr.GetRunId(), got.GetMeta().GetRunId())
	assert.NotZero(t, got.GetMeta().GetCallSeq())
	assert.Equal(t, "c-2", got.GetTo())
	assert.Equal(t, "which branch?", got.GetText())
	assert.EqualValues(t, 90000, got.GetTimeoutMs())

	code, body = rig.post(t, sandboxsock.PathSubagentAsk, sandboxsock.SubagentAskRequest{ResumeQID: "q-1"})
	require.Equal(t, http.StatusOK, code, string(body))
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, sandboxsock.SubagentAskResponse{QID: "q-1", State: sandboxsock.QuestionAnswered, Answer: "resumed"}, out)
	require.Len(t, fake.asksSub, 2)
	assert.Equal(t, "q-1", fake.asksSub[1].GetResumeQid())
	assert.NotEqual(t, got.GetMeta().GetCallSeq(), fake.asksSub[1].GetMeta().GetCallSeq())
}

func TestSubagentAskRejectsBadRequests(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	for name, req := range map[string]sandboxsock.SubagentAskRequest{
		"no target":          {Text: "x"},
		"blank question":     {To: "c", Text: " "},
		"resume with target": {To: "c", ResumeQID: "q"},
		"resume with text":   {Text: "x", ResumeQID: "q"},
		"negative timeout":   {To: "c", Text: "x", TimeoutMs: -1},
		"oversized id":       {To: string(make([]byte, 500)), Text: "x"},
		"to and topic":       {To: "c", Topic: "design", Text: "x"},
		"topic and resume":   {Topic: "design", ResumeQID: "q"},
		"blank topic ask":    {Topic: "design", Text: " "},
		"oversized topic":    {Topic: string(make([]byte, 100)), Text: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := rig.post(t, sandboxsock.PathSubagentAsk, req)
			assert.Equal(t, http.StatusBadRequest, code)
		})
	}
	assert.Empty(t, fake.asksSub)
}

func TestSubagentAnswerAndQuestionsRelay(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	code, body := rig.post(t, sandboxsock.PathSubagentAnswer, map[string]any{"qid": "q-1", "text": "main", "chat_id": "evil"})
	require.Equal(t, http.StatusOK, code, string(body))
	var ans sandboxsock.SubagentAnswerResponse
	require.NoError(t, json.Unmarshal(body, &ans))
	assert.Equal(t, "unblocked", ans.Delivery)
	require.Len(t, fake.answers, 1)
	assert.Equal(t, rig.srv.sr.GetRunId(), fake.answers[0].GetMeta().GetRunId())
	assert.Equal(t, "q-1", fake.answers[0].GetQid())
	assert.Equal(t, "main", fake.answers[0].GetText())
	code, _ = rig.post(t, sandboxsock.PathSubagentAnswer, sandboxsock.SubagentAnswerRequest{QID: "q-1", Text: " "})
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = rig.post(t, sandboxsock.PathSubagentAnswer, sandboxsock.SubagentAnswerRequest{Text: "x"})
	assert.Equal(t, http.StatusBadRequest, code)

	resp, err := rig.client.Get("http://asgard" + sandboxsock.PathSubagentQuestions)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var qs sandboxsock.SubagentQuestionsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&qs))
	assert.Equal(t, []sandboxsock.SubagentQuestionInfo{{QID: "q-1", FromChatID: "c-1", FromAgent: "Lead", FromLabel: "l", CreatedMs: 42, FirstLine: "which branch?"}}, qs.Incoming)
	assert.Equal(t, []sandboxsock.SubagentQuestionInfo{{QID: "q-2", ToChatID: "c-2", State: "pending"}}, qs.Outgoing)
	require.Len(t, fake.qs, 1)
	assert.Equal(t, rig.srv.sr.GetRunId(), fake.qs[0].GetMeta().GetRunId())
}

func TestSubagentAskMapsBackendCodes(t *testing.T) {
	fake := &fakeCallbacks{subErr: status.Error(codes.FailedPrecondition, "target c-2 was killed")}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	code, body := rig.post(t, sandboxsock.PathSubagentAsk, sandboxsock.SubagentAskRequest{To: "c-2", Text: "x"})
	assert.Equal(t, http.StatusServiceUnavailable, code)
	var e sandboxsock.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &e))
	assert.Equal(t, "target c-2 was killed", e.Error)
	assert.Len(t, fake.asksSub, 1, "a permanent backend answer is not retried")
}

func TestSubagentAskByTopicRelaysTarget(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	code, body := rig.post(t, sandboxsock.PathSubagentAsk, map[string]any{"topic": "design", "text": "which layout?", "chat_id": "evil"})
	require.Equal(t, http.StatusOK, code, string(body))
	var out sandboxsock.SubagentAskResponse
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, sandboxsock.SubagentAskResponse{
		QID: "q-3", State: sandboxsock.QuestionAnswered, Answer: "use main",
		Topic: "design", TargetID: "c-9", TargetAgent: "architect", TargetLabel: "arch",
	}, out)
	require.Len(t, fake.asksSub, 1)
	assert.Equal(t, "design", fake.asksSub[0].GetTopic())
	assert.Empty(t, fake.asksSub[0].GetTo())
	assert.Equal(t, rig.srv.sr.GetRunId(), fake.asksSub[0].GetMeta().GetRunId())
}

func TestSubagentRouteRelay(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)

	code, body := rig.post(t, sandboxsock.PathSubagentRouteAdd, map[string]any{"topic": "design", "to": "c-9", "note": "layout", "chat_id": "evil"})
	require.Equal(t, http.StatusOK, code, string(body))
	var add sandboxsock.SubagentRouteAddResponse
	require.NoError(t, json.Unmarshal(body, &add))
	assert.False(t, add.Replaced)
	code, body = rig.post(t, sandboxsock.PathSubagentRouteAdd, sandboxsock.SubagentRouteAddRequest{Topic: "again", To: "c-9"})
	require.Equal(t, http.StatusOK, code, string(body))
	require.NoError(t, json.Unmarshal(body, &add))
	assert.True(t, add.Replaced)
	require.Len(t, fake.routeAdds, 2)
	got := fake.routeAdds[0]
	assert.Equal(t, rig.srv.sr.GetRunId(), got.GetMeta().GetRunId())
	assert.NotZero(t, got.GetMeta().GetCallSeq())
	assert.Equal(t, "design", got.GetTopic())
	assert.Equal(t, "c-9", got.GetTo())
	assert.Equal(t, "layout", got.GetNote())

	code, body = rig.post(t, sandboxsock.PathSubagentRouteRm, sandboxsock.SubagentRouteRmRequest{Topic: "design"})
	require.Equal(t, http.StatusOK, code, string(body))
	require.Len(t, fake.routeRms, 1)
	assert.Equal(t, "design", fake.routeRms[0].GetTopic())
	assert.Equal(t, rig.srv.sr.GetRunId(), fake.routeRms[0].GetMeta().GetRunId())

	resp, err := rig.client.Get("http://asgard" + sandboxsock.PathSubagentRouteList + "?chat_id=evil")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list sandboxsock.SubagentRouteListResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	assert.Equal(t, []sandboxsock.SubagentRouteInfo{
		{Topic: "design", To: "c-9", Agent: "architect", Label: "arch", State: "running", DefinedAt: "c-1", Note: "layout"},
		{Topic: "user", To: "c-1", Agent: "lead", State: "idle", DefinedAt: "(default)", Self: true},
	}, list.Routes)
	require.Len(t, fake.routeLists, 1)
	assert.Equal(t, rig.srv.sr.GetRunId(), fake.routeLists[0].GetMeta().GetRunId())
}

func TestSubagentRouteRejectsBadRequestsAndMapsCodes(t *testing.T) {
	fake := &fakeCallbacks{}
	rig := newCallbackRig(t, context.Background(), fake, nil)
	for name, tt := range map[string]struct {
		path string
		body any
	}{
		"add without topic":   {sandboxsock.PathSubagentRouteAdd, sandboxsock.SubagentRouteAddRequest{To: "c"}},
		"add without target":  {sandboxsock.PathSubagentRouteAdd, sandboxsock.SubagentRouteAddRequest{Topic: "t"}},
		"add oversized topic": {sandboxsock.PathSubagentRouteAdd, sandboxsock.SubagentRouteAddRequest{Topic: string(make([]byte, 100)), To: "c"}},
		"add oversized note":  {sandboxsock.PathSubagentRouteAdd, sandboxsock.SubagentRouteAddRequest{Topic: "t", To: "c", Note: string(make([]byte, 300))}},
		"rm without topic":    {sandboxsock.PathSubagentRouteRm, sandboxsock.SubagentRouteRmRequest{}},
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := rig.post(t, tt.path, tt.body)
			assert.Equal(t, http.StatusBadRequest, code)
		})
	}
	assert.Empty(t, fake.routeAdds)
	assert.Empty(t, fake.routeRms)

	fake = &fakeCallbacks{subErr: status.Error(codes.NotFound, "chat is not in your spawn tree")}
	rig = newCallbackRig(t, context.Background(), fake, nil)
	code, body := rig.post(t, sandboxsock.PathSubagentRouteAdd, sandboxsock.SubagentRouteAddRequest{Topic: "t", To: "c"})
	assert.Equal(t, http.StatusNotFound, code)
	var e sandboxsock.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &e))
	assert.Equal(t, "chat is not in your spawn tree", e.Error)
	assert.Len(t, fake.routeAdds, 1, "a permanent backend answer is not retried")
}
