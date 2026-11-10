// Package exec is the runner's conn.Executor: it executes the StartRuns the
// backend dispatches in the same bubblewrap sandboxes the backend uses, and
// reports output, status events and the result upstream.
package exec

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper/inject"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/worktree"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/runner/lib/conn"
)

const (
	defaultAgentWait = 30 * time.Second
	defaultAgentPoll = 100 * time.Millisecond

	// outputChunkSize is the RunOutputChunk size (protocol cap: 1 MiB).
	outputChunkSize = 1 << 20
	// maxErrorSize caps free-text errors (RunResult.error_truncated).
	maxErrorSize = 64 << 10
	// ErrCancelled is the RunResult.error of a run stopped by CancelRun.
	ErrCancelled = "run cancelled"
)

// runFunc is run.RunWithOptions; tests substitute it.
type runFunc func(ctx context.Context, agent *agentspec.Agent, candidates []agentspec.CLITarget, prompt string, sessions run.SessionMap, runDirOpt, modelOpt optional.Option[string], chatID string, scope run.StatusScope, opts run.Options) (run.Result, error)

// Options configures an Executor.
type Options struct {
	// Agents is the runner's etcd-backed agent view.
	Agents AgentSource
	// AgentWait bounds how long a run waits for its agent hash to arrive
	// (default 30s); AgentPoll is the polling interval (default 100ms).
	AgentWait time.Duration
	AgentPoll time.Duration
	// Conf returns the sandbox configuration (proxy options, config path to
	// mask). It is required: runs fail while it is nil.
	Conf func() *config.Config
	// Callbacks reaches the backend's AgentCallbackService for the sandbox
	// calls (ask-user, call-peer, find-peer, metrics). Nil rejects them.
	Callbacks CallbacksFunc
	// Ready, when set, is consulted before every run; a non-nil error fails
	// the run (sandbox masking/proxy not healthy).
	Ready func() error

	run runFunc
}

// Executor implements conn.Executor.
type Executor struct {
	opts Options

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	// sockDirs maps a running run to the host view of its per-run socket
	// directory (where aw listens on inject.sock). It only exists while the
	// sandboxes of the current target run.
	sockDirs map[string]string
}

var _ conn.Executor = (*Executor)(nil)

// New creates an Executor.
func New(opts Options) *Executor {
	if opts.AgentWait <= 0 {
		opts.AgentWait = defaultAgentWait
	}
	if opts.AgentPoll <= 0 {
		opts.AgentPoll = defaultAgentPoll
	}
	if opts.run == nil {
		opts.run = run.RunWithOptions
	}
	return &Executor{opts: opts, cancels: make(map[string]context.CancelFunc), sockDirs: make(map[string]string)}
}

// StartRun implements conn.Executor. It returns right away: the run executes
// in its own goroutine so a CancelRun is never stuck behind it.
func (e *Executor) StartRun(ctx context.Context, sr *runnerpb.StartRun, sink conn.Sink) {
	runCtx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.cancels[sr.GetRunId()] = cancel
	e.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			e.mu.Lock()
			delete(e.cancels, sr.GetRunId())
			e.mu.Unlock()
		}()
		res := e.execute(runCtx, sr, sink)
		// The result is delivered even for a cancelled run, so use the
		// process context, not the run's.
		if err := e.report(ctx, sr, res, sink); err != nil {
			log.Error().Err(err).Str("run_id", sr.GetRunId()).Msg("failed to report run result")
		}
	}()
}

// CancelRun implements conn.Executor: it stops the run's sandboxes.
func (e *Executor) CancelRun(runID string) {
	e.mu.Lock()
	cancel := e.cancels[runID]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *Executor) setSockDir(runID, dir string) {
	e.mu.Lock()
	e.sockDirs[runID] = dir
	e.mu.Unlock()
}

