// Package runnerhub is the backend side of the runner connection: an mTLS
// listener that multiplexes each runner connection with yamux, hosts the
// backend gRPC services (NodeControlService, AgentCallbackService) on the
// streams the runner opens, and dials the runner-hosted RunnerService over
// streams the backend opens. See pkg/runnerpb/runner.proto for the protocol.
package runnerhub

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

const (
	// maxGRPCMessageSize is the gRPC message limit in both directions.
	maxGRPCMessageSize = 16 << 20
	// maxControlMessageSize caps one whole control-stream message.
	maxControlMessageSize = 12 << 20
	// maxPromptSize caps StartRun.prompt.
	maxPromptSize = 4 << 20
	// handshakeTimeout bounds the TLS handshake and the wait for Register.
	handshakeTimeout = 15 * time.Second
)

var (
	// ErrRunnerOffline is returned when the target runner is not connected.
	ErrRunnerOffline = errors.New("runner offline")
	// ErrRunLost is the failure of a run whose runner restarted or no longer
	// knows it; the caller may re-dispatch it as a new attempt.
	ErrRunLost = errors.New("run lost")
	// ErrHubClosed is returned once the hub is stopped.
	ErrHubClosed = errors.New("runner hub closed")
	// ErrUnknownRun is returned when cancelling a run the hub does not track.
	ErrUnknownRun = errors.New("unknown run")
	// ErrDuplicateRun is returned when dispatching an already tracked run_id.
	ErrDuplicateRun = errors.New("duplicate run id")
	// ErrTooLarge is returned when a StartRun exceeds the protocol size caps.
	ErrTooLarge = errors.New("start run too large")
	// ErrAlreadyFinalized is returned when dispatching a run_id whose result
	// was already finalized; attempts need a fresh run_id.
	ErrAlreadyFinalized = errors.New("run id already finalized")
)

// FinalizedStore persists finalized run ids so a RunResult is applied at most
// once per run_id across backend restarts.
type FinalizedStore interface {
	// MarkFinalized records runID and reports whether it was newly recorded.
	MarkFinalized(ctx context.Context, runID string) (bool, error)
	// IsFinalized reports whether runID was finalized before.
	IsFinalized(ctx context.Context, runID string) (bool, error)
}

// Options configures a Hub.
type Options struct {
	TLSCertFile  string
	TLSKeyFile   string
	ClientCAFile string
	// Addr is the TCP listen address (e.g. ":9311"). Ignored when Listener is set.
	Addr string
	// Listener overrides Addr; the hub wraps it with TLS.
	Listener net.Listener
	// AllowedNodes returns the currently allowed runner ids (client cert CNs).
	// It is consulted on every connection, so config reloads take effect.
	AllowedNodes func() []string
	// Store persists finalized run ids; nil keeps them in memory only.
	Store FinalizedStore
	// Callbacks serves AgentCallbackService; nil answers Unimplemented.
	// Handlers can call NodeIDFromContext to learn the calling runner.
	Callbacks runnerpb.AgentCallbackServiceServer
	// OnNodeOnline, when set, is called (in its own goroutine) every time a
	// runner completes registration, so the host can resume work that waited
	// for it (e.g. queued chat messages).
	OnNodeOnline func(nodeID string)
	// RegisterTimeout bounds how long a connection may take to complete the
	// Register exchange (default 15s).
	RegisterTimeout time.Duration
}

// Hub accepts runner connections and tracks runner nodes and their runs.
type Hub struct {
	opts   Options
	bootID string
	store  FinalizedStore
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	callbacks runnerpb.AgentCallbackServiceServer
	nodes     map[string]*node
	runs      map[string]*Run
	conns     map[*connection]struct{}
	// raw tracks accepted connections still in the TLS handshake.
	raw    map[net.Conn]struct{}
	ln     net.Listener
	closed bool
	wg     sync.WaitGroup
}

