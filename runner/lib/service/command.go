package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/bwrap"
	"github.com/AgentDrasil/asgard/backend/lib/cmdexec"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// Bounds of RunCommand. Output flows to the backend as it is produced; the
// runner only keeps what a resuming backend may ask for again.
const (
	// cmdFrameSize is the largest StreamData slice per frame (far below the
	// protocol's 12 MiB message cap).
	cmdFrameSize = 256 << 10
	// cmdBacklog bounds the output produced but not yet streamed; a command
	// writing faster than the backend reads is held back (pipe backpressure).
	cmdBacklog = 4 << 20
	// cmdKeep is the already streamed output kept for replay after a reset.
	cmdKeep = 4 << 20
	// defaultDetachGrace is how long a command survives without an attached
	// stream (a reset connection needs time to be re-established).
	defaultDetachGrace = 30 * time.Second
	// maxCommands bounds the commands held by the runner (running and cached).
	maxCommands = 128
)

// errCommandKilled is returned to the process' output writers once the
// command was killed.
var errCommandKilled = errors.New("command killed")

// ring is the replay buffer of one output stream: it holds the bytes
// [start, start+len(data)). sent is the offset up to which the attached
// stream has delivered (rewound to the backend's resume offset on attach).
type ring struct {
	start uint64
	data  []byte
	sent  uint64
}

func (b *ring) end() uint64 { return b.start + uint64(len(b.data)) }

// trim drops history older than cmdKeep behind the sent offset.
func (b *ring) trim() {
	if b.sent <= b.start+cmdKeep {
		return
	}
	drop := min(b.sent-cmdKeep-b.start, uint64(len(b.data)))
	b.data = b.data[drop:]
	b.start += drop
}

// cmdRun is one RunCommand request: the process, its buffered output and the
// outcome, shared by every stream attached to the request_id.
type cmdRun struct {
	key string

	mu      sync.Mutex
	changed chan struct{} // closed and replaced on every change
	out     ring
	errb    ring
	done    bool
	exit    *runnerpb.RunCommandExit
	failure error
	killed  bool
	// gen identifies the latest attached stream; older streams are aborted.
	gen      int
	attached int
	grace    *time.Timer
	cancel   context.CancelFunc
	expiry   time.Time
}

func newCmdRun(key string, cancel context.CancelFunc) *cmdRun {
	return &cmdRun{key: key, changed: make(chan struct{}), cancel: cancel}
}

// notifyLocked wakes every waiter. Callers hold r.mu.
func (r *cmdRun) notifyLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// kill stops the command (idempotent).
func (r *cmdRun) kill() {
	r.mu.Lock()
	if !r.killed {
		r.killed = true
		r.notifyLocked()
	}
	r.mu.Unlock()
	r.cancel()
}

// stream is an io.Writer feeding one ring. It blocks while the unsent backlog
// is full, so a slow or absent reader throttles the command.
type cmdWriter struct {
	r *cmdRun
	b *ring
}

func (w cmdWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		w.r.mu.Lock()
		for !w.r.killed && w.b.end()-w.b.sent >= cmdBacklog {
			ch := w.r.changed
			w.r.mu.Unlock()
			<-ch
			w.r.mu.Lock()
		}
		if w.r.killed {
			w.r.mu.Unlock()
			return n, errCommandKilled
		}
		k := min(len(p), int(cmdBacklog-(w.b.end()-w.b.sent)))
		w.b.data = append(w.b.data, p[:k]...)
		w.b.trim()
		w.r.notifyLocked()
		w.r.mu.Unlock()
		p = p[k:]
		n += k
	}
	return n, nil
}

// cmdRegistry holds the commands by (chat, request_id). Completed commands
// stay for the idempotency TTL so a retry gets the cached result.
type cmdRegistry struct {
	ttl time.Duration
	max int

	mu   sync.Mutex
	runs map[string]*cmdRun
}

func newCmdRegistry(ttl time.Duration, max int) *cmdRegistry {
	if ttl <= 0 {
		ttl = defaultIdemTTL
	}
	if max <= 0 {
		max = maxCommands
	}
	return &cmdRegistry{ttl: ttl, max: max, runs: make(map[string]*cmdRun)}
}

func (g *cmdRegistry) get(key string) *cmdRun {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	return g.runs[key]
}

