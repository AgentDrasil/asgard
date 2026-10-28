// Package conn is the runner side of the runner/backend connection: it dials
// the backend over mTLS, multiplexes the connection with yamux, registers on
// the NodeControlService Connect stream, keeps the ordered and acknowledged
// message exchange alive across reconnects, and hosts RunnerService on the
// streams the backend opens. See pkg/runnerpb/runner.proto for the protocol.
package conn

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/pkg/version"
	"github.com/AgentDrasil/asgard/runner/lib/runs"
)

const (
	// maxGRPCMessageSize is the gRPC message limit in both directions.
	maxGRPCMessageSize = 16 << 20
	// maxControlMessageSize caps one whole control-stream message.
	maxControlMessageSize = 12 << 20
	// maxOutputChunkSize caps RunOutputChunk.data.
	maxOutputChunkSize = 1 << 20

	defaultHeartbeatInterval = 15 * time.Second
	defaultBackoffMin        = 500 * time.Millisecond
	defaultBackoffMax        = 30 * time.Second
	defaultRegisterTimeout   = 15 * time.Second
	defaultDialTimeout       = 10 * time.Second
	defaultMaxQueued         = 4096
	defaultMaxQueuedBytes    = 64 << 20
)

var (
	// ErrNotConnected is returned when no registered backend connection exists.
	ErrNotConnected = errors.New("not connected to backend")
	// ErrTooLarge is returned when a message exceeds the protocol size caps.
	ErrTooLarge = errors.New("message too large")
	// ErrRejected is the error of a session the backend refused to register.
	ErrRejected = errors.New("registration rejected")
)

// Options configures a Client.
type Options struct {
	// Addr is the backend registry address (host:port).
	Addr string
	// NodeID is the runner id; it must equal the CN of the client certificate.
	NodeID string
	// TLSConfig returns the mTLS client configuration; it is called on every
	// (re)connect.
	TLSConfig func() (*tls.Config, error)
	// Version is reported in Register (default: pkg/version).
	Version string
	// SupportedCLIs returns the cli -> models map reported in Register.
	SupportedCLIs func() map[string][]string
	// Quotas returns the quota snapshot sent in Heartbeat (nil = empty).
	Quotas func() []*runnerpb.TargetQuota
	// MaxRuns is the concurrent run capacity reported in Heartbeat.
	MaxRuns uint32
	// Executor runs dispatched attempts (default NotImplementedExecutor).
	Executor Executor
	// Service is the RunnerService served to the backend (default: all
	// methods Unimplemented).
	Service runnerpb.RunnerServiceServer

	HeartbeatInterval time.Duration
	BackoffMin        time.Duration
	BackoffMax        time.Duration
	RegisterTimeout   time.Duration
	// MaxQueued and MaxQueuedBytes bound the unacked upstream queue; senders
	// block while it is full.
	MaxQueued      int
	MaxQueuedBytes int
}

// Client maintains the runner's connection to the backend. Its upstream seq
// counter, unacked queue and downstream dedup state belong to the process (the
// boot id), not to a connection, so they survive reconnects.
type Client struct {
	opts   Options
	bootID string
	runs   *runs.Table
	wake   chan struct{}

	mu             sync.Mutex
	runCtx         context.Context
	queue          []*runnerpb.RunnerMessage
	queueBytes     int
	nextSeq        uint64
	space          chan struct{} // closed (and replaced) whenever the queue shrinks
	lastBackendSeq uint64
	backendBoot    string
	ackDirty       bool
	callbacks      runnerpb.AgentCallbackServiceClient
	// workers holds the pending executor calls of each run_id (guarded by mu).
	workers map[string][]func()
}