// New creates a Hub. Call Start to begin accepting connections.
func New(opts Options) *Hub {
	if opts.RegisterTimeout <= 0 {
		opts.RegisterTimeout = handshakeTimeout
	}
	if opts.Callbacks == nil {
		opts.Callbacks = runnerpb.UnimplementedAgentCallbackServiceServer{}
	}
	store := opts.Store
	if store == nil {
		store = newMemoryStore()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		callbacks: opts.Callbacks,
		opts:      opts,
		bootID:    newBootID(),
		store:     store,
		ctx:       ctx,
		cancel:    cancel,
		nodes:     make(map[string]*node),
		runs:      make(map[string]*Run),
		conns:     make(map[*connection]struct{}),
		raw:       make(map[net.Conn]struct{}),
	}
}

func newBootID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Start loads the TLS material and starts accepting runner connections.
func (h *Hub) Start() error {
	tlsCfg, err := h.tlsConfig()
	if err != nil {
		return err
	}
	ln := h.opts.Listener
	if ln == nil {
		ln, err = net.Listen("tcp", h.opts.Addr)
		if err != nil {
			return fmt.Errorf("runner listener: %w", err)
		}
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = ln.Close()
		return ErrHubClosed
	}
	h.ln = ln
	h.mu.Unlock()

	log.Info().Str("addr", ln.Addr().String()).Msg("runner hub listening")
	h.wg.Add(1)
	go h.acceptLoop(ln, tlsCfg)
	return nil
}

// Addr returns the listen address, or nil before Start.
func (h *Hub) Addr() net.Addr {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ln == nil {
		return nil
	}
	return h.ln.Addr()
}

// Stop closes the listener and all runner connections and fails every
// pending run with ErrHubClosed.
func (h *Hub) Stop() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	ln := h.ln
	conns := make([]*connection, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	rawConns := make([]net.Conn, 0, len(h.raw))
	for rc := range h.raw {
		rawConns = append(rawConns, rc)
	}
	for id, r := range h.runs {
		r.finish(nil, ErrHubClosed)
		delete(h.runs, id)
	}
	h.mu.Unlock()

	h.cancel()
	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.sess.Close()
	}
	for _, rc := range rawConns {
		_ = rc.Close()
	}
	h.wg.Wait()
}

func (h *Hub) tlsConfig() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(h.opts.TLSCertFile, h.opts.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load runner tls keypair: %w", err)
	}
	caPEM, err := os.ReadFile(h.opts.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read runner client ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("runner client ca %s has no certificates", h.opts.ClientCAFile)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func (h *Hub) acceptLoop(ln net.Listener, tlsCfg *tls.Config) {
	defer h.wg.Done()
	var delay time.Duration
	for {
		raw, err := ln.Accept()
		if err != nil {
			h.mu.Lock()
			closed := h.closed
			h.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			// Temporary failure (e.g. out of file descriptors): back off and
			// retry, like net/http's Server.Serve.
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else if delay *= 2; delay > time.Second {
				delay = time.Second
			}
			log.Warn().Err(err).Dur("retry_in", delay).Msg("runner hub accept failed")
			select {
			case <-time.After(delay):
			case <-h.ctx.Done():
				return
			}
			continue
		}
		delay = 0
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			_ = raw.Close()
			return
		}
		h.raw[raw] = struct{}{}
		h.wg.Add(1)
		h.mu.Unlock()
		go func() {
			defer h.wg.Done()
			defer func() {
				h.mu.Lock()
				delete(h.raw, raw)
				h.mu.Unlock()
			}()
			h.handleConn(raw, tlsCfg)
		}()
	}
}

func (h *Hub) isAllowed(nodeID string) bool {
	if h.opts.AllowedNodes == nil {
		return false
	}
	for _, n := range h.opts.AllowedNodes() {
		if n == nodeID {
			return true
		}
	}
	return false
}

