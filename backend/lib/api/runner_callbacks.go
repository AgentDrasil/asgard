package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/metrics"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
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
	// The caller's run holds its chat's execution guard while it waits for the
	// peer; tell triggerMessage so the peer is admitted under that guard.
	if holder, running := c.s.activeExecutions.Load(info.ChatID); running {
		ctx = context.WithValue(ctx, peerCallKey{}, &peerCall{chatID: info.ChatID, holder: holder, callerAgentID: info.AgentID})
	}
	c.s.triggerMessage(ctx, nil, rec, agentID, target, tr, true)

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

// GetUserProxyKeys implements runnerpb.AgentCallbackServiceServer. Decrypts the user's proxy keys from DB and returns them.
func (c *callbackService) GetUserProxyKeys(ctx context.Context, req *runnerpb.GetUserProxyKeysRequest) (*runnerpb.GetUserProxyKeysResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	runID := req.GetMeta().GetRunId()
	if runID == "" {
		return nil, status.Error(codes.InvalidArgument, "meta.run_id is required")
	}
	userID := req.GetUserId()
	if userID == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	nodeID, ok := c.nodeID(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unknown runner")
	}
	if c.runs == nil {
		return nil, status.Error(codes.FailedPrecondition, "run lookup unavailable")
	}
	info, ok := c.runs.ActiveRun(runID, nodeID)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "run is not active on this runner")
	}
	if info.UserID != userID {
		return nil, status.Error(codes.PermissionDenied, "user identity does not match active run")
	}

	if c.s == nil || c.s.userProxyKeyRepo == nil {
		return &runnerpb.GetUserProxyKeysResponse{Keys: map[string]string{}}, nil
	}
	keys, err := c.s.userProxyKeyRepo.GetAllDecryptedKeys(ctx, userID)
	if err != nil {
		log.Warn().Err(err).Str("user_id", userID).Msg("failed to load decrypted proxy keys for user")
		return nil, status.Errorf(codes.Internal, "failed to load proxy keys: %v", err)
	}
	if keys == nil {
		keys = make(map[string]string)
	}
	return &runnerpb.GetUserProxyKeysResponse{Keys: keys}, nil
}

func (c *callbackService) validateSessionOwnership(sessionID, nodeID string) (*dbmodels.Session, error) {
	if sessionID == "" || !IsValidChatID(sessionID) {
		return nil, status.Error(codes.InvalidArgument, "invalid session_id")
	}
	if c.s == nil || c.s.repo == nil {
		return nil, nil
	}
	sess, err := c.s.repo.GetSessionMeta(sessionID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get session: %v", err)
	}
	if sess != nil && sess.RunnerNodeID != "" && sess.RunnerNodeID != nodeID {
		return nil, status.Error(codes.PermissionDenied, "session runner mismatch")
	}
	return sess, nil
}

// InitWorkflowSession implements runnerpb.AgentCallbackServiceServer.
func (c *callbackService) InitWorkflowSession(ctx context.Context, req *runnerpb.InitWorkflowSessionRequest) (*runnerpb.InitWorkflowSessionResponse, error) {
	nodeID, ok := c.nodeID(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unknown runner")
	}
	if req.GetRunnerId() != "" && req.GetRunnerId() != nodeID {
		return nil, status.Error(codes.PermissionDenied, "runner_id mismatch")
	}
	if c.s == nil || c.s.repo == nil {
		return &runnerpb.InitWorkflowSessionResponse{Ok: true}, nil
	}
	sessionID := req.GetSessionId()
	existing, err := c.validateSessionOwnership(sessionID, nodeID)
	if err != nil {
		return nil, err
	}
	title := fmt.Sprintf("Scheduled: %s", req.GetWorkflowName())
	if req.GetWorkflowName() == "" {
		title = fmt.Sprintf("Scheduled: %s", req.GetAgentId())
	}
	currentAgent := req.GetAgentId()
	agentIcon := ""
	if a, err := c.s.findAgentFor(nodeID, currentAgent); err == nil && a != nil {
		agentIcon = a.Config.Icon
	}
	if existing != nil {
		if existing.Title != "" {
			title = existing.Title
		}
		if existing.CurrentAgent != "" {
			currentAgent = existing.CurrentAgent
		}
		if agentIcon == "" {
			agentIcon = existing.AgentIcon
		}
	}
	session := &dbmodels.Session{
		ChatID:       sessionID,
		CurrentAgent: currentAgent,
		AgentIcon:    agentIcon,
		Title:        title,
		RunnerNodeID: nodeID,
	}
	if err := c.s.repo.UpsertSession(session); err != nil {
		log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to upsert workflow session from runner")
		return nil, status.Errorf(codes.Internal, "upsert session: %v", err)
	}
	return &runnerpb.InitWorkflowSessionResponse{Ok: true}, nil
}

