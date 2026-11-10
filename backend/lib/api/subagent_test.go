package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func teamAgent(id, team string) *agentspec.Agent {
	return &agentspec.Agent{Config: agentspec.AgentConfig{ID: id, Name: id, Team: team}}
}

// blockingRuns makes every started agent run block until its context ends.
func blockingRuns(rig *callbackRig) (started chan string, models *sync.Map) {
	started = make(chan string, 16)
	models = &sync.Map{}
	rig.server.runSingleAgentFn = func(ctx context.Context, _ *agentspec.Agent, chatID string, req TriggerMessageRequest) (string, string, error) {
		models.Store(chatID, req.Model)
		started <- chatID
		<-ctx.Done()
		return "failed", "", ctx.Err()
	}
	return started, models
}

func spawnReq(target string) SpawnSubagentRequest {
	return SpawnSubagentRequest{CallerChat: rigChat, CallerAgent: "alpha", TargetAgent: target, Label: "work", Prompt: "do it"}
}

func waitStarted(t *testing.T, started chan string) string {
	t.Helper()
	select {
	case id := <-started:
		return id
	case <-time.After(5 * time.Second):
		t.Fatal("subagent never started")
		return ""
	}
}

func TestSpawnSubagentCreatesChildChat(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	started, models := blockingRuns(rig)
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "alpha", RunDir: "/work/repo", RunnerNodeID: "", UserSubject: "u1"}))

	req := spawnReq("beta")
	req.Model = "flash"
	req.Readonly = true
	id, err := rig.server.SpawnSubagent(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, id, waitStarted(t, started))
	m, _ := models.Load(id)
	assert.Equal(t, "flash", m)

	child, err := rig.repo.GetSessionMeta(id)
	require.NoError(t, err)
	assert.Equal(t, rigChat, child.ParentChatID)
	assert.Equal(t, "alpha", child.SpawnedByAgent)
	assert.Equal(t, "work", child.SpawnLabel)
	assert.Equal(t, "beta", child.CurrentAgent)
	assert.Equal(t, "/work/repo", child.RunDir)
	assert.Equal(t, "u1", child.UserSubject)
	assert.True(t, child.SpawnReadonly)
	assert.Equal(t, 2, child.SpawnDepth)

	list, err := rig.server.ListSubagents(rigChat)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, SubagentStateRunning, list[0].State)
	assert.Equal(t, id, list[0].ID)

	n, err := rig.server.KillSubagent(rigChat, id)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	require.Eventually(t, func() bool {
		st, err := rig.server.SubagentStatus(rigChat, id)
		return err == nil && st.State == SubagentStateKilled
	}, 5*time.Second, 10*time.Millisecond)
}

func TestSpawnSubagentTeamRestriction(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"), teamAgent("other", "x"))
	blockingRuns(rig)

	_, err := rig.server.SpawnSubagent(context.Background(), spawnReq("other"))
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentDenied, se.Kind)

	_, err = rig.server.SpawnSubagent(context.Background(), spawnReq("missing"))
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentDenied, se.Kind)
}

func TestSpawnSubagentConcurrencyCap(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	started, _ := blockingRuns(rig)

	var ids []string
	for i := 0; i < maxRunningSubagents; i++ {
		id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
		require.NoError(t, err)
		waitStarted(t, started)
		ids = append(ids, id)
	}
	_, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentLimit, se.Kind)

	// Finishing one frees a slot.
	_, err = rig.server.KillSubagent(rigChat, ids[0])
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, running := rig.server.activeExecutions.Load(ids[0])
		return !running
	}, 5*time.Second, 10*time.Millisecond)
	_, err = rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
}