// New creates a Client. Call Run to connect.
func New(opts Options) *Client {
	if opts.Version == "" {
		opts.Version = version.Get()
	}
	if opts.Executor == nil {
		opts.Executor = NotImplementedExecutor{}
	}
	if opts.Service == nil {
		opts.Service = runnerpb.UnimplementedRunnerServiceServer{}
	}
	setDefault := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	setDefault(&opts.HeartbeatInterval, defaultHeartbeatInterval)
	setDefault(&opts.BackoffMin, defaultBackoffMin)
	setDefault(&opts.BackoffMax, defaultBackoffMax)
	setDefault(&opts.RegisterTimeout, defaultRegisterTimeout)
	if opts.MaxQueued <= 0 {
		opts.MaxQueued = defaultMaxQueued
	}
	if opts.MaxQueuedBytes <= 0 {
		opts.MaxQueuedBytes = defaultMaxQueuedBytes
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return &Client{
		opts:    opts,
		bootID:  hex.EncodeToString(b),
		runs:    runs.NewTable(),
		wake:    make(chan struct{}, 1),
		space:   make(chan struct{}),
		workers: make(map[string][]func()),
	}
}

// BootID returns this process's random boot id.
func (c *Client) BootID() string { return c.bootID }

// Callbacks returns the AgentCallbackService client of the current
// connection, or ErrNotConnected.
func (c *Client) Callbacks() (runnerpb.AgentCallbackServiceClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.callbacks == nil {
		return nil, ErrNotConnected
	}
	return c.callbacks, nil
}

// Connected reports whether a registered connection is up.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callbacks != nil
}

// Queued returns the number of unacked upstream messages.
func (c *Client) Queued() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// Run connects and stays connected, reconnecting with exponential backoff,
// until ctx is cancelled. Runs started by the backend are not tied to a
// connection and keep going while it is down.
func (c *Client) Run(ctx context.Context) error {
	c.mu.Lock()
	c.runCtx = ctx
	c.mu.Unlock()

	delay := c.opts.BackoffMin
	for ctx.Err() == nil {
		accepted, err := c.session(ctx)
		if accepted {
			delay = c.opts.BackoffMin
		}
		if ctx.Err() != nil {
			break
		}
		wait := jitter(delay)
		log.Warn().Err(err).Dur("retry_in", wait).Msg("backend connection ended")
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
		if delay *= 2; delay > c.opts.BackoffMax {
			delay = c.opts.BackoffMax
		}
	}
	return ctx.Err()
}

// jitter spreads d by up to +-20% so many runners do not reconnect in step.
func jitter(d time.Duration) time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(401))
	if err != nil {
		return d
	}
	return d * time.Duration(800+n.Int64()) / 1000
}

