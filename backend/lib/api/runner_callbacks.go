package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/metrics"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

const (
	// callbackResultTTL is how long the result of a finished sandbox call is
	// kept so a retry (e.g. after a reconnect lost the response) still gets it.
	callbackResultTTL = 10 * time.Minute
	// callbackMaxEntries bounds the call table: finished entries are evicted
	// (oldest first) to stay under it, in-flight ones never are, so the table
	// holds at most this many finished plus maxInFlightTotal executing entries.
	callbackMaxEntries = 4096
	// maxInFlightPerRun and maxInFlightTotal bound the calls executing at once
	// (the eviction above only ever removes finished entries); excess calls
	// get ResourceExhausted, which the runner treats as permanent.
	maxInFlightPerRun = 16
	maxInFlightTotal  = 256
	// callbackMaxBytes bounds the total size of the cached results.
	callbackMaxBytes = 64 << 20
	// maxMetricsEvents bounds one RecordMetrics batch.
	maxMetricsEvents = 1000
	// maxCallbackText caps the reply and peer output sent to a runner; the
	// protocol's control messages stay well below the 16 MiB gRPC limit.
	maxCallbackText = 8 << 20
	// maxMessageIDSize bounds a runner-supplied ask-user message id.
	maxMessageIDSize = 128
)

// callbackService is the backend side of AgentCallbackService: the sandbox
// calls (ask-user, call-peer, find-peer, metrics) the runners forward. It
// validates that the run belongs to the calling runner and is active, derives
// the identity of the call from the hub's run tracking (never from the
// request), and runs each (run_id, call_seq) at most once: a retry attaches to
// the in-flight call or receives the cached result.
type callbackService struct {
	runnerpb.UnimplementedAgentCallbackServiceServer
	s    *Server
	runs runLookup
	// nodeID identifies the calling runner (runnerhub.NodeIDFromContext).
	nodeID func(context.Context) (string, bool)

	mu        sync.Mutex
	calls     map[callKey]*callEntry
	lastSweep time.Time
	bytes     int
	inFlight  map[string]int // executing calls per run_id
	inTotal   int
	now       func() time.Time
}

type callKey struct {
	runID string
	seq   uint64
}

type callEntry struct {
	kind       string
	done       chan struct{}
	resp       proto.Message
	err        error
	finishedAt time.Time
	size       int
}

// runLookup is the hub's run tracking.
type runLookup interface {
	ActiveRun(runID, nodeID string) (runnerhub.RunInfo, bool)
}

func newCallbackService(s *Server, runs runLookup) *callbackService {
	return &callbackService{s: s, runs: runs, nodeID: runnerhub.NodeIDFromContext, calls: make(map[callKey]*callEntry), inFlight: make(map[string]int), now: time.Now}
}

// callFunc executes one call for the validated run. A returned error is an
// infrastructure failure reported as a gRPC status; domain failures belong in
// the response.
type callFunc func(ctx context.Context, info runnerhub.RunInfo) (proto.Message, error)

// run validates the call, then executes fn once per (run_id, call_seq).
func (c *callbackService) run(ctx context.Context, meta *runnerpb.CallMeta, kind string, fn callFunc) (proto.Message, error) {
	runID, seq := meta.GetRunId(), meta.GetCallSeq()
	if runID == "" || seq == 0 {
		return nil, status.Error(codes.InvalidArgument, "meta.run_id and meta.call_seq are required")
	}
	nodeID, ok := c.nodeID(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unknown runner")
	}
	info, ok := c.runs.ActiveRun(runID, nodeID)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "run is not active on this runner")
	}

	key := callKey{runID: runID, seq: seq}
	c.mu.Lock()
	e := c.calls[key]
	if e != nil && e.kind != kind {
		c.mu.Unlock()
		return nil, status.Errorf(codes.InvalidArgument, "call_seq %d was used for a %s call", seq, e.kind)
	}
	if e == nil {
		if c.inFlight[runID] >= maxInFlightPerRun || c.inTotal >= maxInFlightTotal {
			c.mu.Unlock()
			return nil, status.Error(codes.ResourceExhausted, "too many concurrent callback calls")
		}
		c.inFlight[runID]++
		c.inTotal++
		c.sweepLocked()
		e = &callEntry{kind: kind, done: make(chan struct{})}
		c.calls[key] = e
		go c.execute(e, info, fn)
	}
	c.mu.Unlock()

	select {
	case <-e.done:
		return e.resp, e.err
	case <-ctx.Done():
		// The runner went away (or gave up); the call keeps running for the
		// retry to attach to, until the run ends.
		return nil, status.FromContextError(ctx.Err()).Err()
	}
}

