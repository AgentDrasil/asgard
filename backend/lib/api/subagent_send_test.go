package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
)

func sendRig(t *testing.T, inject func(string) (bool, error)) (*callbackRig, chan string, *fakeInjector, *dispatch.ActiveRuns) {
	t.Helper()
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	started, _ := blockingRuns(rig)
	inj := &fakeInjector{fn: inject}
	runs := dispatch.NewActiveRuns()
	rig.server.injector = inj
	rig.server.activeRuns = runs
	return rig, started, inj, runs
}

func waitIdle(t *testing.T, rig *callbackRig, id string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, running := rig.server.activeExecutions.Load(id)
		return !running
	}, 5*time.Second, 10*time.Millisecond)
}

func TestSendToSubagentInjectsIntoRunningChild(t *testing.T) {
	rig, started, inj, runs := sendRig(t, func(string) (bool, error) { return true, nil })
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	runs.Register(id, "beta", "node", "run-child")

	got, err := rig.server.SendToSubagent(context.Background(), rigChat, "alpha", id, "change of plan")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryInjected, got)
	assert.Equal(t, []string{"[message from parent agent alpha]\nchange of plan"}, inj.got)

	full, err := rig.repo.GetSession(id)
	require.NoError(t, err)
	last := full.Messages[len(full.Messages)-1]
	assert.Equal(t, "user", last.Role)
	assert.Contains(t, last.Content, "change of plan")
}

func TestSendToSubagentQueuesWhenCLICannotInject(t *testing.T) {
	// opencode and simplest: the hub reports injected=false.
	rig, started, inj, runs := sendRig(t, func(string) (bool, error) { return false, nil })
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	runs.Register(id, "beta", "node", "run-child")

	got, err := rig.server.SendToSubagent(context.Background(), rigChat, "alpha", id, "more work")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryQueued, got)
	assert.EqualValues(t, 1, inj.calls.Load())
	q, err := rig.repo.GetQueuedMessages(id)
	require.NoError(t, err)
	require.Len(t, q, 1)
	assert.Equal(t, "[message from parent agent alpha]\nmore work", q[0].Prompt)
}

func TestSendToSubagentQueueFullIsLimit(t *testing.T) {
	rig, started, _, _ := sendRig(t, func(string) (bool, error) { return false, nil })
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	for i := 0; i < dbmodels.MaxQueuedMessagesPerSession; i++ {
		got, err := rig.server.SendToSubagent(context.Background(), rigChat, "alpha", id, "m")
		require.NoError(t, err)
		assert.Equal(t, SubagentDeliveryQueued, got)
	}
	_, err = rig.server.SendToSubagent(context.Background(), rigChat, "alpha", id, "one too many")
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentLimit, se.Kind)
}

func TestSendToSubagentStartsIdleChildAndClearsKilled(t *testing.T) {
	rig, started, inj, _ := sendRig(t, func(string) (bool, error) { return true, nil })
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	_, err = rig.server.KillSubagent(rigChat, id)
	require.NoError(t, err)
	waitIdle(t, rig, id)
	sess, _ := rig.repo.GetSessionMeta(id)
	require.Equal(t, dbmodels.SpawnOutcomeKilled, sess.SpawnOutcome)

	got, err := rig.server.SendToSubagent(context.Background(), rigChat, "alpha", id, "try again")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryStarted, got)
	assert.Zero(t, inj.calls.Load(), "nothing running to inject into")
	assert.Equal(t, id, waitStarted(t, started))
	info, err := rig.server.SubagentStatus(rigChat, id)
	require.NoError(t, err)
	assert.Equal(t, SubagentStateRunning, info.State)
}

func TestSendToSubagentRestartCountsTowardCap(t *testing.T) {
	rig, started, _, _ := sendRig(t, func(string) (bool, error) { return true, nil })
	first, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	_, err = rig.server.KillSubagent(rigChat, first)
	require.NoError(t, err)
	waitIdle(t, rig, first)
	for i := 0; i < maxRunningSubagents; i++ {
		_, err = rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
		require.NoError(t, err)
		waitStarted(t, started)
	}
	_, err = rig.server.SendToSubagent(context.Background(), rigChat, "alpha", first, "restart")
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentLimit, se.Kind)
}