func TestSubagentDepthSharedWithCallPeer(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"), teamAgent("gamma", "t"))
	started, _ := blockingRuns(rig)

	// Ancestry alone: root (1) -> child (2) -> grandchild (3) -> great (4) -> refused.
	// An agent cannot spawn itself (it is not its own teammate), so alternate.
	parent, cur := rigChat, "alpha"
	for depth := 2; depth <= maxAgentNestingDepth; depth++ {
		next := map[string]string{"alpha": "beta", "beta": "gamma", "gamma": "beta"}[cur]
		req := spawnReq(next)
		req.CallerChat = parent
		id, err := rig.server.SpawnSubagent(context.Background(), req)
		require.NoError(t, err, "depth %d", depth)
		waitStarted(t, started)
		sess, _ := rig.repo.GetSessionMeta(id)
		assert.Equal(t, depth, sess.SpawnDepth)
		parent, cur = id, next
	}
	req := spawnReq("alpha")
	req.CallerChat = parent
	_, err := rig.server.SpawnSubagent(context.Background(), req)
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentLimit, se.Kind)

	// The ancestry counts toward call-peer too: the chat of the deepest agent
	// has no room for a peer call.
	assert.ErrorContains(t, rig.server.enterPeerCall(parent, cur, "alpha"), "too deep")

	// A chat with two agents above it has room for one peer (2 + 1 + 1 = 4).
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: "chat-mid", CurrentAgent: "alpha", SpawnDepth: 3}))
	require.NoError(t, rig.server.enterPeerCall("chat-mid", "alpha", "beta"))
	assert.ErrorContains(t, rig.server.enterPeerCall("chat-mid", "beta", "gamma"), "too deep")
	rig.server.leavePeerCall("chat-mid")
}

func TestSubagentDepthCountsPeerChainAtSpawn(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"), teamAgent("gamma", "t"), teamAgent("delta", "t"))
	blockingRuns(rig)
	require.NoError(t, rig.server.enterPeerCall(rigChat, "alpha", "beta"))
	require.NoError(t, rig.server.enterPeerCall(rigChat, "beta", "gamma"))
	require.NoError(t, rig.server.enterPeerCall(rigChat, "gamma", "delta"))
	// Chain of four agents: a child would be the fifth.
	req := spawnReq("beta")
	req.CallerAgent = "delta"
	_, err := rig.server.SpawnSubagent(context.Background(), req)
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentLimit, se.Kind)
	rig.server.leavePeerCall(rigChat)
}

func TestSubagentOwnership(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	started, _ := blockingRuns(rig)
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)

	// An unrelated chat can neither see nor stop it.
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: "chat-other", CurrentAgent: "alpha"}))
	var se *SubagentError
	_, err = rig.server.SubagentStatus("chat-other", id)
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentNotFound, se.Kind)
	_, err = rig.server.KillSubagent("chat-other", id)
	require.ErrorAs(t, err, &se)
	_, running := rig.server.activeExecutions.Load(id)
	assert.True(t, running)
	list, err := rig.server.ListSubagents("chat-other")
	require.NoError(t, err)
	assert.Empty(t, list)

	// A child cannot act on its parent or siblings.
	_, err = rig.server.SubagentStatus(id, rigChat)
	require.ErrorAs(t, err, &se)

	// An ancestor reaches a grandchild.
	grand := &dbmodels.Session{ChatID: "chat-grand", CurrentAgent: "beta", ParentChatID: id}
	require.NoError(t, rig.repo.SaveSession(grand))
	_, err = rig.server.SubagentStatus(rigChat, "chat-grand")
	assert.NoError(t, err)
}

func TestStopCascadesToDescendants(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"), teamAgent("gamma", "t"))
	started, _ := blockingRuns(rig)
	holdChat(t, rig)

	child, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	req := spawnReq("gamma")
	req.CallerChat = child
	grand, err := rig.server.SpawnSubagent(context.Background(), req)
	require.NoError(t, err)
	waitStarted(t, started)

	code, msg := rig.server.stopSessionExecution(rigChat)
	require.Equal(t, 200, code, msg)
	for _, id := range []string{child, grand} {
		require.Eventually(t, func() bool {
			_, running := rig.server.activeExecutions.Load(id)
			return !running
		}, 5*time.Second, 10*time.Millisecond, id)
	}
}

func TestKillSubagentCascadesAndStopIdleParent(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"), teamAgent("gamma", "t"))
	started, _ := blockingRuns(rig)

	child, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	req := spawnReq("gamma")
	req.CallerChat = child
	grand, err := rig.server.SpawnSubagent(context.Background(), req)
	require.NoError(t, err)
	waitStarted(t, started)

	n, err := rig.server.KillSubagent(rigChat, child)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	for _, id := range []string{child, grand} {
		require.Eventually(t, func() bool {
			_, running := rig.server.activeExecutions.Load(id)
			return !running
		}, 5*time.Second, 10*time.Millisecond, id)
	}

	// The killer hears of the kill from the CLI, not from a push: its idle
	// chat is not restarted.
	select {
	case id := <-started:
		t.Fatalf("%s started after the kill", id)
	case <-time.After(150 * time.Millisecond):
	}
	// Nothing left to stop: the idle parent keeps its 409.
	code, _ := rig.server.stopSessionExecution(rigChat)
	assert.Equal(t, 409, code)
}