// session runs one connection until it ends. accepted reports whether the
// backend accepted the registration.
func (c *Client) session(ctx context.Context) (accepted bool, err error) {
	tlsCfg, err := c.opts.TLSConfig()
	if err != nil {
		return false, err
	}
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: defaultDialTimeout}, Config: tlsCfg}
	raw, err := dialer.DialContext(ctx, "tcp", c.opts.Addr)
	if err != nil {
		return false, fmt.Errorf("dial backend: %w", err)
	}
	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard
	sess, err := yamux.Client(raw, ycfg)
	if err != nil {
		_ = raw.Close()
		return false, fmt.Errorf("yamux session: %w", err)
	}

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-connCtx.Done()
		_ = sess.Close()
	}()
	defer func() { _ = sess.Close() }()

	// Backend-opened streams: RunnerService.
	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxGRPCMessageSize),
		grpc.MaxSendMsgSize(maxGRPCMessageSize),
	)
	runnerpb.RegisterRunnerServiceServer(srv, c.opts.Service)
	go func() { _ = srv.Serve(sess) }()
	defer srv.Stop()

	// Runner-opened streams: NodeControlService, AgentCallbackService.
	cc, err := grpc.NewClient("passthrough:///backend",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return sess.Open() }),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxGRPCMessageSize),
			grpc.MaxCallSendMsgSize(maxGRPCMessageSize),
		),
	)
	if err != nil {
		return false, fmt.Errorf("grpc client: %w", err)
	}
	defer func() { _ = cc.Close() }()

	stream, err := runnerpb.NewNodeControlServiceClient(cc).Connect(connCtx)
	if err != nil {
		return false, fmt.Errorf("open control stream: %w", err)
	}
	if err := stream.Send(&runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_Register{Register: c.buildRegister()}}); err != nil {
		return false, fmt.Errorf("send register: %w", err)
	}
	timer := time.AfterFunc(c.opts.RegisterTimeout, cancel)
	first, err := stream.Recv()
	timer.Stop()
	if err != nil {
		return false, fmt.Errorf("await register result: %w", err)
	}
	res := first.GetRegisterResult()
	if res == nil {
		return false, errors.New("first backend message was not RegisterResult")
	}
	if !res.GetAccepted() {
		log.Error().Str("reason", res.GetRejectReason()).Uint32("backend_protocol", res.GetProtocolVersion()).Msg("backend rejected runner registration")
		return false, fmt.Errorf("%w: %s", ErrRejected, res.GetRejectReason())
	}
	if res.GetProtocolVersion() != runnerpb.ProtocolVersion {
		return false, fmt.Errorf("protocol version mismatch: backend %d, runner %d", res.GetProtocolVersion(), runnerpb.ProtocolVersion)
	}
	c.attach(res, runnerpb.NewAgentCallbackServiceClient(cc))
	defer c.detach()
	log.Info().Str("backend_boot_id", res.GetBackendBootId()).Msg("registered with backend")

	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer cancel()
		c.writeLoop(connCtx, stream)
	}()
	go func() {
		defer wg.Done()
		c.heartbeatLoop(connCtx)
	}()
	defer cancel()
	return true, c.readLoop(stream)
}

func (c *Client) buildRegister() *runnerpb.Register {
	var clis map[string]*runnerpb.ModelList
	if c.opts.SupportedCLIs != nil {
		clis = make(map[string]*runnerpb.ModelList)
		for cli, models := range c.opts.SupportedCLIs() {
			clis[cli] = &runnerpb.ModelList{Models: models}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	completed := c.completedLocked()
	done := make(map[string]bool, len(completed))
	for _, r := range completed {
		done[r.GetRunId()] = true
	}
	var active []*runnerpb.RunRef
	for _, r := range c.runs.Active() {
		if !done[r.GetRunId()] {
			active = append(active, r)
		}
	}
	return &runnerpb.Register{
		NodeId:                c.opts.NodeID,
		Version:               c.opts.Version,
		ProtocolVersion:       runnerpb.ProtocolVersion,
		SupportedClis:         clis,
		ActiveRuns:            active,
		CompletedUnackedRuns:  completed,
		LastAckedBackendSeq:   c.lastBackendSeq,
		RunnerBootId:          c.bootID,
		LastSeenBackendBootId: c.backendBoot,
	}
}

// completedLocked lists the runs whose RunResult is still queued.
func (c *Client) completedLocked() []*runnerpb.RunRef {
	var out []*runnerpb.RunRef
	for _, m := range c.queue {
		if r := m.GetRunResult(); r != nil {
			out = append(out, &runnerpb.RunRef{
				RunId:          r.GetRunId(),
				ChatId:         r.GetChatId(),
				Attempt:        r.GetAttempt(),
				WorkflowNodeId: r.GetWorkflowNodeId(),
				RunToken:       r.GetRunToken(),
			})
		}
	}
	return out
}

// attach applies an accepted RegisterResult: a changed backend boot id resets
// the downstream dedup state; the backend's last acked seq trims the queue.
func (c *Client) attach(res *runnerpb.RegisterResult, cb runnerpb.AgentCallbackServiceClient) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if res.GetBackendBootId() != c.backendBoot {
		c.backendBoot = res.GetBackendBootId()
		c.lastBackendSeq = 0
	}
	c.trimLocked(res.GetLastAckedRunnerSeq())
	c.callbacks = cb
	c.ackDirty = c.lastBackendSeq > 0
}

func (c *Client) detach() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.callbacks = nil
}