// execute runs fn detached from any RPC; the call is cancelled when its run
// ends or the server shuts down.
func (c *callbackService) execute(e *callEntry, info runnerhub.RunInfo, fn callFunc) {
	ctx, cancel := context.WithCancel(c.s.ctx)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-info.Done:
			cancel()
		case <-stop:
		}
	}()

	type outcome struct {
		resp proto.Message
		err  error
	}
	res := make(chan outcome, 1)
	go func() {
		var o outcome
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Str("run_id", info.RunID).Msg("panic in callback handler")
				o = outcome{err: status.Error(codes.Internal, "internal error")}
			}
			res <- o
		}()
		o.resp, o.err = fn(ctx, info)
	}()

	// The call finishes when fn returns or when its context ends (run end,
	// shutdown), whichever is first; a later result of fn is discarded. This
	// frees the in-flight slots even for a fn that ignores its context.
	var o outcome
	select {
	case o = <-res:
	case <-ctx.Done():
		o = outcome{err: status.Error(codes.Canceled, "call cancelled: run ended")}
	}
	if o.err != nil {
		if _, ok := status.FromError(o.err); !ok {
			o.err = status.Error(codes.Internal, o.err.Error())
		}
	}
	c.mu.Lock()
	e.resp, e.err, e.finishedAt = o.resp, o.err, c.now()
	if c.inFlight[info.RunID]--; c.inFlight[info.RunID] <= 0 {
		delete(c.inFlight, info.RunID)
	}
	c.inTotal--
	if o.resp != nil {
		e.size = proto.Size(o.resp)
		c.bytes += e.size
	}
	c.evictLocked(true)
	c.mu.Unlock()
	close(e.done)
}

// sweepLocked runs on call creation: it drops expired entries at most once a
// minute and enforces the bounds.
func (c *callbackService) sweepLocked() {
	c.evictLocked(false)
}

// evictLocked drops expired finished entries (at most once a minute unless
// force) and then the oldest finished ones while over the entry or byte bound.
func (c *callbackService) evictLocked(force bool) {
	now := c.now()
	over := len(c.calls) >= callbackMaxEntries || c.bytes > callbackMaxBytes
	if !force && !over && now.Sub(c.lastSweep) < time.Minute {
		return
	}
	if now.Sub(c.lastSweep) >= time.Minute || over {
		c.lastSweep = now
		for k, e := range c.calls {
			if !e.finishedAt.IsZero() && now.Sub(e.finishedAt) > callbackResultTTL {
				c.dropLocked(k, e)
			}
		}
	}
	for len(c.calls) >= callbackMaxEntries || c.bytes > callbackMaxBytes {
		var oldestKey callKey
		var oldest *callEntry
		for k, e := range c.calls {
			if !e.finishedAt.IsZero() && (oldest == nil || e.finishedAt.Before(oldest.finishedAt)) {
				oldestKey, oldest = k, e
			}
		}
		if oldest == nil {
			return
		}
		c.dropLocked(oldestKey, oldest)
	}
}

func (c *callbackService) dropLocked(k callKey, e *callEntry) {
	c.bytes -= e.size
	delete(c.calls, k)
}

// AskUser implements runnerpb.AgentCallbackServiceServer.
func (c *callbackService) AskUser(ctx context.Context, req *runnerpb.AskUserRequest) (*runnerpb.AskUserResponse, error) {
	resp, err := c.run(ctx, req.GetMeta(), "ask_user", func(ctx context.Context, info runnerhub.RunInfo) (proto.Message, error) {
		if req.GetQuestion() == "" {
			return askUserError("question is required"), nil
		}
		msgID := req.GetMessageId()
		if msgID == "" || len(msgID) > maxMessageIDSize {
			msgID = fmt.Sprintf("ask-%s-%d", info.RunID, req.GetMeta().GetCallSeq())
		}
		reply, err := c.s.askUser(ctx, AskUserRequest{
			ChatID:    info.ChatID,
			AgentName: req.GetAgentName(),
			Question:  req.GetQuestion(),
			MessageID: msgID,
			Model:     req.GetModel(),
		})
		if err != nil {
			return askUserError("run ended before the user replied"), nil
		}
		return &runnerpb.AskUserResponse{Reply: capText(reply)}, nil
	})
	if err != nil {
		return nil, err
	}
	return resp.(*runnerpb.AskUserResponse), nil
}

func askUserError(msg string) *runnerpb.AskUserResponse {
	text, cut := runnerhub.TruncateErrorText(msg)
	return &runnerpb.AskUserResponse{Error: text, ErrorTruncated: cut}
}

// CallPeer implements runnerpb.AgentCallbackServiceServer.
func (c *callbackService) CallPeer(ctx context.Context, req *runnerpb.CallPeerRequest) (*runnerpb.CallPeerResponse, error) {
	resp, err := c.run(ctx, req.GetMeta(), "call_peer", func(ctx context.Context, info runnerhub.RunInfo) (proto.Message, error) {
		return c.callPeer(ctx, info, req), nil
	})
	if err != nil {
		return nil, err
	}
	return resp.(*runnerpb.CallPeerResponse), nil
}