// handleConn authenticates one raw connection and serves its yamux session
// until it ends.
func (h *Hub) handleConn(raw net.Conn, tlsCfg *tls.Config) {
	tc := tls.Server(raw, tlsCfg)
	_ = tc.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := tc.Handshake(); err != nil {
		log.Warn().Err(err).Str("remote", raw.RemoteAddr().String()).Msg("runner tls handshake failed")
		_ = tc.Close()
		return
	}
	_ = tc.SetDeadline(time.Time{})

	peers := tc.ConnectionState().PeerCertificates
	if len(peers) == 0 {
		_ = tc.Close()
		return
	}
	nodeID := peers[0].Subject.CommonName
	if !h.isAllowed(nodeID) {
		log.Warn().Str("node_id", nodeID).Msg("runner connection rejected: node not allowed")
		_ = tc.Close()
		return
	}

	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	sess, err := yamux.Server(tc, cfg)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("runner yamux session failed")
		_ = tc.Close()
		return
	}
	h.serveSession(nodeID, sess)
}

// serveSession hosts the backend gRPC services on sess and blocks until the
// session ends.
func (h *Hub) serveSession(nodeID string, sess *yamux.Session) {
	cc, err := grpc.NewClient("passthrough:///"+nodeID,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return sess.Open() }),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxGRPCMessageSize),
			grpc.MaxCallSendMsgSize(maxGRPCMessageSize),
		),
	)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("runner grpc client failed")
		_ = sess.Close()
		return
	}
	c := &connection{
		hub:    h,
		nodeID: nodeID,
		sess:   sess,
		cc:     cc,
		runner: runnerpb.NewRunnerServiceClient(cc),
		wake:   make(chan struct{}, 1),
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = cc.Close()
		_ = sess.Close()
		return
	}
	h.conns[c] = struct{}{}
	h.mu.Unlock()

	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxGRPCMessageSize),
		grpc.MaxSendMsgSize(maxGRPCMessageSize),
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (resp any, err error) {
			defer recoverToStatus(info.FullMethod, &err)
			return next(withNodeID(ctx, nodeID), req)
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) (err error) {
			defer recoverToStatus(info.FullMethod, &err)
			return next(srv, &nodeStream{ServerStream: ss, ctx: withNodeID(ss.Context(), nodeID)})
		}),
	)
	runnerpb.RegisterNodeControlServiceServer(srv, &nodeControl{conn: c})
	runnerpb.RegisterAgentCallbackServiceServer(srv, callbackProxy{h: h})

	// A connection that never registers must not linger.
	timer := time.AfterFunc(h.opts.RegisterTimeout, func() {
		if !c.registered.Load() {
			_ = sess.Close()
		}
	})

	_ = srv.Serve(sess)
	timer.Stop()
	srv.Stop()
	_ = cc.Close()
	h.detach(c)
}

// recoverToStatus turns a handler panic into codes.Internal so a bad runner
// request can never crash the backend.
func recoverToStatus(method string, err *error) {
	if r := recover(); r != nil {
		log.Error().Interface("panic", r).Str("method", method).Bytes("stack", debug.Stack()).Msg("panic in runner gRPC handler")
		*err = status.Error(codes.Internal, "internal error")
	}
}

// RefreshAllowed re-reads the allowed node set and closes the sessions of
// nodes that are no longer allowed. Call it after the allowed set changes.
func (h *Hub) RefreshAllowed() {
	h.mu.Lock()
	var drop []*connection
	for c := range h.conns {
		if !h.isAllowed(c.nodeID) {
			drop = append(drop, c)
		}
	}
	h.mu.Unlock()
	for _, c := range drop {
		log.Info().Str("node_id", c.nodeID).Msg("runner no longer allowed, closing connection")
		_ = c.sess.Close()
	}
}