// add registers a new run, or returns the one already there. It fails when
// the runner holds too many commands.
func (g *cmdRegistry) add(r *cmdRun) (*cmdRun, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	if cur := g.runs[r.key]; cur != nil {
		return cur, false, nil
	}
	if len(g.runs) >= g.max {
		// Make room by dropping the oldest completed command.
		var oldest *cmdRun
		var oldestExp time.Time
		for _, c := range g.runs {
			c.mu.Lock()
			done, exp := c.done, c.expiry
			c.mu.Unlock()
			if done && (oldest == nil || exp.Before(oldestExp)) {
				oldest, oldestExp = c, exp
			}
		}
		if oldest == nil {
			return nil, false, status.Error(codes.ResourceExhausted, "too many commands running on this runner")
		}
		delete(g.runs, oldest.key)
	}
	g.runs[r.key] = r
	return r, true, nil
}

func (g *cmdRegistry) expireLocked() {
	now := time.Now()
	for k, c := range g.runs {
		c.mu.Lock()
		gone := c.done && now.After(c.expiry)
		c.mu.Unlock()
		if gone {
			delete(g.runs, k)
		}
	}
}

// cmdParams is a validated RunCommand request.
type cmdParams struct {
	chatID     string
	command    string
	dir        string // real path of the working directory
	cwd        string // the directory as the sandbox sees it
	readonly   []string
	sandbox    bool
	allowCross bool
	timeout    time.Duration
	outputFile string
	tmp        *runnerpb.DirRef
	env        []string
}

// filterEnv drops the runner's own settings from the environment commands
// inherit.
func filterEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "ASGARD_RUNNER_") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// RunCommand implements RunnerService: it runs a workflow command node and
// streams its output. Streams of one request_id share a single execution: a
// retry attaches to the in-flight command (or gets the cached result) and
// resumes the output at the offsets it already received. A stream that goes
// away does not kill the command at once, so a reset connection can resume;
// the command is killed when no stream reattaches within the grace period,
// when the backend asks (cancel), or on the timeout.
func (s *Service) RunCommand(req *runnerpb.RunCommandRequest, stream runnerpb.RunnerService_RunCommandServer) error {
	if !agents.IsValidChatID(req.GetChatId()) {
		return status.Error(codes.InvalidArgument, "invalid session id")
	}
	key := req.GetChatId() + "/" + req.GetRequestId()
	if req.GetCancel() {
		// Explicit cancel of an in-flight request (idempotent).
		if r := s.cmds.get(key); r != nil && req.GetRequestId() != "" {
			r.kill()
		}
		return nil
	}
	if req.GetRequestId() != "" {
		if r := s.cmds.get(key); r != nil {
			return s.attach(stream, r, req.GetResumeStdoutOffset(), req.GetResumeStderrOffset())
		}
		if req.GetResumed() {
			// The command is gone (runner restart, expired): never run it again.
			return status.Error(codes.NotFound, "command state is gone; it cannot be resumed")
		}
	}
	p, err := s.prepareCommand(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := newCmdRun(key, cancel)
	if req.GetRequestId() != "" {
		cur, created, err := s.cmds.add(r)
		if err != nil {
			cancel()
			return err
		}
		if !created {
			cancel()
			return s.attach(stream, cur, req.GetResumeStdoutOffset(), req.GetResumeStderrOffset())
		}
	}
	go s.execute(ctx, r, p)
	return s.attach(stream, r, 0, 0)
}

// prepareCommand validates the request and resolves its directories through
// the confined roots. Nothing is trusted: the working directory must lie
// beneath an allowed root (symlinks and ".." that leave it are refused) and
// is created if needed.
func (s *Service) prepareCommand(req *runnerpb.RunCommandRequest) (*cmdParams, error) {
	chat := req.GetChatId()
	if strings.TrimSpace(req.GetCommand()) == "" {
		return nil, status.Error(codes.InvalidArgument, "command is required")
	}
	// Sandboxed unless the runner's operator allowed otherwise; the request
	// alone can never turn the sandbox off.
	sandbox := req.Sandbox == nil || req.GetSandbox()
	var roDirs []string
	if sandbox {
		// A malformed readonly mount cannot be enforced: refuse (fail closed).
		var rerr error
		if roDirs, rerr = s.readonlyDirsChecked(); rerr != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "invalid readonly mount configuration: %v", rerr)
		}
		if s.ready == nil {
			return nil, status.Error(codes.FailedPrecondition, "sandbox not ready: no sandbox is configured on this runner")
		}
		if err := s.ready(); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "sandbox not ready: %v", err)
		}
		if s.sandboxConf == nil || s.sandboxConf() == nil {
			return nil, status.Error(codes.FailedPrecondition, "sandbox not ready: sandbox configuration not available")
		}
	} else if !s.allowUnsandboxed {
		return nil, status.Error(codes.PermissionDenied, "unsandboxed commands are not enabled on this runner")
	}

	rd, err := runDirString(chat, req.GetRunDir())
	if err != nil {
		return nil, grpcErr(err)
	}
	fs := s.fsFor(chat)
	wd := req.GetWorkingDir()
	ref := runnerfs.PathRef{RunDir: rd, Path: wd, Scope: "workspace"}
	switch {
	case wd == "":
		ref.Path = "."
	case filepath.IsAbs(wd):
		ref.Scope = ""
	}
	if rd == "" && wd == "" {
		return nil, status.Error(codes.InvalidArgument, "run dir is required")
	}
	dir, err := fs.EnsureDir(ref)
	if err != nil {
		return nil, grpcErr(err)
	}

	of := req.GetOutputFile()
	if of != "" && !filepath.IsAbs(of) {
		if !filepath.IsLocal(of) {
			return nil, status.Error(codes.InvalidArgument, "output_file escapes the tmp dir")
		}
		if req.GetTmp() == nil {
			return nil, status.Error(codes.InvalidArgument, "tmp dir is required for a relative output_file")
		}
		if _, err := runDirString(chat, req.GetTmp()); err != nil {
			return nil, grpcErr(err)
		}
	}

	cwd := dir
	if sandbox {
		if cwd, err = bwrap.SandboxCwd(chat, dir, req.GetAllowCrossSession(), roDirs); err != nil {
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
	}
	env := filterEnv(os.Environ())
	if sandbox {
		env = append(env, "ASGARD_TMP_DIR=/tmp", "ASGARD_SESSION_DIR=/session")
	} else {
		env = append(env, "ASGARD_TMP_DIR="+agents.GetSessionTmpBaseDir(chat), "ASGARD_SESSION_DIR="+agents.GetSessionScopedBaseDir("session", chat))
	}
	env = append(env, "ASGARD_CHAT_ID="+chat)
	p := &cmdParams{
		chatID: chat, command: req.GetCommand(), dir: dir, cwd: cwd, readonly: roDirs, sandbox: sandbox, allowCross: req.GetAllowCrossSession(),
		outputFile: of, tmp: req.GetTmp(), env: env,
	}
	if t := req.GetTimeoutSeconds(); t > 0 {
		p.timeout = time.Duration(t) * time.Second
	}
	if sandbox {
		p.env = append(p.env, "ASGARD_SANDBOXED=1")
	}
	return p, nil
}