func (c *Client) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// writeLoop sends queued messages in seq order (starting with the replay of
// the unacked queue) and pending Acks. It is the only sender after Register.
func (c *Client) writeLoop(ctx context.Context, stream grpc.BidiStreamingClient[runnerpb.RunnerMessage, runnerpb.BackendMessage]) {
	var sent uint64
	for {
		c.mu.Lock()
		var out []*runnerpb.RunnerMessage
		for _, m := range c.queue {
			if m.GetSeq() > sent {
				out = append(out, m)
			}
		}
		ack, dirty := c.lastBackendSeq, c.ackDirty
		c.ackDirty = false
		c.mu.Unlock()

		for _, m := range out {
			if err := stream.Send(m); err != nil {
				return
			}
			sent = m.GetSeq()
		}
		if dirty {
			if err := stream.Send(&runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_Ack{Ack: &runnerpb.Ack{AckSeq: ack}}}); err != nil {
				return
			}
		}
		select {
		case <-c.wake:
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) heartbeatLoop(ctx context.Context) {
	c.sendHeartbeat()
	t := time.NewTicker(c.opts.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			c.sendHeartbeat()
		case <-ctx.Done():
			return
		}
	}
}

// sendHeartbeat queues a fresh Heartbeat, superseding any still-unacked one:
// only the latest snapshot matters, so offline periods do not fill the queue.
func (c *Client) sendHeartbeat() {
	hb := &runnerpb.Heartbeat{ActiveRuns: uint32(c.runs.Len()), MaxRuns: c.opts.MaxRuns}
	if c.opts.Quotas != nil {
		hb.Quotas = c.opts.Quotas()
	}
	msg := &runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_Heartbeat{Heartbeat: hb}}

	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.queue[:0]
	for _, m := range c.queue {
		if m.GetHeartbeat() != nil {
			c.queueBytes -= proto.Size(m)
			continue
		}
		kept = append(kept, m)
	}
	for i := len(kept); i < len(c.queue); i++ {
		c.queue[i] = nil
	}
	c.queue = kept
	if len(c.queue) >= c.opts.MaxQueued {
		return
	}
	c.appendLocked(msg)
}

// readLoop handles backend messages until the stream ends.
func (c *Client) readLoop(stream grpc.BidiStreamingClient[runnerpb.RunnerMessage, runnerpb.BackendMessage]) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if ack := msg.GetAck(); ack != nil {
			c.mu.Lock()
			c.trimLocked(ack.GetAckSeq())
			c.mu.Unlock()
			continue
		}
		if msg.GetRegisterResult() != nil || msg.GetSeq() == 0 {
			return errors.New("unexpected unsequenced backend message")
		}
		c.mu.Lock()
		dup := msg.GetSeq() <= c.lastBackendSeq
		c.mu.Unlock()
		if !dup {
			c.handle(msg)
		}
		c.mu.Lock()
		if !dup {
			c.lastBackendSeq = msg.GetSeq()
		}
		c.ackDirty = true
		c.mu.Unlock()
		c.signal()
	}
}

