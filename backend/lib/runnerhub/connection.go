package runnerhub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

type nodeIDKey struct{}

func withNodeID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, nodeIDKey{}, id)
}

// NodeIDFromContext returns the id of the runner a backend gRPC call came
// from (the CN of its client certificate).
func NodeIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(nodeIDKey{}).(string)
	return id, ok
}

// nodeStream overrides the context of a server stream.
type nodeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *nodeStream) Context() context.Context { return s.ctx }

// connection is one runner connection: a yamux session carrying the backend
// gRPC server (runner-opened streams) and the RunnerService client
// (backend-opened streams).
type connection struct {
	hub    *Hub
	nodeID string
	sess   *yamux.Session
	cc     *grpc.ClientConn
	runner runnerpb.RunnerServiceClient

	connecting atomic.Bool
	registered atomic.Bool
	wake       chan struct{}
	// ackDirty/ackSeq (guarded by Hub.mu) request an Ack frame.
	ackDirty bool
	ackSeq   uint64
}

func (c *connection) wakeup() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// nodeControl implements NodeControlService for one connection.
type nodeControl struct {
	runnerpb.UnimplementedNodeControlServiceServer
	conn *connection
}

type connectStream = grpc.BidiStreamingServer[runnerpb.RunnerMessage, runnerpb.BackendMessage]

// Connect runs the control stream: Register handshake, then the ordered,
// acknowledged message exchange.
func (s *nodeControl) Connect(stream connectStream) error {
	c := s.conn
	h := c.hub
	if !c.connecting.CompareAndSwap(false, true) {
		return status.Error(codes.AlreadyExists, "connection already registered")
	}
	// Any end of the control stream ends the session so the runner
	// reconnects with a fresh one (which may register again). A rejection
	// lingers briefly so the RegisterResult reaches the runner.
	rejected := false
	defer func() {
		if rejected {
			time.AfterFunc(time.Second, func() { _ = c.sess.Close() })
			return
		}
		_ = c.sess.Close()
	}()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	reg := first.GetRegister()
	if reg == nil {
		return status.Error(codes.InvalidArgument, "first message must be Register")
	}
	result := h.register(c, reg)
	rejected = !result.GetAccepted()
	if err := stream.Send(&runnerpb.BackendMessage{Payload: &runnerpb.BackendMessage_RegisterResult{RegisterResult: result}}); err != nil {
		return err
	}
	if !result.GetAccepted() {
		log.Warn().Str("node_id", c.nodeID).Str("reason", result.GetRejectReason()).Msg("runner registration rejected")
		return nil
	}
	c.registered.Store(true)
	log.Info().Str("node_id", c.nodeID).Str("version", truncateString(reg.GetVersion(), 128)).Msg("runner registered")

	go c.writeLoop(stream)
	c.wakeup() // replay unacked messages

	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || status.Code(err) == codes.Canceled {
				return nil
			}
			return err
		}
		if err := h.handleUpstream(c, msg); err != nil {
			log.Warn().Err(err).Str("node_id", c.nodeID).Msg("runner message handling failed, dropping connection")
			_ = c.sess.Close()
			return err
		}
	}
}

// writeLoop sends queued BackendMessages in seq order (starting with the
// replay of the unacked queue) and pending Ack frames. It is the only sender
// on the stream after RegisterResult.
func (c *connection) writeLoop(stream connectStream) {
	h := c.hub
	var sent uint64
	for {
		select {
		case <-c.wake:
		case <-stream.Context().Done():
			return
		}
		h.mu.Lock()
		n := h.nodes[c.nodeID]
		if n == nil || n.conn != c {
			h.mu.Unlock()
			return
		}
		var out []*runnerpb.BackendMessage
		for _, m := range n.queue {
			if m.Seq > sent {
				out = append(out, m)
			}
		}
		ack, ackDirty := c.ackSeq, c.ackDirty
		c.ackDirty = false
		h.mu.Unlock()

		for _, m := range out {
			if err := stream.Send(m); err != nil {
				_ = c.sess.Close()
				return
			}
			sent = m.Seq
		}
		if ackDirty {
			if err := stream.Send(&runnerpb.BackendMessage{Payload: &runnerpb.BackendMessage_Ack{Ack: &runnerpb.Ack{AckSeq: ack}}}); err != nil {
				_ = c.sess.Close()
				return
			}
		}
	}
}

