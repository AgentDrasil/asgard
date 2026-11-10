package api

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func TestCallbackSubagentLifecycleUsesRunIdentity(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	started, models := blockingRuns(rig)
	ctx := rig.ctx()

	sp, err := rig.svc.SpawnSubagent(ctx, &runnerpb.SpawnSubagentRequest{
		Meta: meta(rigRun, 1), AgentId: "beta", Model: "flash", Readonly: true, Label: "scan", Prompt: "look around",
	})
	require.NoError(t, err)
	assert.Equal(t, sp.GetId(), waitStarted(t, started))
	m, _ := models.Load(sp.GetId())
	assert.Equal(t, "flash", m)

	// The child hangs below the run's chat and is spawned by the run's agent.
	child, err := rig.repo.GetSessionMeta(sp.GetId())
	require.NoError(t, err)
	assert.Equal(t, rigChat, child.ParentChatID)
	assert.Equal(t, "alpha", child.SpawnedByAgent)
	assert.True(t, child.SpawnReadonly)

	// A retry of the same call does not spawn a second child.
	again, err := rig.svc.SpawnSubagent(ctx, &runnerpb.SpawnSubagentRequest{Meta: meta(rigRun, 1), AgentId: "beta", Prompt: "look around"})
	require.NoError(t, err)
	assert.Equal(t, sp.GetId(), again.GetId())

	list, err := rig.svc.ListSubagents(ctx, &runnerpb.ListSubagentsRequest{Meta: meta(rigRun, 2)})
	require.NoError(t, err)
	require.Len(t, list.GetSubagents(), 1)
	got := list.GetSubagents()[0]
	assert.Equal(t, sp.GetId(), got.GetId())
	assert.Equal(t, rigChat, got.GetParentChatId())
	assert.Equal(t, "scan", got.GetLabel())
	assert.Equal(t, "beta", got.GetAgent())
	assert.Equal(t, "alpha", got.GetSpawnedBy())
	assert.True(t, got.GetReadonly())
	assert.Equal(t, SubagentStateRunning, got.GetState())
	assert.InDelta(t, time.Now().UnixMilli(), got.GetLastActivityUnixMs(), float64(time.Minute.Milliseconds()))

	st, err := rig.svc.SubagentStatus(ctx, &runnerpb.SubagentStatusRequest{Meta: meta(rigRun, 3), Id: sp.GetId()})
	require.NoError(t, err)
	assert.Equal(t, sp.GetId(), st.GetSubagent().GetId())

	kill, err := rig.svc.KillSubagent(ctx, &runnerpb.KillSubagentRequest{Meta: meta(rigRun, 4), Id: sp.GetId()})
	require.NoError(t, err)
	assert.Equal(t, int32(1), kill.GetStopped())
}

func TestCallbackSubagentIgnoresOtherChats(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	started, _ := blockingRuns(rig)

	// A child of another chat, as another agent would have spawned it.
	other := &dbmodels.Session{ChatID: "chat-other", CurrentAgent: "alpha"}
	require.NoError(t, rig.repo.SaveSession(other))
	foreign := &dbmodels.Session{ChatID: "chat-foreign-child", CurrentAgent: "beta", ParentChatID: "chat-other", SpawnedByAgent: "alpha"}
	require.NoError(t, rig.repo.SaveSession(foreign))

	ctx := rig.ctx()
	list, err := rig.svc.ListSubagents(ctx, &runnerpb.ListSubagentsRequest{Meta: meta(rigRun, 1)})
	require.NoError(t, err)
	assert.Empty(t, list.GetSubagents())

	_, err = rig.svc.SubagentStatus(ctx, &runnerpb.SubagentStatusRequest{Meta: meta(rigRun, 2), Id: foreign.ChatID})
	assert.Equal(t, codes.NotFound, status.Code(err))
	_, err = rig.svc.KillSubagent(ctx, &runnerpb.KillSubagentRequest{Meta: meta(rigRun, 3), Id: foreign.ChatID})
	assert.Equal(t, codes.NotFound, status.Code(err))

	// Spawning lands below the run's chat; there is no request field to say otherwise.
	sp, err := rig.svc.SpawnSubagent(ctx, &runnerpb.SpawnSubagentRequest{Meta: meta(rigRun, 4), AgentId: "beta", Prompt: "x"})
	require.NoError(t, err)
	waitStarted(t, started)
	child, err := rig.repo.GetSessionMeta(sp.GetId())
	require.NoError(t, err)
	assert.Equal(t, rigChat, child.ParentChatID)
}

