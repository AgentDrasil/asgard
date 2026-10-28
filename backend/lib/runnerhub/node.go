package runnerhub

import (
	"time"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// node is the registry entry of one runner. It outlives connections: the
// outbound queue, the dedup state and the pending runs are kept across
// reconnects. All fields are guarded by Hub.mu.
type node struct {
	id     string
	online bool
	// onlineGen counts the registrations of the node: it increases on every
	// transition to online, so a changed value means a real reconnect.
	onlineGen       uint64
	lastSeen        time.Time
	version         string
	protocolVersion uint32
	runnerBootID    string
	maxRuns         uint32
	supportedCLIs   map[string][]string
	quotas          []*runnerpb.TargetQuota
	conn            *connection

	// nextSeq is the last backend seq assigned (per backend boot id).
	nextSeq uint64
	// queue holds unacked outbound messages in seq order.
	queue []*runnerpb.BackendMessage
	// highestRunnerSeq is the highest runner seq already processed.
	highestRunnerSeq uint64
}

func (h *Hub) nodeLocked(id string) *node {
	n := h.nodes[id]
	if n == nil {
		n = &node{id: id, supportedCLIs: map[string][]string{}}
		h.nodes[id] = n
	}
	return n
}

// enqueueLocked assigns the next seq to msg, queues it for (re)delivery and
// wakes the connection writer.
func (n *node) enqueueLocked(msg *runnerpb.BackendMessage) {
	n.nextSeq++
	msg.Seq = n.nextSeq
	n.queue = append(n.queue, msg)
	if n.conn != nil {
		n.conn.wakeup()
	}
}

// trimQueueLocked drops every queued message with seq <= acked.
func (n *node) trimQueueLocked(acked uint64) {
	i := 0
	for i < len(n.queue) && n.queue[i].Seq <= acked {
		i++
	}
	n.queue = n.queue[i:]
}

// queuedStartRunsLocked returns the run ids of StartRuns still queued.
func (n *node) queuedStartRunsLocked() map[string]bool {
	out := make(map[string]bool)
	for _, m := range n.queue {
		if sr := m.GetStartRun(); sr != nil {
			out[sr.GetRunId()] = true
		}
	}
	return out
}