// callPeer triggers the peer agent through the same code as POST
// /api/agents/{id}/message with wait=true, in the caller's chat.
func (c *callbackService) callPeer(ctx context.Context, info runnerhub.RunInfo, req *runnerpb.CallPeerRequest) *runnerpb.CallPeerResponse {
	fail := func(msg string) *runnerpb.CallPeerResponse {
		text, cut := runnerhub.TruncateErrorText(msg)
		return &runnerpb.CallPeerResponse{Status: "failed", Error: text, ErrorTruncated: cut}
	}
	agentID := req.GetAgentId()
	if agentID == "" {
		return fail("agent_id is required")
	}

	// Caller identity comes from the hub's run tracking, over anything the
	// sandbox or runner put in the metadata.
	metadata := map[string]any{}
	if raw := req.GetMetadataJson(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return fail("invalid metadata_json: " + err.Error())
		}
		if metadata == nil { // JSON null
			metadata = map[string]any{}
		}
	}
	metadata["internal"] = true
	metadata["source"] = "call-peer"
	metadata["caller_agent_id"] = info.AgentID
	tr := TriggerMessageRequest{
		Prompt:   req.GetPrompt(),
		ChatID:   info.ChatID,
		Model:    req.GetModel(),
		Wait:     true,
		Metadata: metadata,
	}
	if req.GetAllowCrossSession() {
		allow := true
		tr.AllowCrossSession = &allow
	}

	target := c.s.findLoadedAgent(agentID)
	rec := newResponseCapture()
	if target == nil && !c.s.bindingEnforced() {
		return fail("agent not found")
	}
	c.s.triggerMessage(ctx, rec, agentID, target, tr, true)

	var body struct {
		Status string `json:"status"`
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(rec.body.Bytes(), &body)
	resp := &runnerpb.CallPeerResponse{Status: body.Status, Output: capText(body.Output)}
	if rec.code != http.StatusOK {
		if resp.Status == "" || resp.Status == "completed" {
			resp.Status = "failed"
		}
		body.Error = firstNonEmpty(body.Error, fmt.Sprintf("peer call failed with HTTP status %d: %s", rec.code, rec.body.String()))
	}
	resp.Error, resp.ErrorTruncated = runnerhub.TruncateErrorText(body.Error)
	return resp
}

// capText cuts s to maxCallbackText on a UTF-8 boundary and marks the cut.
func capText(s string) string {
	if len(s) <= maxCallbackText {
		return s
	}
	n := maxCallbackText
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n[truncated]"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// FindPeer implements runnerpb.AgentCallbackServiceServer.
func (c *callbackService) FindPeer(ctx context.Context, req *runnerpb.FindPeerRequest) (*runnerpb.FindPeerResponse, error) {
	resp, err := c.run(ctx, req.GetMeta(), "find_peer", func(_ context.Context, info runnerhub.RunInfo) (proto.Message, error) {
		if c.s.repo == nil {
			return nil, status.Error(codes.FailedPrecondition, "database unavailable in degraded mode")
		}
		peers, code, msg := c.s.teamAgentsFor(info.ChatID)
		if msg != "" {
			grpcCode := codes.Internal
			if code == http.StatusNotFound {
				grpcCode = codes.NotFound
			}
			return nil, status.Error(grpcCode, msg)
		}
		return &runnerpb.FindPeerResponse{Peers: peers}, nil
	})
	if err != nil {
		return nil, err
	}
	return resp.(*runnerpb.FindPeerResponse), nil
}

// RecordMetrics implements runnerpb.AgentCallbackServiceServer.
func (c *callbackService) RecordMetrics(ctx context.Context, req *runnerpb.RecordMetricsRequest) (*runnerpb.RecordMetricsResponse, error) {
	if len(req.GetEvents()) > maxMetricsEvents {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d events per call", maxMetricsEvents)
	}
	resp, err := c.run(ctx, req.GetMeta(), "record_metrics", func(context.Context, runnerhub.RunInfo) (proto.Message, error) {
		events := make([]metrics.Event, 0, len(req.GetEvents()))
		for _, ev := range req.GetEvents() {
			events = append(events, metrics.Event{Kind: metrics.Kind(ev.GetKind()), Tokens: ev.GetTokens(), Bytes: ev.GetBytes()})
		}
		c.s.compressionMetrics.Record(events)
		return &runnerpb.RecordMetricsResponse{}, nil
	})
	if err != nil {
		return nil, err
	}
	return resp.(*runnerpb.RecordMetricsResponse), nil
}

// responseCapture is an in-memory http.ResponseWriter, so the handlers' shared
// response-writing code can serve an in-process call.
type responseCapture struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func newResponseCapture() *responseCapture {
	return &responseCapture{header: http.Header{}, code: http.StatusOK}
}

func (r *responseCapture) Header() http.Header         { return r.header }
func (r *responseCapture) WriteHeader(code int)        { r.code = code }
func (r *responseCapture) Write(b []byte) (int, error) { return r.body.Write(b) }