func TestCallbackSubagentRequiresActiveRun(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	_, err := rig.svc.SpawnSubagent(rig.ctx(), &runnerpb.SpawnSubagentRequest{Meta: meta("no-such-run", 1), AgentId: "beta", Prompt: "x"})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = rig.svc.ListSubagents(rig.ctx(), &runnerpb.ListSubagentsRequest{Meta: &runnerpb.CallMeta{RunId: rigRun}})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCallbackSubagentErrorCodes(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"), teamAgent("other", "x"))
	started, _ := blockingRuns(rig)
	ctx := rig.ctx()
	seq := uint64(0)
	next := func() *runnerpb.CallMeta { seq++; return meta(rigRun, seq) }

	_, err := rig.svc.SpawnSubagent(ctx, &runnerpb.SpawnSubagentRequest{Meta: next(), AgentId: "beta", Prompt: "  "})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = rig.svc.SpawnSubagent(ctx, &runnerpb.SpawnSubagentRequest{Meta: next(), Prompt: "x"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = rig.svc.SubagentStatus(ctx, &runnerpb.SubagentStatusRequest{Meta: next(), Id: "../etc"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = rig.svc.SpawnSubagent(ctx, &runnerpb.SpawnSubagentRequest{Meta: next(), AgentId: "other", Prompt: "x"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	_, err = rig.svc.KillSubagent(ctx, &runnerpb.KillSubagentRequest{Meta: next(), Id: "chat-nope"})
	assert.Equal(t, codes.NotFound, status.Code(err))

	for i := 0; i < maxRunningSubagents; i++ {
		_, err = rig.svc.SpawnSubagent(ctx, &runnerpb.SpawnSubagentRequest{Meta: next(), AgentId: "beta", Prompt: "x"})
		require.NoError(t, err)
		waitStarted(t, started)
	}
	_, err = rig.svc.SpawnSubagent(ctx, &runnerpb.SpawnSubagentRequest{Meta: next(), AgentId: "beta", Prompt: "x"})
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
}

func TestSubagentStatusErrMapsEveryKind(t *testing.T) {
	tests := []struct {
		kind SubagentErrorKind
		want codes.Code
	}{
		{SubagentInvalid, codes.InvalidArgument},
		{SubagentNotFound, codes.NotFound},
		{SubagentDenied, codes.PermissionDenied},
		{SubagentLimit, codes.ResourceExhausted},
		// Not Unavailable: the runner retries that as a lost connection.
		{SubagentUnavailable, codes.FailedPrecondition},
		{SubagentInternal, codes.Internal},
	}
	for _, tt := range tests {
		err := subagentStatusErr(&SubagentError{Kind: tt.kind, Msg: "boom"})
		assert.Equal(t, tt.want, status.Code(err), "kind %d", tt.kind)
		assert.Equal(t, "boom", status.Convert(err).Message())
	}
	assert.Equal(t, codes.Internal, status.Code(subagentStatusErr(errors.New("plain"))))
}

func TestCallbackSubagentDegradedMode(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"))
	rig.server.repo = nil
	_, err := rig.svc.ListSubagents(rig.ctx(), &runnerpb.ListSubagentsRequest{Meta: meta(rigRun, 1)})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestCallbackWaitAndReplyUseRunIdentity(t *testing.T) {
	rig, c := reportRig(t)
	ctx := rig.ctx()
	child := spawnOne(t, rig, c)

	// Wait: a short timeout leaves the child running.
	w, err := rig.svc.WaitSubagents(ctx, &runnerpb.WaitSubagentsRequest{Meta: meta(rigRun, 1), Ids: []string{child}, TimeoutMs: 30})
	require.NoError(t, err)
	require.Len(t, w.GetResults(), 1)
	assert.True(t, w.GetResults()[0].GetStillRunning())
	assert.Equal(t, child, w.GetResults()[0].GetSubagent().GetId())

	_, err = rig.svc.WaitSubagents(ctx, &runnerpb.WaitSubagentsRequest{Meta: meta(rigRun, 2), Ids: []string{"nope"}})
	assert.Equal(t, codes.NotFound, status.Code(err))

	// Reply: the run's chat has no parent.
	_, err = rig.svc.ReplyToParent(ctx, &runnerpb.ReplyToParentRequest{Meta: meta(rigRun, 3), Text: "hi"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}
