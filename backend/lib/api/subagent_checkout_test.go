package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
	"github.com/AgentDrasil/asgard/backend/lib/worktree"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

type checkoutRig struct {
	server *Server
	repo   *dbmodels.SessionRepository
	agent  *agentspec.Agent
	reqs   chan dispatch.Request
	// wtPath is where the owner chat's worktree lives.
	wtPath string
}

func newCheckoutRig(t *testing.T) *checkoutRig {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)
	server.conf = &config.Config{}
	server.dirLocks = dirlock.NewManager(func(string) {})
	reqs := make(chan dispatch.Request, 8)
	server.dispatcher = &stubDispatcher{step: func(_ int, req dispatch.Request) (dispatch.Result, error) {
		reqs <- req
		return dispatch.Result{Output: []byte(`{"last_content":"ok","session_id":"s"}`), Target: agentspec.CLITarget{CLI: "opencode", Model: "m1"}}, nil
	}}
	agent := dispatchTestAgent()
	agent.Config.Worktree = agentspec.WorktreeConfig{Enabled: true, Root: "/wt"}
	wtPath, err := worktree.PathFor("/wt", "/work", "owner")
	require.NoError(t, err)
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: "owner", CurrentAgent: "da", RunDir: "/work", Worktree: true}))
	return &checkoutRig{server: server, repo: repo, agent: agent, reqs: reqs, wtPath: wtPath}
}

func (r *checkoutRig) child(t *testing.T, id, parent string, readonly bool) {
	t.Helper()
	require.NoError(t, r.repo.SaveSession(&dbmodels.Session{
		ChatID: id, CurrentAgent: "da", RunDir: "/work", ParentChatID: parent,
		WorktreeChatID: "owner", WorktreeRoot: "/wt", SpawnReadonly: readonly, SpawnDepth: 2,
	}))
}

func (r *checkoutRig) run(ctx context.Context, chat string) error {
	exec := NewSingleAgentExecutor(r.agent, r.server.conf, r.repo, r.server, nil)
	_, err := exec.Execute(ctx, SingleAgentRunParams{ChatID: chat, Prompt: "go"})
	return err
}

// holdOwner takes the lock the way the running owner chat does.
func (r *checkoutRig) holdOwner(t *testing.T) func(bool) {
	t.Helper()
	rel, err := r.server.acquireChatDirLock(t.Context(), "owner", r.agent, r.wtPath)
	require.NoError(t, err)
	return rel
}

func runWithin(t *testing.T, d time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatal("run hung: the subagent waits for a lock held by the chat that waits for it")
		return nil
	}
}

// The worktree of a child is the owner's: the run request names the owner and
// the child never creates a worktree of its own.
func TestSubagentRunUsesOwnersWorktree(t *testing.T) {
	r := newCheckoutRig(t)
	r.child(t, "child", "owner", false)
	require.NoError(t, runWithin(t, 5*time.Second, func() error { return r.run(t.Context(), "child") }))
	req := <-r.reqs
	assert.True(t, req.Worktree)
	assert.Equal(t, "owner", req.WorktreeChatID)
	assert.Equal(t, "child", req.ChatID)
	assert.False(t, req.Readonly)

	// The owner itself is unchanged.
	require.NoError(t, runWithin(t, 5*time.Second, func() error { return r.run(t.Context(), "owner") }))
	req = <-r.reqs
	assert.True(t, req.Worktree)
	assert.Equal(t, "owner", req.WorktreeChatID)
}

// The owner holds the worktree lock while it runs and waits for its writable
// child: once it is marked waiting, the child must not wait for it. This also
// holds for a grandchild.
func TestWritableSubagentDoesNotDeadlockOnWaitingParent(t *testing.T) {
	r := newCheckoutRig(t)
	relOwner := r.holdOwner(t)
	defer relOwner(false)
	require.NoError(t, r.server.dirLocks.SetWaitingOnDescendants("owner", true))
	r.child(t, "child", "owner", false)
	r.child(t, "grand", "child", false)

	require.NoError(t, runWithin(t, 5*time.Second, func() error { return r.run(t.Context(), "child") }))
	require.NoError(t, runWithin(t, 5*time.Second, func() error { return r.run(t.Context(), "grand") }))
}