// register validates a Register and, if accepted, rebinds the node to c.
func (h *Hub) register(c *connection, reg *runnerpb.Register) *runnerpb.RegisterResult {
	reject := func(format string, args ...any) *runnerpb.RegisterResult {
		reason, truncated := truncateErrorText(fmt.Sprintf(format, args...))
		return &runnerpb.RegisterResult{
			RejectReason:          reason,
			RejectReasonTruncated: truncated,
			ProtocolVersion:       runnerpb.ProtocolVersion,
			BackendBootId:         h.bootID,
		}
	}
	switch {
	case reg.GetNodeId() != c.nodeID:
		return reject("node_id %q does not match certificate CN %q", shortID(reg.GetNodeId()), shortID(c.nodeID))
	case reg.GetProtocolVersion() != runnerpb.ProtocolVersion:
		return reject("protocol version mismatch: runner %d, backend %d", reg.GetProtocolVersion(), runnerpb.ProtocolVersion)
	case !h.isAllowed(c.nodeID):
		return reject("node %q is not allowed", shortID(c.nodeID))
	}

	// Runs the runner reports as active that this backend neither tracks nor
	// finalized (e.g. after a backend restart) get cancelled below.
	finalized := make(map[string]bool)
	for _, ref := range reg.GetActiveRuns() {
		if done, err := h.store.IsFinalized(h.ctx, ref.GetRunId()); err != nil || done {
			finalized[ref.GetRunId()] = true // on error, do not cancel blindly
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.nodeLocked(c.nodeID)
	if old := n.conn; old != nil && old != c {
		// A reconnect before the old session noticed it died.
		n.conn = nil
		go func() { _ = old.sess.Close() }()
	}

	if n.runnerBootID != reg.GetRunnerBootId() {
		// The runner restarted: it counts from seq 1 again and lost
		// everything it had not finished. Unacked downstream messages are
		// dropped, their StartRuns are reported lost below.
		n.queue = nil
		n.highestRunnerSeq = 0
	} else if reg.GetLastSeenBackendBootId() == h.bootID {
		n.trimQueueLocked(reg.GetLastAckedBackendSeq())
	}
	n.runnerBootID = reg.GetRunnerBootId()
	n.version = reg.GetVersion()
	n.protocolVersion = reg.GetProtocolVersion()
	n.supportedCLIs = make(map[string][]string, len(reg.GetSupportedClis()))
	for cli, ml := range reg.GetSupportedClis() {
		n.supportedCLIs[cli] = append([]string(nil), ml.GetModels()...)
	}
	n.lastSeen = time.Now()
	n.online = true
	n.conn = c

	// Runs neither executing, nor awaiting result replay, nor still queued
	// for (re)delivery are gone.
	known := make(map[string]bool)
	for _, ref := range reg.GetActiveRuns() {
		known[ref.GetRunId()] = true
	}
	for _, ref := range reg.GetCompletedUnackedRuns() {
		known[ref.GetRunId()] = true
	}
	for id := range n.queuedStartRunsLocked() {
		known[id] = true
	}
	for id, r := range h.runs {
		if r.nodeID == n.id && !known[id] {
			r.finish(nil, fmt.Errorf("%w: runner %s no longer knows run %s", ErrRunLost, n.id, id))
			delete(h.runs, id)
		}
	}

	queuedCancels := make(map[string]bool)
	for _, m := range n.queue {
		if cr := m.GetCancelRun(); cr != nil {
			queuedCancels[cr.GetRunId()] = true
		}
	}
	for _, ref := range reg.GetActiveRuns() {
		id := ref.GetRunId()
		if h.runs[id] != nil || finalized[id] || queuedCancels[id] || id == "" {
			continue
		}
		queuedCancels[id] = true
		log.Warn().Str("node_id", n.id).Str("run_id", id).Msg("cancelling untracked run reported by runner")
		n.enqueueLocked(&runnerpb.BackendMessage{Payload: &runnerpb.BackendMessage_CancelRun{CancelRun: &runnerpb.CancelRun{RunId: id}}})
	}

	return &runnerpb.RegisterResult{
		Accepted:           true,
		ProtocolVersion:    runnerpb.ProtocolVersion,
		LastAckedRunnerSeq: n.highestRunnerSeq,
		BackendBootId:      h.bootID,
	}
}

// handleUpstream processes one runner message: Ack frames trim the queue;
// every other message is deduplicated by seq, applied, and only then acked.
func (h *Hub) handleUpstream(c *connection, msg *runnerpb.RunnerMessage) error {
	h.mu.Lock()
	n := h.nodes[c.nodeID]
	if n == nil || n.conn != c {
		h.mu.Unlock()
		return errors.New("connection superseded")
	}
	n.lastSeen = time.Now()
	if ack := msg.GetAck(); ack != nil {
		n.trimQueueLocked(ack.GetAckSeq())
		h.mu.Unlock()
		return nil
	}
	if msg.GetRegister() != nil || msg.GetSeq() == 0 {
		h.mu.Unlock()
		return errors.New("unexpected unsequenced runner message")
	}
	dup := msg.GetSeq() <= n.highestRunnerSeq
	h.mu.Unlock()

	if !dup {
		if err := h.apply(c, msg); err != nil {
			return err
		}
	}

	h.mu.Lock()
	if !dup && n.conn == c {
		n.highestRunnerSeq = msg.GetSeq()
	}
	c.ackSeq, c.ackDirty = n.highestRunnerSeq, true
	h.mu.Unlock()
	c.wakeup()
	return nil
}

// apply performs the effects of a new (non-duplicate) runner message. An
// error means the effect could not be persisted: the connection is dropped and
// the runner replays the message.
func (h *Hub) apply(c *connection, msg *runnerpb.RunnerMessage) error {
	switch p := msg.GetPayload().(type) {
	case *runnerpb.RunnerMessage_Heartbeat:
		h.mu.Lock()
		if n := h.nodes[c.nodeID]; n != nil {
			n.quotas = p.Heartbeat.GetQuotas()
			n.maxRuns = p.Heartbeat.GetMaxRuns()
		}
		h.mu.Unlock()
	case *runnerpb.RunnerMessage_RunEvent:
		if r := h.runFor(c, p.RunEvent.GetRunId()); r != nil {
			r.addEvent(p.RunEvent)
		}
	case *runnerpb.RunnerMessage_RunOutput:
		if r := h.runFor(c, p.RunOutput.GetRunId()); r != nil {
			if err := r.addChunk(p.RunOutput); err != nil {
				h.failRun(r, err)
			}
		}
	case *runnerpb.RunnerMessage_RunResult:
		return h.applyResult(c, p.RunResult)
	default:
		return fmt.Errorf("unexpected runner message %T", p)
	}
	return nil
}

func (h *Hub) runFor(c *connection, runID string) *Run {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.runs[runID]; r != nil && r.nodeID == c.nodeID {
		return r
	}
	return nil
}

func (h *Hub) failRun(r *Run, err error) {
	h.mu.Lock()
	if h.runs[r.runID] == r {
		delete(h.runs, r.runID)
	}
	h.mu.Unlock()
	r.finish(nil, err)
}

// applyResult finalizes a run at most once per run_id: the id is persisted
// before the result is delivered, so a replay (including after a backend
// restart) is ignored.
func (h *Hub) applyResult(c *connection, res *runnerpb.RunResult) error {
	h.mu.Lock()
	r := h.runs[res.GetRunId()]
	h.mu.Unlock()
	if r != nil && r.nodeID != c.nodeID {
		// Only the owning node may finalize a tracked run.
		log.Warn().Str("run_id", shortID(res.GetRunId())).Str("node_id", c.nodeID).Str("owner", r.nodeID).Msg("ignoring run result from non-owner node")
		return nil
	}

	fresh, err := h.store.MarkFinalized(h.ctx, res.GetRunId())
	if err != nil {
		return fmt.Errorf("persist finalized run %s: %w", res.GetRunId(), err)
	}
	if r == nil {
		if fresh {
			log.Warn().Str("run_id", shortID(res.GetRunId())).Str("node_id", c.nodeID).Msg("run result for unknown run")
		} else {
			log.Debug().Str("run_id", shortID(res.GetRunId())).Msg("ignoring repeated run result")
		}
		return nil
	}
	// A tracked run is delivered even if its id was finalized before (a crash
	// between persisting and delivering must not wedge it).
	h.mu.Lock()
	if h.runs[r.runID] == r {
		delete(h.runs, r.runID)
	}
	h.mu.Unlock()
	if got := r.outputSize(); got != res.GetOutputSize() {
		r.finish(nil, fmt.Errorf("run %s output size mismatch: received %d, expected %d", r.runID, got, res.GetOutputSize()))
		return nil
	}
	r.finish(res, nil)
	return nil
}

const (
	// maxErrorTextSize caps free-text error fields sent to the runner.
	maxErrorTextSize = 64 << 10
	// maxIDLogSize caps runner-supplied identifiers echoed in messages.
	maxIDLogSize = 128
)

// truncateString cuts s to at most max bytes on a UTF-8 boundary.
func truncateString(s string, max int) string {
	t, _ := truncateAt(s, max)
	return t
}

func truncateAt(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max], true
}

// truncateErrorText caps a free-text error at 64 KiB and reports whether it
// was cut (for the *_truncated flags).
func truncateErrorText(s string) (string, bool) {
	return truncateAt(s, maxErrorTextSize)
}

// shortID bounds a runner-supplied identifier before it is echoed.
func shortID(s string) string {
	return truncateString(s, maxIDLogSize)
}