// detach marks the node offline if c is still its current connection. Pending
// runs are kept: they wait for the runner to reconnect.
func (h *Hub) detach(c *connection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, c)
	if n := h.nodes[c.nodeID]; n != nil && n.conn == c {
		n.conn = nil
		n.online = false
		log.Info().Str("node_id", c.nodeID).Msg("runner disconnected")
	}
}

// Dispatch queues sr for the runner nodeID and returns a handle for its
// events and result. It fails fast with ErrRunnerOffline when the node is not
// connected. The StartRun is sent on the ordered control stream and replayed
// after a reconnect with an unchanged runner boot id; if the runner restarted
// the run finishes with ErrRunLost. ctx only bounds the call itself; use
// Cancel to stop a dispatched run.
func (h *Hub) Dispatch(ctx context.Context, nodeID string, sr *runnerpb.StartRun) (*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sr.GetRunId() == "" {
		return nil, errors.New("start run: empty run_id")
	}
	if len(sr.GetPrompt()) > maxPromptSize || proto.Size(sr) > maxControlMessageSize {
		return nil, fmt.Errorf("%w: run %s", ErrTooLarge, sr.GetRunId())
	}

	done, err := h.store.IsFinalized(ctx, sr.GetRunId())
	if err != nil {
		return nil, fmt.Errorf("check finalized run %s: %w", shortID(sr.GetRunId()), err)
	}
	if done {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyFinalized, shortID(sr.GetRunId()))
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrHubClosed
	}
	n := h.nodes[nodeID]
	if n == nil || !n.online || !h.isAllowed(nodeID) {
		return nil, fmt.Errorf("%w: %s", ErrRunnerOffline, shortID(nodeID))
	}
	if _, dup := h.runs[sr.GetRunId()]; dup {
		return nil, fmt.Errorf("%w: %s", ErrDuplicateRun, sr.GetRunId())
	}
	r := newRun(sr.GetRunId(), nodeID, sr)
	h.runs[sr.GetRunId()] = r
	n.enqueueLocked(&runnerpb.BackendMessage{Payload: &runnerpb.BackendMessage_StartRun{StartRun: sr}})
	return r, nil
}

// Cancel asks the runner to cancel a dispatched run. The CancelRun is queued
// even if the node is currently offline and replayed on reconnect.
func (h *Hub) Cancel(nodeID, runID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.nodes[nodeID]
	r := h.runs[runID]
	if n == nil || r == nil || r.nodeID != nodeID || !h.isAllowed(nodeID) {
		return fmt.Errorf("%w: %s", ErrUnknownRun, shortID(runID))
	}
	n.enqueueLocked(&runnerpb.BackendMessage{Payload: &runnerpb.BackendMessage_CancelRun{CancelRun: &runnerpb.CancelRun{RunId: runID}}})
	return nil
}

// SetCallbacks installs the AgentCallbackService handler. It takes effect for
// all connections, also those already established.
func (h *Hub) SetCallbacks(cb runnerpb.AgentCallbackServiceServer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.callbacks = cb
}

// callbackProxy serves AgentCallbackService on every connection by delegating
// to the handler installed at call time.
type callbackProxy struct {
	runnerpb.UnimplementedAgentCallbackServiceServer
	h *Hub
}

func (p callbackProxy) AskUser(ctx context.Context, req *runnerpb.AskUserRequest) (*runnerpb.AskUserResponse, error) {
	return p.h.currentCallbacks().AskUser(ctx, req)
}

func (p callbackProxy) CallPeer(ctx context.Context, req *runnerpb.CallPeerRequest) (*runnerpb.CallPeerResponse, error) {
	return p.h.currentCallbacks().CallPeer(ctx, req)
}

func (p callbackProxy) FindPeer(ctx context.Context, req *runnerpb.FindPeerRequest) (*runnerpb.FindPeerResponse, error) {
	return p.h.currentCallbacks().FindPeer(ctx, req)
}

func (p callbackProxy) RecordMetrics(ctx context.Context, req *runnerpb.RecordMetricsRequest) (*runnerpb.RecordMetricsResponse, error) {
	return p.h.currentCallbacks().RecordMetrics(ctx, req)
}