// Writable subagents serialize with each other, a readonly one skips the lock.
func TestSubagentLocking(t *testing.T) {
	r := newCheckoutRig(t)
	relOwner := r.holdOwner(t)
	defer relOwner(false)
	require.NoError(t, r.server.dirLocks.SetWaitingOnDescendants("owner", true))
	r.child(t, "w1", "owner", false)
	r.child(t, "w2", "owner", false)
	r.child(t, "ro1", "owner", true)
	r.child(t, "ro2", "owner", true)

	// w1 holds the lock while it runs.
	hold := make(chan struct{})
	entered := make(chan struct{}, 1)
	r.server.dispatcher = &stubDispatcher{step: func(_ int, req dispatch.Request) (dispatch.Result, error) {
		if req.ChatID == "w1" {
			entered <- struct{}{}
			<-hold
		}
		r.reqs <- req
		return dispatch.Result{Output: []byte(`{"last_content":"ok","session_id":"s"}`)}, nil
	}}
	w1 := make(chan error, 1)
	go func() { w1 <- r.run(t.Context(), "w1") }()
	<-entered

	// A second writer waits for w1.
	w2 := make(chan error, 1)
	go func() { w2 <- r.run(t.Context(), "w2") }()
	require.Eventually(t, func() bool {
		st := r.server.dirLocks.StatusForChat("w2")
		return st != nil && st.IsWaitingForLock && st.LockedByChatID == "w1"
	}, 2*time.Second, 10*time.Millisecond)

	// Readonly children run meanwhile, in parallel, holding no lock.
	require.NoError(t, runWithin(t, 5*time.Second, func() error { return r.run(t.Context(), "ro1") }))
	require.NoError(t, runWithin(t, 5*time.Second, func() error { return r.run(t.Context(), "ro2") }))
	assert.Nil(t, r.server.dirLocks.StatusForChat("ro1"))
	var sawReadonly int
	for len(r.reqs) > 0 {
		if req := <-r.reqs; req.Readonly {
			sawReadonly++
			assert.Equal(t, "owner", req.WorktreeChatID)
		}
	}
	assert.Equal(t, 2, sawReadonly)

	close(hold)
	require.NoError(t, <-w1)
	require.NoError(t, <-w2)
}

// A readonly run takes no lock even when the checkout is locked by a stranger.
func TestReadonlySubagentIgnoresForeignLock(t *testing.T) {
	r := newCheckoutRig(t)
	rel, err := r.server.acquireChatDirLock(t.Context(), "stranger", r.agent, r.wtPath)
	require.NoError(t, err)
	defer rel(false)
	r.child(t, "ro", "owner", true)
	require.NoError(t, runWithin(t, 5*time.Second, func() error { return r.run(t.Context(), "ro") }))

	r.child(t, "rw", "owner", false)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	err = r.run(ctx, "rw")
	require.Error(t, err, "a writable subagent still waits for a stranger's lock")
}

// A parent that is still running (not waiting) is a writer like any other: its
// writable child queues behind it, a readonly child does not.
func TestWritableSubagentQueuesBehindRunningParent(t *testing.T) {
	r := newCheckoutRig(t)
	relOwner := r.holdOwner(t)
	r.child(t, "child", "owner", false)
	r.child(t, "ro", "owner", true)

	require.NoError(t, runWithin(t, 5*time.Second, func() error { return r.run(t.Context(), "ro") }))

	done := make(chan error, 1)
	go func() { done <- r.run(t.Context(), "child") }()
	require.Eventually(t, func() bool {
		st := r.server.dirLocks.StatusForChat("child")
		return st != nil && st.IsWaitingForLock && st.LockedByChatID == "owner"
	}, 2*time.Second, 10*time.Millisecond)
	relOwner(false)
	require.NoError(t, runWithin(t, 5*time.Second, func() error { return <-done }))
}

// A subagent whose recorded worktree cannot be honored now fails closed.
func TestSubagentFailsClosedWithoutWorktree(t *testing.T) {
	r := newCheckoutRig(t)
	r.child(t, "child", "owner", false)

	r.agent.Config.Worktree.Enabled = false
	err := r.run(t.Context(), "child")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree")
	assert.Empty(t, r.reqs, "nothing may run in the main checkout")

	r.agent.Config.Worktree = agentspec.WorktreeConfig{Enabled: true, Root: "/elsewhere"}
	err = r.run(t.Context(), "child")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/elsewhere")
	assert.Empty(t, r.reqs)
}

// A readonly child of a plain (non-worktree) chat takes no lock and runs
// readonly in the chat's own checkout.
func TestReadonlySubagentOfPlainChat(t *testing.T) {
	r := newCheckoutRig(t)
	require.NoError(t, r.repo.SaveSession(&dbmodels.Session{ChatID: "plain", CurrentAgent: "da", RunDir: "/work"}))
	require.NoError(t, r.repo.SaveSession(&dbmodels.Session{
		ChatID: "ro", CurrentAgent: "da", RunDir: "/work", ParentChatID: "plain", SpawnReadonly: true, SpawnDepth: 2,
	}))
	rel, err := r.server.acquireChatDirLock(t.Context(), "plain", r.agent, "/work")
	require.NoError(t, err)
	defer rel(false)

	require.NoError(t, runWithin(t, 5*time.Second, func() error { return r.run(t.Context(), "ro") }))
	req := <-r.reqs
	assert.True(t, req.Readonly)
	assert.False(t, req.Worktree)
	assert.Empty(t, req.WorktreeChatID)
	assert.Nil(t, r.server.dirLocks.StatusForChat("ro"))
}