// handle applies one new downstream message. It never blocks on the executor.
func (c *Client) handle(msg *runnerpb.BackendMessage) {
	c.mu.Lock()
	ctx := c.runCtx
	c.mu.Unlock()
	switch p := msg.GetPayload().(type) {
	case *runnerpb.BackendMessage_StartRun:
		sr := p.StartRun
		ref := &runnerpb.RunRef{
			RunId:          sr.GetRunId(),
			ChatId:         sr.GetChatId(),
			Attempt:        sr.GetAttempt(),
			WorkflowNodeId: sr.GetStatusScope().GetNodeId(),
			RunToken:       sr.GetStatusScope().GetRunToken(),
		}
		if !c.runs.Begin(ref) {
			return // replayed StartRun of a known run
		}
		// Off the reader goroutine: the executor may block (e.g. on a full
		// unacked queue) and the reader must keep draining Acks.
		c.dispatch(sr.GetRunId(), func() { c.opts.Executor.StartRun(ctx, sr, c) })
	case *runnerpb.BackendMessage_CancelRun:
		id := p.CancelRun.GetRunId()
		c.dispatch(id, func() { c.opts.Executor.CancelRun(id) })
	default:
		log.Warn().Msgf("ignoring unknown backend message %T", p)
	}
}

// Event implements Sink.
func (c *Client) Event(ctx context.Context, ev *runnerpb.RunEvent) error {
	return c.enqueue(ctx, &runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_RunEvent{RunEvent: ev}})
}

// Output implements Sink.
func (c *Client) Output(ctx context.Context, chunk *runnerpb.RunOutputChunk) error {
	if len(chunk.GetData()) > maxOutputChunkSize {
		return fmt.Errorf("%w: output chunk of %d bytes", ErrTooLarge, len(chunk.GetData()))
	}
	return c.enqueue(ctx, &runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_RunOutput{RunOutput: chunk}})
}

// Result implements Sink. The run leaves the active table once its result is
// queued (from then on it is reported as completed-unacked until acked).
func (c *Client) Result(ctx context.Context, res *runnerpb.RunResult) error {
	err := c.enqueue(ctx, &runnerpb.RunnerMessage{Payload: &runnerpb.RunnerMessage_RunResult{RunResult: res}})
	if err == nil {
		c.runs.End(res.GetRunId())
	}
	return err
}

// enqueue assigns the next upstream seq and queues msg for (re)delivery. It
// blocks while the unacked queue is full.
func (c *Client) enqueue(ctx context.Context, msg *runnerpb.RunnerMessage) error {
	size := proto.Size(msg)
	if size > maxControlMessageSize {
		return fmt.Errorf("%w: %d bytes", ErrTooLarge, size)
	}
	for {
		c.mu.Lock()
		if len(c.queue) == 0 || (len(c.queue) < c.opts.MaxQueued && c.queueBytes+size <= c.opts.MaxQueuedBytes) {
			c.appendLocked(msg)
			c.mu.Unlock()
			return nil
		}
		space := c.space
		c.mu.Unlock()
		select {
		case <-space:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Client) appendLocked(msg *runnerpb.RunnerMessage) {
	c.nextSeq++
	msg.Seq = c.nextSeq
	c.queue = append(c.queue, msg)
	c.queueBytes += proto.Size(msg)
	c.signal()
}

// trimLocked drops every queued message with seq <= acked.
func (c *Client) trimLocked(acked uint64) {
	i := 0
	for i < len(c.queue) && c.queue[i].GetSeq() <= acked {
		c.queueBytes -= proto.Size(c.queue[i])
		c.queue[i] = nil
		i++
	}
	if i == 0 {
		return
	}
	c.queue = c.queue[i:]
	close(c.space)
	c.space = make(chan struct{})
}

// dispatch runs fn on the serial worker of runID, so executor calls of one run
// happen in the order the backend sent them, without ever blocking the reader
// or other runs. The worker goroutine exits when its queue is empty.
func (c *Client) dispatch(runID string, fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pending, busy := c.workers[runID]
	c.workers[runID] = append(pending, fn)
	if busy {
		return
	}
	go func() {
		for {
			c.mu.Lock()
			q := c.workers[runID]
			if len(q) == 0 {
				delete(c.workers, runID)
				c.mu.Unlock()
				return
			}
			next := q[0]
			q[0] = nil
			c.workers[runID] = q[1:]
			c.mu.Unlock()
			next()
		}
	}()
}