// ReportWorkflowEvent implements runnerpb.AgentCallbackServiceServer.
func (c *callbackService) ReportWorkflowEvent(ctx context.Context, req *runnerpb.ReportWorkflowEventRequest) (*runnerpb.ReportWorkflowEventResponse, error) {
	nodeID, ok := c.nodeID(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unknown runner")
	}
	if req.GetRunnerId() != "" && req.GetRunnerId() != nodeID {
		return nil, status.Error(codes.PermissionDenied, "runner_id mismatch")
	}
	if c.s == nil {
		return &runnerpb.ReportWorkflowEventResponse{Ok: true}, nil
	}
	sessionID := req.GetSessionId()
	if _, err := c.validateSessionOwnership(sessionID, nodeID); err != nil {
		return nil, err
	}

	var metadata map[string]any
	if raw := req.GetMetadataJson(); len(raw) > 0 {
		_ = json.Unmarshal(raw, &metadata)
	}

	var ts time.Time
	if req.GetTimestampUnixNano() > 0 {
		ts = time.Unix(0, req.GetTimestampUnixNano())
	} else {
		ts = time.Now()
	}

	ev := workflow.WorkflowEvent{
		Type:       workflow.WorkflowEventType(req.GetEventType()),
		Workflow:   req.GetWorkflow(),
		SessionID:  sessionID,
		NodeID:     req.GetNodeId(),
		NodeType:   workflowspec.NodeType(req.GetNodeType()),
		AgentID:    req.GetAgentId(),
		Status:     workflowspec.NodeStatus(req.GetStatus()),
		SkipReason: workflowspec.SkipReason(req.GetSkipReason()),
		Message:    req.GetMessage(),
		EntryType:  req.GetEntryType(),
		Metadata:   metadata,
		MessageID:  req.GetMessageId(),
		Artifacts:  req.GetArtifacts(),
		Output:     req.GetOutput(),
		AgentName:  req.GetAgentName(),
		CLI:        req.GetCli(),
		Model:      req.GetModel(),
		Timestamp:  ts,
	}

	c.s.handleWorkflowEvent(sessionID, ev)
	return &runnerpb.ReportWorkflowEventResponse{Ok: true}, nil
}