func TestStopIdleParentStopsRunningChildren(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	started, _ := blockingRuns(rig)
	child, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)

	code, msg := rig.server.stopSessionExecution(rigChat)
	assert.Equal(t, 200, code, msg)
	require.Eventually(t, func() bool {
		_, running := rig.server.activeExecutions.Load(child)
		return !running
	}, 5*time.Second, 10*time.Millisecond)
}

func TestKilledOutcomeIsDurableAndClearedOnRerun(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	started, _ := blockingRuns(rig)
	id, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	_, err = rig.server.KillSubagent(rigChat, id)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, running := rig.server.activeExecutions.Load(id)
		return !running
	}, 5*time.Second, 10*time.Millisecond)

	// The state comes from the database row, not process memory, and a stop
	// leaves no per-chat marker behind.
	sess, err := rig.repo.GetSessionMeta(id)
	require.NoError(t, err)
	assert.Equal(t, dbmodels.SpawnOutcomeKilled, sess.SpawnOutcome)
	assert.Equal(t, SubagentStateKilled, rig.server.subagentInfo(sess).State)
	assert.False(t, rig.server.sessionStopInProgress(id))

	// A chat that is not a subagent never gets an outcome.
	require.NoError(t, rig.repo.SetSpawnOutcome(rigChat, dbmodels.SpawnOutcomeKilled))
	root, _ := rig.repo.GetSessionMeta(rigChat)
	assert.Empty(t, root.SpawnOutcome)

	require.NoError(t, rig.repo.SetSpawnOutcome(id, ""))
	sess, _ = rig.repo.GetSessionMeta(id)
	assert.Equal(t, SubagentStateDone, rig.server.subagentInfo(sess).State)
}

func TestSubagentStatusFailedAndDone(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	rig.server.runSingleAgentFn = func(_ context.Context, _ *agentspec.Agent, chatID string, _ TriggerMessageRequest) (string, string, error) {
		_ = rig.repo.AppendMessage(chatID, dbmodels.ChatMessage{ID: "a-" + chatID, Role: "assistant", Content: "all done", Timestamp: 1})
		return "completed", "all done", nil
	}
	settled := func(id string) {
		require.Eventually(t, func() bool {
			_, running := rig.server.activeExecutions.Load(id)
			return !running
		}, 5*time.Second, 10*time.Millisecond)
	}

	good, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	settled(good)
	st, err := rig.server.SubagentStatus(rigChat, good)
	require.NoError(t, err)
	assert.Equal(t, SubagentStateDone, st.State)
	assert.Equal(t, "all done", st.Summary)

	// Every run from now on fails.
	rig.server.runSingleAgentFn = func(context.Context, *agentspec.Agent, string, TriggerMessageRequest) (string, string, error) {
		return "failed", "", &agentRunError{Err: errors.New("sandbox exploded")}
	}
	bad, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	settled(bad)
	require.Eventually(t, func() bool {
		st, err := rig.server.SubagentStatus(rigChat, bad)
		return err == nil && st.State == SubagentStateFailed
	}, 5*time.Second, 10*time.Millisecond)
	st, _ = rig.server.SubagentStatus(rigChat, bad)
	assert.Contains(t, st.Summary, "sandbox exploded")

	// List and status agree.
	list, err := rig.server.ListSubagents(rigChat)
	require.NoError(t, err)
	states := map[string]string{}
	for _, in := range list {
		states[in.ID] = in.State
	}
	assert.Equal(t, SubagentStateDone, states[good])
	assert.Equal(t, SubagentStateFailed, states[bad])
}

func TestSpawnSubagentGuardRejections(t *testing.T) {
	var se *SubagentError
	wf := &agentspec.Agent{Config: agentspec.AgentConfig{ID: "flow", Name: "flow", Team: "t", Type: "workflow"}}
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"), wf)
	blockingRuns(rig)

	req := spawnReq("beta")
	req.CallerChat = "chat-unknown"
	_, err := rig.server.SpawnSubagent(context.Background(), req)
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentNotFound, se.Kind)

	_, err = rig.server.SpawnSubagent(context.Background(), spawnReq("flow"))
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentInvalid, se.Kind)

	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: "chat-wt", CurrentAgent: "alpha", Worktree: true}))
	req = spawnReq("beta")
	req.CallerChat = "chat-wt"
	_, err = rig.server.SpawnSubagent(context.Background(), req)
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentInvalid, se.Kind)
	assert.Contains(t, se.Msg, "worktree")

	list, err := rig.server.ListSubagents("chat-wt")
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestSpawnSubagentFailureRemovesChildRow(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	blockingRuns(rig)
	rig.server.runners = fakeRunnerRegistry{nodes: []runnerhub.NodeInfo{{NodeID: "runner-off", Online: false}}}
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "alpha", RunnerNodeID: "runner-off"}))

	_, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentUnavailable, se.Kind)
	ids, err := rig.repo.ChildChatIDs(rigChat)
	require.NoError(t, err)
	assert.Empty(t, ids, "no orphan child row after a failed spawn")
}

