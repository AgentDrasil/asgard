package dispatch

import "sync"

// activeKey identifies a run slot. Call-peer nested runs execute under the
// same chat id as the session's own agent, so the chat id alone would let an
// injection hit a peer's run instead of the session's current agent.
type activeKey struct{ chatID, agentID string }

type activeEntry struct {
	nodeID, runID string
	// seq distinguishes registrations so a release removes only its own entry.
	seq uint64
}

// ActiveRuns maps (chat, agent) to the run currently executing for it. It is
// what lets the API layer address a mid-run injection: nothing else knows
// which run id belongs to a chat. Known limit: a call-peer run of the same
// agent in the same chat shadows the outer run's entry while it executes.
type ActiveRuns struct {
	mu  sync.Mutex
	m   map[activeKey]activeEntry
	seq uint64
}

// NewActiveRuns creates an empty registry.
func NewActiveRuns() *ActiveRuns { return &ActiveRuns{m: map[activeKey]activeEntry{}} }

// Register records runID on nodeID as the current run of (chatID, agentID) and
// returns the release func. The latest registration wins (a retry or quota
// restart gets a new run id); release removes the entry only if it is still
// the one this call made, so an earlier attempt finishing late cannot erase a
// later attempt. release is idempotent.
func (a *ActiveRuns) Register(chatID, agentID, nodeID, runID string) (release func()) {
	a.mu.Lock()
	a.seq++
	seq := a.seq
	k := activeKey{chatID, agentID}
	a.m[k] = activeEntry{nodeID: nodeID, runID: runID, seq: seq}
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if e, ok := a.m[k]; ok && e.seq == seq {
			delete(a.m, k)
		}
	}
}

// Lookup returns the run currently registered for (chatID, agentID).
func (a *ActiveRuns) Lookup(chatID, agentID string) (nodeID, runID string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.m[activeKey{chatID, agentID}]
	return e.nodeID, e.runID, ok
}
