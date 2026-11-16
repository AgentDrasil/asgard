package api

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func saveTreeChat(t *testing.T, rig *callbackRig, id, parent, agent, label string, readonly bool) {
	t.Helper()
	require.NoError(t, rig.repo.SaveSession(&dbmodels.Session{
		ChatID: id, CurrentAgent: agent, ParentChatID: parent, SpawnLabel: label, SpawnReadonly: readonly,
	}))
}

func treeIDs(nodes []SubagentTreeNode) []string {
	var ids []string
	for _, n := range nodes {
		ids = append(ids, n.Info.ID)
	}
	return ids
}

func TestSubagentTreeFromAnyChatOfTheTree(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	saveTreeChat(t, rig, "c1", rigChat, "beta", "one", true)
	saveTreeChat(t, rig, "c2", rigChat, "beta", "two", false)
	saveTreeChat(t, rig, "g1", "c1", "beta", "deep", false)
	saveTreeChat(t, rig, "h1", "g1", "alpha", "deeper", true)
	// A second, unrelated tree.
	saveTreeChat(t, rig, "other-root", "", "alpha", "", false)
	saveTreeChat(t, rig, "other-child", "other-root", "beta", "x", false)

	want := []string{rigChat, "c1", "g1", "h1", "c2"}
	for _, caller := range []string{rigChat, "c1", "g1", "h1", "c2"} {
		nodes, truncated, err := rig.server.SubagentTree(caller)
		require.False(t, truncated)
		require.NoError(t, err, caller)
		assert.Equal(t, want, treeIDs(nodes), "caller %s", caller)
		for _, n := range nodes {
			assert.Equal(t, n.Info.ID == caller, n.Self, "caller %s row %s", caller, n.Info.ID)
		}
	}

	nodes, _, err := rig.server.SubagentTree("g1")
	require.NoError(t, err)
	depths := map[string]int{}
	for _, n := range nodes {
		depths[n.Info.ID] = n.Depth
	}
	assert.Equal(t, map[string]int{rigChat: 0, "c1": 1, "c2": 1, "g1": 2, "h1": 3}, depths)
	root := nodes[0]
	assert.Empty(t, root.Info.ParentChatID)
	assert.Equal(t, SubagentStateIdle, root.Info.State)
	assert.Equal(t, "alpha", root.Info.Agent)
	assert.Equal(t, "c1", nodes[1].Info.ID)
	assert.Equal(t, "one", nodes[1].Info.Label)
	assert.True(t, nodes[1].Info.Readonly)
	assert.Equal(t, rigChat, nodes[1].Info.ParentChatID)
	assert.Equal(t, SubagentStateDone, nodes[1].Info.State)

	// The other tree sees only itself and never this one.
	other, _, err := rig.server.SubagentTree("other-child")
	require.NoError(t, err)
	assert.Equal(t, []string{"other-root", "other-child"}, treeIDs(other))
}

func TestSubagentTreeRootRunningState(t *testing.T) {
	rig, c := reportRig(t)
	child := spawnOne(t, rig, c)
	rig.server.activeExecutions.Store(rigChat, &executionHandle{})
	nodes, _, err := rig.server.SubagentTree(child)
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	assert.Equal(t, SubagentStateRunning, nodes[0].Info.State)
	assert.Equal(t, SubagentStateRunning, nodes[1].Info.State)
	assert.True(t, nodes[1].Self)
}

func TestSubagentTreeTruncatedAtTheCap(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"))
	for i := 0; i < maxSubagentDescendants+5; i++ {
		saveTreeChat(t, rig, fmt.Sprintf("c%05d", i), rigChat, "alpha", "", false)
	}
	nodes, truncated, err := rig.server.SubagentTree(rigChat)
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.Len(t, nodes, maxSubagentDescendants)
	assert.Equal(t, rigChat, nodes[0].Info.ID)

	resp, err := rig.svc.SubagentTree(rig.ctx(), &runnerpb.SubagentTreeRequest{Meta: meta(rigRun, 1)})
	require.NoError(t, err)
	assert.True(t, resp.GetTruncated())
}

func TestSubagentTreeOrphanRootAndCycleSafe(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"))
	// The parent was deleted: the child is the topmost chat left.
	saveTreeChat(t, rig, "orphan", "deleted-parent", "alpha", "o", false)
	saveTreeChat(t, rig, "kid", "orphan", "alpha", "k", false)
	nodes, truncated, err := rig.server.SubagentTree("kid")
	require.NoError(t, err)
	assert.False(t, truncated)
	assert.Equal(t, []string{"orphan", "kid"}, treeIDs(nodes))
	assert.Empty(t, nodes[0].Info.ParentChatID)
}

func TestSubagentTreeUnknownCaller(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"))
	_, _, err := rig.server.SubagentTree("nope")
	var se *SubagentError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, SubagentNotFound, se.Kind)
}

func TestCallbackSubagentTreeUsesRunIdentity(t *testing.T) {
	rig := newCallbackRig(t, teamAgent("alpha", "t"), teamAgent("beta", "t"))
	saveTreeChat(t, rig, "c1", rigChat, "beta", "one", false)
	saveTreeChat(t, rig, "other-root", "", "alpha", "", false)
	resp, err := rig.svc.SubagentTree(rig.ctx(), &runnerpb.SubagentTreeRequest{Meta: meta(rigRun, 1)})
	require.NoError(t, err)
	require.Len(t, resp.GetNodes(), 2)
	assert.Equal(t, rigChat, resp.GetNodes()[0].GetInfo().GetId())
	assert.True(t, resp.GetNodes()[0].GetSelf())
	assert.Equal(t, int32(1), resp.GetNodes()[1].GetDepth())

	_, err = rig.svc.SubagentTree(rig.ctx(), &runnerpb.SubagentTreeRequest{Meta: &runnerpb.CallMeta{RunId: "unknown-run"}})
	require.Error(t, err)
}