func (e *Executor) clearSockDir(runID, dir string) {
	e.mu.Lock()
	if e.sockDirs[runID] == dir {
		delete(e.sockDirs, runID)
	}
	e.mu.Unlock()
}

// InjectMessage implements conn.Executor: it hands prompt to the aw process of
// the run over <sockDir>/inject.sock. Everything that means "the message was
// not delivered" (unknown run, no sock dir yet, socket missing or closed) maps
// to types.ErrNotInjectable so the backend queues the message instead.
func (e *Executor) InjectMessage(ctx context.Context, runID, injectID, prompt string, deadline time.Time) error {
	e.mu.Lock()
	dir, ok := e.sockDirs[runID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: run %q has no socket dir", types.ErrNotInjectable, runID)
	}
	// inject.Send requires a deadline: a frozen aw must not hang the call.
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := inject.Send(ctx, filepath.Join(dir, inject.SocketName), prompt); err != nil {
		if ctx.Err() != nil && !errors.Is(err, types.ErrNotInjectable) {
			// Timed out mid-exchange: the reply was lost, so the message
			// may or may not have been delivered. It is reported as an error
			// (not not_injectable) to say the delivery state is unknown; the
			// backend cannot tell the two apart and queues on any failure.
			log.Warn().Err(err).Str("run_id", runID).Str("inject_id", injectID).Msg("inject timed out")
		}
		return err
	}
	return nil
}

// outcome is what execute learned about a run.
type outcome struct {
	output []byte
	target agentspec.CLITarget
	runDir string
	err    error
}

func (e *Executor) execute(ctx context.Context, sr *runnerpb.StartRun, sink conn.Sink) outcome {
	if e.opts.Ready != nil {
		if err := e.opts.Ready(); err != nil {
			return outcome{err: fmt.Errorf("sandbox not ready: %w", err)}
		}
	}
	var conf *config.Config
	if e.opts.Conf != nil {
		conf = e.opts.Conf()
	}
	if conf == nil {
		return outcome{err: errors.New("sandbox configuration not available")}
	}
	agent, err := resolveAgent(ctx, e.opts.Agents, sr, e.opts.AgentWait, e.opts.AgentPoll)
	if err != nil {
		return outcome{err: err}
	}
	agent = applyOverlay(agent, sr)

	runDir, err := ResolveDirRef(sr.GetChatId(), sr.GetRunDir())
	if err != nil {
		return outcome{err: fmt.Errorf("resolving run dir: %w", err)}
	}
	runDirOpt := optional.None[string]()
	if runDir != "" {
		runDirOpt = optional.Some(runDir)
	}
	// repoDir is the directory the user sees; with a worktree the agent runs
	// in the chat's checkout of it.
	repoDir := runDir
	var plan *worktree.Plan
	worktreeRun := false
	if sr.GetWorktree() {
		// A subagent shares the worktree of the chat that owns it.
		wtChat := sr.GetWorktreeChatId()
		if wtChat == "" {
			wtChat = sr.GetChatId()
		}
		wtAgent, p, err := prepareWorktree(ctx, agent, wtChat, runDir, sr.GetReadonly())
		if err != nil {
			return outcome{err: err}
		}
		agent, repoDir = wtAgent, p.RepoDir
		runDirOpt = optional.Some(p.Path)
		worktreeRun = true
		plan = mergePlan(sr, wtAgent, p)
	}

	candidates := make([]agentspec.CLITarget, 0, len(sr.GetCandidates()))
	for _, c := range sr.GetCandidates() {
		candidates = append(candidates, agentspec.CLITarget{CLI: c.GetCli(), Model: c.GetModel()})
	}
	sessions := run.SessionMap(sr.GetSessions())
	modelOpt := optional.None[string]()
	if sr.GetModel() != "" {
		modelOpt = optional.Some(sr.GetModel())
	}
	scope := run.StatusScope{
		NodeID:            sr.GetStatusScope().GetNodeId(),
		RunToken:          sr.GetStatusScope().GetRunToken(),
		Headless:          sr.GetStatusScope().GetHeadless(),
		AllowCrossSession: sr.GetStatusScope().GetAllowCrossSession(),
	}

	opts := e.runOptions(ctx, sr, agent, conf, sink, plan)
	res, err := e.opts.run(ctx, agent, candidates, sr.GetPrompt(), sessions, runDirOpt, modelOpt, sr.GetChatId(), scope, opts)
	if worktreeRun && res.RunDir != "" {
		res.RunDir = repoDir
	}
	return outcome{output: res.Output, target: res.Target, runDir: res.RunDir, err: err}
}