// WorkflowRunStart implements runnerpb.AgentCallbackServiceServer.
func (c *callbackService) WorkflowRunStart(ctx context.Context, req *runnerpb.WorkflowRunStartRequest) (*runnerpb.WorkflowRunStartResponse, error) {
	nodeID, ok := c.nodeID(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unknown runner")
	}
	runID := req.GetRunId()
	if runID == "" {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	sessionID := req.GetSessionId()
	if _, err := c.validateSessionOwnership(sessionID, nodeID); err != nil {
		return nil, err
	}
	if c.s == nil || c.s.workflowRunRepo == nil {
		return &runnerpb.WorkflowRunStartResponse{Ok: true}, nil
	}
	// Check if already settled to avoid resurrection from late retries
	existing, err := c.s.workflowRunRepo.GetRunRow(runID)
	if err == nil && existing != nil && (existing.Status == dbmodels.WorkflowStatusCompleted ||
		existing.Status == dbmodels.WorkflowStatusFailed ||
		existing.Status == dbmodels.WorkflowStatusCancelled) {
		return &runnerpb.WorkflowRunStartResponse{Ok: true}, nil
	}
	err = c.s.workflowRunRepo.SaveRun(&dbmodels.WorkflowRun{
		RunID:             runID,
		SessionID:         sessionID,
		Status:            req.GetStatus(),
		DAGSpec:           req.GetDagSpec(),
		NodeStates:        "{}",
		RunDir:            req.GetRunDir(),
		Input:             req.GetInput(),
		ParentRunID:       req.GetParentRunId(),
		AllowCrossSession: req.GetAllowCrossSession(),
	})
	if err != nil {
		log.Warn().Err(err).Str("run_id", runID).Msg("failed to save workflow run from runner")
		return nil, status.Errorf(codes.Internal, "save workflow run: %v", err)
	}
	return &runnerpb.WorkflowRunStartResponse{Ok: true}, nil
}

// WorkflowRunSettle implements runnerpb.AgentCallbackServiceServer.
func (c *callbackService) WorkflowRunSettle(ctx context.Context, req *runnerpb.WorkflowRunSettleRequest) (*runnerpb.WorkflowRunSettleResponse, error) {
	nodeID, ok := c.nodeID(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "unknown runner")
	}
	runID := req.GetRunId()
	if runID == "" {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	rawStates := req.GetNodeStatesJson()
	if len(rawStates) > 0 && !json.Valid(rawStates) {
		return nil, status.Error(codes.InvalidArgument, "invalid node_states_json")
	}
	if c.s == nil || c.s.workflowRunRepo == nil {
		return &runnerpb.WorkflowRunSettleResponse{Ok: true}, nil
	}
	run, err := c.s.workflowRunRepo.GetRunRow(runID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get workflow run: %v", err)
	}
	if run == nil {
		return &runnerpb.WorkflowRunSettleResponse{Ok: true}, nil
	}
	if _, err := c.validateSessionOwnership(run.SessionID, nodeID); err != nil {
		return nil, err
	}
	// Always release execution guard on run settlement to prevent any leak
	c.s.releaseExecution(run.SessionID)

	switch strings.ToUpper(req.GetStatus()) {
	case workflow.PersistStatusCompleted:
		run.Status = dbmodels.WorkflowStatusCompleted
	case workflow.PersistStatusFailed:
		run.Status = dbmodels.WorkflowStatusFailed
	case workflow.PersistStatusCancelled, "CANCELED":
		run.Status = dbmodels.WorkflowStatusCancelled
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown run status %q", req.GetStatus())
	}
	if len(rawStates) > 0 {
		run.NodeStates = string(rawStates)
	}
	if err := c.s.workflowRunRepo.SaveRun(run); err != nil {
		log.Warn().Err(err).Str("run_id", runID).Msg("failed to settle workflow run from runner")
		return nil, status.Errorf(codes.Internal, "settle workflow run: %v", err)
	}
	return &runnerpb.WorkflowRunSettleResponse{Ok: true}, nil
}

// HoldMergeLock implements runnerpb.AgentCallbackServiceServer. It takes the
// exclusive lock on the main checkout for the merge of a worktree chat, queued
// behind every chat that works there (with or without a worktree). The first
// message tells the runner the lock is held; the lock is released when the
// runner ends the call, the connection drops or the run finishes. Unlike the
// other callbacks it is not deduplicated by call_seq: a retry after a lost
// connection must queue for the lock again, never inherit one that is gone.
// For the same reason it is outside the in-flight caps; runners are trusted to
// open at most one per run, and the lock ends with the run.
func (c *callbackService) HoldMergeLock(req *runnerpb.HoldMergeLockRequest, stream grpc.ServerStreamingServer[runnerpb.HoldMergeLockResponse]) error {
	ctx := stream.Context()
	nodeID, ok := c.nodeID(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "unknown runner")
	}
	runID := req.GetMeta().GetRunId()
	if runID == "" {
		return status.Error(codes.InvalidArgument, "meta.run_id is required")
	}
	info, ok := c.runs.ActiveRun(runID, nodeID)
	if !ok {
		return status.Error(codes.FailedPrecondition, "run is not active on this runner")
	}
	repoDir := req.GetRepoDir()
	if !filepath.IsAbs(repoDir) || filepath.Clean(repoDir) != repoDir || dirlock.IsExemptDir(info.ChatID, repoDir) {
		return status.Errorf(codes.InvalidArgument, "invalid repo_dir %q", repoDir)
	}
	if c.s == nil || c.s.dirLocks == nil {
		return status.Error(codes.FailedPrecondition, "directory locks are not enabled")
	}

	// Only a worktree chat merges, and only into the checkout it was started
	// on: a runner cannot use this call to hold an arbitrary directory.
	if c.s.repo == nil {
		return status.Error(codes.FailedPrecondition, "sessions are not available")
	}
	meta, err := c.s.repo.GetSessionMeta(info.ChatID)
	if err != nil {
		return status.Errorf(codes.Internal, "get session: %v", err)
	}
	if meta == nil || !meta.Worktree {
		return status.Error(codes.FailedPrecondition, "chat does not use a git worktree")
	}
	if want := c.worktreeRepoDir(meta, nodeID, info.AgentID); want == "" || filepath.Clean(want) != repoDir {
		return status.Errorf(codes.PermissionDenied, "repo_dir %q is not the checkout of this chat", repoDir)
	}
	title := meta.Title
	// Give up when the run ends too: a lock must not outlive the run that asked.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if info.Done != nil {
		go func() {
			select {
			case <-info.Done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	release, err := c.s.dirLocks.AcquireMerge(ctx, dirlock.AcquireRequest{
		ChatID:   info.ChatID,
		Title:    title,
		RunnerID: nodeID,
		Dirs:     []string{repoDir},
	})
	if err != nil {
		return status.FromContextError(err).Err()
	}
	defer release()

	if err := stream.Send(&runnerpb.HoldMergeLockResponse{Granted: true}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// worktreeRepoDir is the checkout a worktree chat works on: its run directory,
// or the agent's first literal run directory when none was chosen (as the
// executor derives it). "" if neither exists.
func (c *callbackService) worktreeRepoDir(meta *dbmodels.Session, nodeID, agentID string) string {
	if meta.RunDir != "" {
		return meta.RunDir
	}
	agent, err := c.s.findAgentFor(nodeID, agentID)
	if err != nil || agent == nil {
		return ""
	}
	for _, d := range agent.Config.RunDirs {
		if d != "" && !strings.ContainsAny(d, "*?[") {
			return d
		}
	}
	return ""
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