// execute runs the command and records the outcome.
func (s *Service) execute(ctx context.Context, r *cmdRun, p *cmdParams) {
	defer r.cancel()
	runCtx := ctx
	if p.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	stdout, stderr := cmdWriter{r, &r.out}, cmdWriter{r, &r.errb}
	var code int
	var err error
	if p.sandbox {
		conf := s.sandboxConf()
		if conf == nil {
			err = status.Error(codes.FailedPrecondition, "sandbox not ready: sandbox configuration not available")
			code = -1
		} else {
			code, err = s.runSandboxed(runCtx, cmdexec.SandboxSpec{
				Command: p.command, Dir: p.dir, Cwd: p.cwd, ChatID: p.chatID,
				ConfigPath: conf.GetConfigPath(), Proxy: conf.SandboxProxyOptions(),
				AllowCrossSession: p.allowCross, ReadonlyDirs: p.readonly, Env: p.env,
			}, stdout, stderr)
		}
	} else {
		code, err = cmdexec.RunDirect(runCtx, p.command, p.dir, p.env, stdout, stderr)
	}

	r.mu.Lock()
	killed := r.killed
	r.mu.Unlock()
	switch {
	case killed:
		err = status.Error(codes.Canceled, "command cancelled")
	case runCtx.Err() != nil && ctx.Err() == nil:
		// Timed out: reported like a killed process (exit -1), as locally.
		code, err = -1, nil
	case err != nil:
		err = status.Error(codes.Internal, err.Error())
	}

	exit := &runnerpb.RunCommandExit{Code: int32(code)}
	if err == nil && p.outputFile != "" {
		if a := s.outputArtifact(context.Background(), p); a != nil {
			exit.Artifacts = []*runnerpb.Artifact{a}
		}
	}
	r.mu.Lock()
	r.done = true
	r.exit, r.failure = exit, err
	r.expiry = time.Now().Add(s.cmds.ttl)
	r.notifyLocked()
	r.mu.Unlock()
}

