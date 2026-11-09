package dispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func TestActiveRuns_KeyedByChatAndAgentLatestWins(t *testing.T) {
	a := NewActiveRuns()
	relPeer := a.Register("c", "peer", "n1", "run-peer")
	rel1 := a.Register("c", "main", "n1", "run-1")

	node, run, ok := a.Lookup("c", "main")
	require.True(t, ok)
	assert.Equal(t, "n1", node)
	assert.Equal(t, "run-1", run)
	_, run, _ = a.Lookup("c", "peer")
	assert.Equal(t, "run-peer", run, "a peer under the same chat is a separate slot")

	// A retry registers before the earlier attempt is released.
	rel2 := a.Register("c", "main", "n1", "run-2")
	rel1()
	_, run, ok = a.Lookup("c", "main")
	require.True(t, ok)
	assert.Equal(t, "run-2", run, "an old attempt must not remove the newer entry")

	rel2()
	rel2() // idempotent
	_, _, ok = a.Lookup("c", "main")
	assert.False(t, ok)
	relPeer()
}

func TestRemoteRun_RegistersActiveRunUntilItReturns(t *testing.T) {
	hub := &fakeHub{handle: &fakeHandle{events: []*runnerpb.RunEvent{{}}, result: okResult("out")}}
	d := newTestRemote(hub, nil)
	var seenRun string
	var seenOK bool
	_, err := d.Run(context.Background(), Request{ChatID: "chat-1", Agent: testAgent(), OnEvent: func(*runnerpb.RunEvent) {
		_, seenRun, seenOK = d.ActiveRuns().Lookup("chat-1", "a")
	}})
	require.NoError(t, err)
	require.True(t, seenOK, "registered while the run executes")
	assert.Equal(t, hub.sent[0].GetRunId(), seenRun)
	_, _, ok := d.ActiveRuns().Lookup("chat-1", "a")
	assert.False(t, ok, "removed once Run returned")
}

func TestRemoteRun_DispatchFailureRegistersNothing(t *testing.T) {
	hub := &fakeHub{err: errors.New("offline")}
	d := newTestRemote(hub, nil)
	_, err := d.Run(context.Background(), Request{ChatID: "chat-1", Agent: testAgent()})
	require.Error(t, err)
	_, _, ok := d.ActiveRuns().Lookup("chat-1", "a")
	assert.False(t, ok)
}