// runOptions builds the sandbox inputs of a run. The runner's socket is served
// from the run's sock dir: status updates and every other sandbox call
// (ask-user, call-peer, find-peer, metrics) go through it.
func (e *Executor) runOptions(ctx context.Context, sr *runnerpb.StartRun, agent *agentspec.Agent, conf *config.Config, sink conn.Sink, plan *worktree.Plan) run.Options {
	opts := run.Options{
		LangRules:  sr.GetLangRules(),
		ConfigPath: conf.GetConfigPath(),
		Proxy:      conf.SandboxProxyOptions(),
		Env: []string{
			sandboxsock.EnvURL + "=" + SandboxStatusURL,
		},
		OnSockDir: func(sockDir, runDir string) (func(), error) {
			srv := newStatusServer(ctx, sr, agent, runDir, sink)
			srv.callbacks = e.opts.Callbacks
			srv.worktree = plan
			stop, err := srv.serve(sockDir)
			if err != nil {
				return nil, err
			}
			e.setSockDir(sr.GetRunId(), sockDir)
			return func() {
				// Forget the dir before the status server stops and the
				// dir is removed, so no inject dials a vanishing socket.
				// Only drop our own entry.
				e.clearSockDir(sr.GetRunId(), sockDir)
				if stop != nil {
					stop()
				}
			}, nil
		},
	}
	if sr.GetUserId() != "" && e.opts.Callbacks != nil {
		if cbClient, err := e.opts.Callbacks(); err != nil {
			log.Warn().Err(err).Str("user_id", sr.GetUserId()).Msg("failed to get callbacks client for proxy keys")
		} else if cbClient != nil {
			resp, err := cbClient.GetUserProxyKeys(ctx, &runnerpb.GetUserProxyKeysRequest{
				Meta:   &runnerpb.CallMeta{RunId: sr.GetRunId()},
				UserId: sr.GetUserId(),
			})
			if err != nil {
				log.Warn().Err(err).Str("user_id", sr.GetUserId()).Msg("failed to query user proxy keys")
			} else if resp != nil && len(resp.GetKeys()) > 0 {
				if opts.Proxy.EnvVars == nil {
					opts.Proxy.EnvVars = make(map[string]string)
				}
				for k, v := range resp.GetKeys() {
					opts.Proxy.EnvVars[k] = v
					if k == "GEMINI_API_KEY" {
						opts.Proxy.GeminiAPIKey = v
					}
				}
			}
		}
	}
	if enabled := sr.GetEnabledProviders(); len(enabled) > 0 {
		set := make(map[string]bool, len(enabled))
		for _, p := range enabled {
			set[p] = true
		}
		opts.IsProviderEnabled = func(cli string) bool { return set[cli] }
	}
	return opts
}