func TestSendToSubagentOwnershipAndValidation(t *testing.T) {
	rig, started, inj, _ := sendRig(t, func(string) (bool, error) { return true, nil })
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: "chat-other", CurrentAgent: "alpha"}))
	foreign := &dbmodels.Session{ChatID: "chat-foreign-child", CurrentAgent: "beta", ParentChatID: "chat-other"}
	require.NoError(t, rig.repo.SaveSession(foreign))

	var se *SubagentError
	_, err := rig.server.SendToSubagent(context.Background(), rigChat, "alpha", foreign.ChatID, "hi")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentNotFound, se.Kind)
	_, err = rig.server.SendToSubagent(context.Background(), rigChat, "alpha", "chat-nope", "hi")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentNotFound, se.Kind)
	// A child cannot message its parent through send.
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	_, err = rig.server.SendToSubagent(context.Background(), id, "beta", rigChat, "hi")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentNotFound, se.Kind)
	_, err = rig.server.SendToSubagent(context.Background(), rigChat, "alpha", id, "  ")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentInvalid, se.Kind)
	assert.Zero(t, inj.calls.Load())
}

func TestSendToSubagentDeniedWhenCallerStoppedMidSend(t *testing.T) {
	rig, started, _, runs := sendRig(t, func(string) (bool, error) { return false, nil })
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	runs.Register(id, "beta", "node", "run-child")

	// The caller's run ends while the inject is pending.
	ctx, cancel := context.WithCancel(context.Background())
	rig.server.injector = &fakeInjector{fn: func(string) (bool, error) {
		cancel()
		return false, runnerhub.ErrUnknownRun
	}}
	_, err = rig.server.SendToSubagent(ctx, rigChat, "alpha", id, "too late")
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentDenied, se.Kind)
	q, err := rig.repo.GetQueuedMessages(id)
	require.NoError(t, err)
	assert.Empty(t, q, "nothing queued for the child")

	// An idle child is not restarted either.
	_, err = rig.server.KillSubagent(rigChat, id)
	require.NoError(t, err)
	waitIdle(t, rig, id)
	_, err = rig.server.SendToSubagent(ctx, rigChat, "alpha", id, "still too late")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentDenied, se.Kind)
	_, running := rig.server.activeExecutions.Load(id)
	assert.False(t, running, "child not restarted")
	select {
	case <-started:
		t.Fatal("child was restarted")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSpawnSubagentDeniedWhenCallerRunEnded(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	blockingRuns(rig)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := rig.server.SpawnSubagent(ctx, spawnReq("beta"))
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentDenied, se.Kind)
	ids, err := rig.repo.ChildChatIDs(rigChat)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestSendToSubagentRunnerOfflineIsUnavailable(t *testing.T) {
	rig, started, _, _ := sendRig(t, func(string) (bool, error) { return true, nil })
	rig.server.runners = fakeRunnerRegistry{nodes: []runnerhub.NodeInfo{{NodeID: "runner-x", Online: true}}}
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "alpha", RunnerNodeID: "runner-x"}))
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	_, err = rig.server.KillSubagent(rigChat, id)
	require.NoError(t, err)
	waitIdle(t, rig, id)

	rig.server.runners = fakeRunnerRegistry{nodes: []runnerhub.NodeInfo{{NodeID: "runner-x", Online: false}}}
	_, err = rig.server.SendToSubagent(context.Background(), rigChat, "alpha", id, "hello?")
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentUnavailable, se.Kind)
}

func TestSendToSubagentUnknownRunWhileGuardHeldQueues(t *testing.T) {
	// The run vanished between Lookup and Inject, but the child's guard is
	// still held: the message waits in the queue, no second turn starts.
	rig, started, _, runs := sendRig(t, func(string) (bool, error) { return false, runnerhub.ErrUnknownRun })
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	runs.Register(id, "beta", "node", "run-child")

	got, err := rig.server.SendToSubagent(context.Background(), rigChat, "alpha", id, "next")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryQueued, got)
	q, err := rig.repo.GetQueuedMessages(id)
	require.NoError(t, err)
	require.Len(t, q, 1)
	select {
	case <-started:
		t.Fatal("a second turn was started")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSendToSubagentSucceedsWhenRecordingFails(t *testing.T) {
	rig, started, _, runs := sendRig(t, func(string) (bool, error) { return true, nil })
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	runs.Register(id, "beta", "node", "run-child")

	// Make appending to the transcript fail: the session dir is below a file.
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	rig.repo.SetSessionDirFunc(func(chatID string) string { return filepath.Join(blocker, chatID) })

	got, err := rig.server.SendToSubagent(context.Background(), rigChat, "alpha", id, "hi")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryInjected, got)
}

func TestSendToSubagentInjectErrorQueues(t *testing.T) {
	rig, started, _, runs := sendRig(t, func(string) (bool, error) { return false, errors.New("boom") })
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	runs.Register(id, "beta", "node", "run-child")
	got, err := rig.server.SendToSubagent(context.Background(), rigChat, "alpha", id, "x")
	require.NoError(t, err)
	assert.Equal(t, SubagentDeliveryQueued, got)
}