func TestSpawnRefusedWhileCallerIsBeingStopped(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	blockingRuns(rig)
	holdChat(t, rig)
	rig.server.cancelledExecutions.Store(rigChat, time.Now())
	_, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentDenied, se.Kind)
	ids, _ := rig.repo.ChildChatIDs(rigChat)
	assert.Empty(t, ids)
}

func TestChatBaseDepthFailsClosed(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	blockingRuns(rig)
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: "chat-deep", CurrentAgent: "alpha", SpawnDepth: 4}))
	base, err := rig.server.chatBaseDepth("chat-deep")
	require.NoError(t, err)
	assert.Equal(t, 3, base)

	// A broken lookup is an error, not depth 0.
	rig.server.repo = dbmodels.NewSessionRepository(db.NewDBForTest(t)) // no tables
	_, err = rig.server.chatBaseDepth("chat-deep")
	assert.Error(t, err)
	assert.Error(t, rig.server.enterPeerCall("chat-deep", "alpha", "beta"))
	_, err = rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	assert.Error(t, err)
}

func worktreeTeamAgent(id string) *agentspec.Agent {
	a := teamAgent(id, "t")
	a.Config.Worktree = agentspec.WorktreeConfig{Enabled: true, Root: "/wt"}
	return a
}

// A subagent of a worktree chat shares the worktree of the chat that opted in,
// however deep it is nested.
func TestSpawnSubagentSharesParentWorktree(t *testing.T) {
	rig := newCallbackRig(t, worktreeTeamAgent("alpha"), worktreeTeamAgent("beta"), worktreeTeamAgent("gamma"), teamAgent("plain", "t"))
	started, _ := blockingRuns(rig)
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "alpha", RunDir: "/work/repo", Worktree: true}))

	req := spawnReq("beta")
	req.Readonly = true
	childID, err := rig.server.SpawnSubagent(context.Background(), req)
	require.NoError(t, err)
	waitStarted(t, started)
	child, err := rig.repo.GetSessionMeta(childID)
	require.NoError(t, err)
	assert.Equal(t, rigChat, child.WorktreeChatID)
	assert.False(t, child.Worktree, "the child does not opt in to a worktree of its own")

	// A grandchild resolves to the root chat, not to its parent.
	req = spawnReq("gamma")
	req.CallerChat = childID
	req.CallerAgent = "beta"
	grandID, err := rig.server.SpawnSubagent(context.Background(), req)
	require.NoError(t, err)
	waitStarted(t, started)
	grand, err := rig.repo.GetSessionMeta(grandID)
	require.NoError(t, err)
	assert.Equal(t, rigChat, grand.WorktreeChatID)

	// An agent that cannot work in a worktree is refused, not run in the main checkout.
	req = spawnReq("plain")
	var se *SubagentError
	_, err = rig.server.SpawnSubagent(context.Background(), req)
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentInvalid, se.Kind)
	assert.Contains(t, se.Msg, "worktree")
}