// report streams the output and finalizes the run.
func (e *Executor) report(ctx context.Context, sr *runnerpb.StartRun, o outcome, sink conn.Sink) error {
	for off := 0; off < len(o.output); off += outputChunkSize {
		end := min(off+outputChunkSize, len(o.output))
		if err := sink.Output(ctx, &runnerpb.RunOutputChunk{RunId: sr.GetRunId(), Offset: uint64(off), Data: o.output[off:end]}); err != nil {
			return fmt.Errorf("sending output: %w", err)
		}
	}
	res := &runnerpb.RunResult{
		RunId:          sr.GetRunId(),
		OutputSize:     uint64(len(o.output)),
		ChatId:         sr.GetChatId(),
		Attempt:        sr.GetAttempt(),
		WorkflowNodeId: sr.GetStatusScope().GetNodeId(),
		RunToken:       sr.GetStatusScope().GetRunToken(),
		RunDir:         o.runDir,
	}
	if o.target != (agentspec.CLITarget{}) {
		res.SelectedTarget = &runnerpb.Target{Cli: o.target.CLI, Model: o.target.Model}
	}
	if o.runDir != "" {
		res.RunDirRef = sr.GetRunDir()
		if res.RunDirRef == nil {
			res.RunDirRef = DirRefFor(sr.GetChatId(), o.runDir)
		}
	}
	if o.err != nil {
		msg := o.err.Error()
		if errors.Is(o.err, context.Canceled) {
			msg = ErrCancelled
		}
		res.Error, res.ErrorTruncated = truncateError(msg)
		var nq *run.NoQuotaError
		if errors.As(o.err, &nq) {
			res.NoQuota = NoQuotaToProto(nq)
		}
	}
	if err := sink.Result(ctx, res); err != nil {
		if ctx.Err() != nil || errors.Is(err, conn.ErrTooLarge) {
			return err
		}
		// The result itself was rejected (e.g. an invalid message): finalize
		// the run with a minimal one so it is not left hanging.
		// It keeps the output size and dirs consistent with the chunks
		// already queued.
		res = &runnerpb.RunResult{
			RunId: res.GetRunId(), ChatId: res.GetChatId(), Attempt: res.GetAttempt(),
			WorkflowNodeId: res.GetWorkflowNodeId(), RunToken: res.GetRunToken(),
			OutputSize: res.GetOutputSize(), SelectedTarget: res.GetSelectedTarget(),
			RunDir: res.GetRunDir(), RunDirRef: res.GetRunDirRef(),
			Error: "failed to report run result: " + strings.ToValidUTF8(err.Error(), "\uFFFD"),
		}
		return sink.Result(ctx, res)
	}
	return nil
}

// truncateError makes msg valid UTF-8 and cuts it to 64 KiB on a rune boundary.
func truncateError(msg string) (string, bool) {
	msg = strings.ToValidUTF8(msg, "\uFFFD")
	if len(msg) <= maxErrorSize {
		return msg, false
	}
	return strings.TrimSpace(truncateUTF8(msg, maxErrorSize)), true
}

// truncateUTF8 cuts s to at most max bytes without splitting a rune.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// NoQuotaToProto converts the backend's quota error to its wire form.
func NoQuotaToProto(e *run.NoQuotaError) *runnerpb.NoQuota {
	nq := &runnerpb.NoQuota{
		AgentId:       e.AgentID,
		ExplicitModel: e.ExplicitModel,
		MinThreshold:  e.MinThreshold,
		PairingNote:   e.PairingNote,
	}
	for _, t := range e.Targets {
		nq.Targets = append(nq.Targets, &runnerpb.TargetQuota{Cli: t.CLI, Model: t.Model, Remaining: t.Remaining, Enabled: t.Enabled, Reason: t.Reason})
	}
	return nq
}

// mergePlan returns the plan /merge works on, or nil when the run may not
// merge. Only a writer delivers work, so only it may merge, and a subagent
// (it shares the worktree of the chat that owns it, WorktreeChatId) never
// does: the owner delivers. A subagent's sandbox learns that too (it is told
// not to run asgard-merge).
func mergePlan(sr *runnerpb.StartRun, agent *agentspec.Agent, p worktree.Plan) *worktree.Plan {
	if sr.GetWorktreeChatId() != "" {
		if wr := agent.Config.WorktreeRun; wr != nil {
			wr.Shared = true
		}
		return nil
	}
	if sr.GetReadonly() {
		return nil
	}
	return &p
}