func (h *Hub) currentCallbacks() runnerpb.AgentCallbackServiceServer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.callbacks
}

// RunInfo describes an active run as the hub tracks it. Callback handlers
// derive the identity of a sandbox call from it, never from the request.
type RunInfo struct {
	RunID          string
	NodeID         string
	ChatID         string
	AgentID        string
	WorkflowNodeID string
	RunToken       string
	Attempt        uint32
	// Done is closed once the run finished (result, loss or hub stop).
	Done <-chan struct{}
}

// ActiveRun returns the run runID if it is active and executes on nodeID.
func (h *Hub) ActiveRun(runID, nodeID string) (RunInfo, bool) {
	h.mu.Lock()
	r := h.runs[runID]
	h.mu.Unlock()
	if r == nil || r.nodeID != nodeID {
		return RunInfo{}, false
	}
	return RunInfo{
		RunID:          r.runID,
		NodeID:         r.nodeID,
		ChatID:         r.start.GetChatId(),
		AgentID:        r.start.GetAgentId(),
		WorkflowNodeID: r.start.GetStatusScope().GetNodeId(),
		RunToken:       r.start.GetStatusScope().GetRunToken(),
		Attempt:        r.start.GetAttempt(),
		Done:           r.done,
	}, true
}

// Client returns the RunnerService client of an online node. It is bound to
// the node's current connection and stops working when that connection ends.
func (h *Hub) Client(nodeID string) (runnerpb.RunnerServiceClient, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.nodes[nodeID]
	if n == nil || !n.online || n.conn == nil || !h.isAllowed(nodeID) {
		return nil, fmt.Errorf("%w: %s", ErrRunnerOffline, shortID(nodeID))
	}
	return n.conn.runner, nil
}

// NodeInfo is a snapshot of one runner node.
type NodeInfo struct {
	NodeID string
	Online bool
	// OnlineGen increases with every registration of the node; comparing two
	// values tells whether it reconnected in between.
	OnlineGen       uint64
	LastSeen        time.Time
	Version         string
	ProtocolVersion uint32
	RunnerBootID    string
	// ActiveRuns counts the runs the hub is tracking for the node.
	ActiveRuns    int
	MaxRuns       uint32
	SupportedCLIs map[string][]string
	// Quotas is the latest heartbeat quota snapshot.
	Quotas []*runnerpb.TargetQuota
}

// Nodes returns a snapshot of all known and allowed nodes, sorted by id.
func (h *Hub) Nodes() []NodeInfo {
	active := make(map[string]int)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.runs {
		active[r.nodeID]++
	}
	out := make([]NodeInfo, 0, len(h.nodes))
	seen := make(map[string]bool, len(h.nodes))
	for id, n := range h.nodes {
		seen[id] = true
		info := NodeInfo{
			NodeID:          id,
			Online:          n.online && h.isAllowed(id),
			OnlineGen:       n.onlineGen,
			LastSeen:        n.lastSeen,
			Version:         n.version,
			ProtocolVersion: n.protocolVersion,
			RunnerBootID:    n.runnerBootID,
			ActiveRuns:      active[id],
			MaxRuns:         n.maxRuns,
			SupportedCLIs:   make(map[string][]string, len(n.supportedCLIs)),
		}
		for cli, models := range n.supportedCLIs {
			info.SupportedCLIs[cli] = append([]string(nil), models...)
		}
		for _, q := range n.quotas {
			info.Quotas = append(info.Quotas, proto.Clone(q).(*runnerpb.TargetQuota))
		}
		out = append(out, info)
	}
	if h.opts.AllowedNodes != nil {
		for _, id := range h.opts.AllowedNodes() {
			if !seen[id] {
				seen[id] = true
				out = append(out, NodeInfo{NodeID: id, SupportedCLIs: map[string][]string{}})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}