// outputArtifact reports the declared output file when the command produced
// it, like the backend's collectArtifact: the name is the output_file, the
// viewer path is the file API form of its location.
func (s *Service) outputArtifact(ctx context.Context, p *cmdParams) *runnerpb.Artifact {
	of := p.outputFile
	var ref runnerfs.PathRef
	var abs string
	if filepath.IsAbs(of) {
		abs = filepath.Clean(of)
		ref = runnerfs.PathRef{Path: abs}
	} else {
		switch p.tmp.GetScope() {
		case runnerpb.FileScope_FILE_SCOPE_TMP, runnerpb.FileScope_FILE_SCOPE_SESSION:
			ns, prefix := "tmp", "/tmp"
			if p.tmp.GetScope() == runnerpb.FileScope_FILE_SCOPE_SESSION {
				ns, prefix = "session", "/session"
			}
			sub := filepath.Join(p.tmp.GetRelOrPath(), of)
			ref = runnerfs.PathRef{Path: prefix + "/" + sub}
			abs = filepath.Join(agents.GetSessionScopedBaseDir(ns, p.chatID), sub)
		default:
			base, err := runDirString(p.chatID, p.tmp)
			if err != nil {
				return nil
			}
			ref = runnerfs.PathRef{RunDir: base, Path: of, Scope: "workspace"}
			abs = filepath.Join(base, of)
		}
	}
	st, err := s.fsFor(p.chatID).Stat(ctx, ref)
	if err != nil || !st.Exists {
		return nil
	}
	viewer := agents.ViewerArtifactPath(abs, agents.GetSessionTmpBaseDir(p.chatID), agents.GetSessionScopedBaseDir("session", p.chatID))
	return &runnerpb.Artifact{ViewerPath: viewer, Name: of, AbsPath: abs}
}

// attach streams the run's output from the given offsets, then its exit
// frame. A newer attach supersedes this one. When the last stream goes away
// the command gets the grace period to be reattached.
func (s *Service) attach(stream runnerpb.RunnerService_RunCommandServer, r *cmdRun, so, se uint64) error {
	ctx := stream.Context()
	r.mu.Lock()
	if so < r.out.start || so > r.out.end() || se < r.errb.start || se > r.errb.end() {
		r.mu.Unlock()
		return status.Error(codes.OutOfRange, "output at the resume offset is no longer buffered")
	}
	r.gen++
	my := r.gen
	if r.grace != nil {
		r.grace.Stop()
		r.grace = nil
	}
	r.attached++
	r.out.sent, r.errb.sent = so, se
	r.notifyLocked()
	r.mu.Unlock()
	defer s.detach(r)

	for {
		r.mu.Lock()
		if r.gen != my {
			r.mu.Unlock()
			return status.Error(codes.Aborted, "superseded by a newer stream for this request")
		}
		var frames []*runnerpb.RunCommandFrame
		take := func(b *ring) *runnerpb.StreamData {
			if b.sent >= b.end() {
				return nil
			}
			from := b.sent - b.start
			to := min(uint64(len(b.data)), from+cmdFrameSize)
			sd := &runnerpb.StreamData{Offset: b.sent, Data: append([]byte(nil), b.data[from:to]...)}
			b.sent += to - from
			b.trim()
			return sd
		}
		if sd := take(&r.out); sd != nil {
			frames = append(frames, &runnerpb.RunCommandFrame{Payload: &runnerpb.RunCommandFrame_Stdout{Stdout: sd}})
		}
		if sd := take(&r.errb); sd != nil {
			frames = append(frames, &runnerpb.RunCommandFrame{Payload: &runnerpb.RunCommandFrame_Stderr{Stderr: sd}})
		}
		if len(frames) > 0 {
			r.notifyLocked() // the writers may be waiting for room
			r.mu.Unlock()
			for _, f := range frames {
				if err := stream.Send(f); err != nil {
					return err
				}
			}
			continue
		}
		if r.done {
			exit, failure := r.exit, r.failure
			r.mu.Unlock()
			if failure != nil {
				return failure
			}
			return stream.Send(&runnerpb.RunCommandFrame{Payload: &runnerpb.RunCommandFrame_Exit{Exit: exit}})
		}
		ch := r.changed
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

// detach records that a stream is gone. The command is killed when no stream
// is attached after the grace period (immediately when it has no request_id,
// which cannot be resumed).
func (s *Service) detach(r *cmdRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attached--
	if r.attached > 0 || r.done || r.grace != nil {
		return
	}
	g := s.detachGrace
	if strings.HasSuffix(r.key, "/") {
		g = 0
	}
	r.grace = time.AfterFunc(g, func() {
		r.mu.Lock()
		stale := r.attached > 0 || r.done
		r.mu.Unlock()
		if !stale {
			r.kill()
		}
	})
}

// Close kills every running command (the runner is shutting down).
func (s *Service) Close() {
	s.cmds.mu.Lock()
	runs := make([]*cmdRun, 0, len(s.cmds.runs))
	for _, r := range s.cmds.runs {
		runs = append(runs, r)
	}
	s.cmds.mu.Unlock()
	for _, r := range runs {
		r.kill()
	}
}