// A target whose worktree root differs from the owner's would derive another
// path than the owner's live worktree: refused at spawn. The root is persisted
// on the child (and on grandchildren) from the owner.
func TestSpawnSubagentWorktreeRoot(t *testing.T) {
	other := worktreeTeamAgent("other")
	other.Config.Worktree.Root = "/wt-b"
	rig := newCallbackRig(t, worktreeTeamAgent("alpha"), worktreeTeamAgent("beta"), worktreeTeamAgent("gamma"), other)
	started, _ := blockingRuns(rig)
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{ChatID: rigChat, CurrentAgent: "alpha", RunDir: "/work/repo", Worktree: true}))

	var se *SubagentError
	_, err := rig.server.SpawnSubagent(context.Background(), spawnReq("other"))
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentInvalid, se.Kind)
	assert.Contains(t, se.Msg, "/wt-b")

	childID, err := rig.server.SpawnSubagent(context.Background(), spawnReq("beta"))
	require.NoError(t, err)
	waitStarted(t, started)
	child, err := rig.repo.GetSessionMeta(childID)
	require.NoError(t, err)
	assert.Equal(t, "/wt", child.WorktreeRoot)

	// The grandchild inherits the persisted root, and a differing agent is refused.
	req := spawnReq("other")
	req.CallerChat, req.CallerAgent = childID, "beta"
	_, err = rig.server.SpawnSubagent(context.Background(), req)
	require.ErrorAs(t, err, &se)
	req = spawnReq("gamma")
	req.CallerChat, req.CallerAgent = childID, "beta"
	grandID, err := rig.server.SpawnSubagent(context.Background(), req)
	require.NoError(t, err)
	waitStarted(t, started)
	grand, err := rig.repo.GetSessionMeta(grandID)
	require.NoError(t, err)
	assert.Equal(t, "/wt", grand.WorktreeRoot)
}

func TestSubagentStateQueuedWhileWaitingForCheckoutLock(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	rig.server.dirLocks = dirlock.NewManager(nil)
	ctx := t.Context()

	release, err := rig.server.dirLocks.Acquire(ctx, dirlock.AcquireRequest{ChatID: rigChat, Dirs: []string{"/work/repo"}, Mode: dirlock.ModeExclusive})
	require.NoError(t, err)

	child := &dbmodels.Session{ChatID: "child-w", ParentChatID: rigChat, CurrentAgent: "beta", RunDir: "/work/repo"}
	ro := &dbmodels.Session{ChatID: "child-r", ParentChatID: rigChat, CurrentAgent: "beta", RunDir: "/work/repo", SpawnReadonly: true}
	rig.server.activeExecutions.Store(child.ChatID, struct{}{})
	rig.server.activeExecutions.Store(ro.ChatID, struct{}{})
	// Guard taken but not asking for the lock yet: running.
	assert.Equal(t, SubagentStateRunning, rig.server.subagentInfo(child).State)

	got := make(chan func(bool), 1)
	go func() {
		rel, err := rig.server.dirLocks.Acquire(ctx, dirlock.AcquireRequest{ChatID: child.ChatID, Dirs: []string{"/work/repo"}, Mode: dirlock.ModeExclusive, Ancestors: []string{rigChat}})
		if err == nil {
			got <- rel
		}
	}()
	require.Eventually(t, func() bool {
		st := rig.server.dirLocks.StatusForChat(child.ChatID)
		return st != nil && st.IsWaitingForLock
	}, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, SubagentStateQueued, rig.server.subagentInfo(child).State)
	assert.Equal(t, SubagentStateRunning, rig.server.subagentInfo(ro).State, "a readonly child takes no lock")

	// The parent run ends: the child gets the lock and runs.
	release(false)
	rel := <-got
	assert.Equal(t, SubagentStateRunning, rig.server.subagentInfo(child).State)
	rel(false)
}

func TestSpawnRootAndSharedChat(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	mid := &dbmodels.Session{ChatID: "mid", ParentChatID: "top", CurrentAgent: "beta"}
	leaf := &dbmodels.Session{ChatID: "leaf", ParentChatID: "mid", CurrentAgent: "beta"}
	top := &dbmodels.Session{ChatID: "top", CurrentAgent: "alpha"}
	for _, s := range []*dbmodels.Session{top, mid, leaf} {
		require.NoError(t, rig.repo.SaveSession(s))
	}
	root, err := rig.server.spawnRoot(leaf)
	require.NoError(t, err)
	assert.Equal(t, "top", root)
	root, err = rig.server.spawnRoot(top)
	require.NoError(t, err)
	assert.Equal(t, "top", root)

	teamExec := &SingleAgentExecutor{server: rig.server, agent: teamAgent("alpha", "t")}
	got, err := teamExec.sharedChat(leaf)
	require.NoError(t, err)
	assert.Equal(t, "top", got, "a descendant shares the root's directory")
	got, err = teamExec.sharedChat(top)
	require.NoError(t, err)
	assert.Equal(t, "top", got, "a team agent's root chat gets its own")

	soloExec := &SingleAgentExecutor{server: rig.server, agent: teamAgent("solo", "")}
	got, err = soloExec.sharedChat(top)
	require.NoError(t, err)
	assert.Empty(t, got, "a plain chat keeps its sandbox unchanged")
}
