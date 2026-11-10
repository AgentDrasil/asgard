// Package dispatch is the backend's seam for executing an agent run. The
// production implementation (Remote) hands every run to the runner the chat is
// bound to; Local executes in-process and exists for tests and embedded use.
package dispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// Request describes one attempt of an agent run. Every Dispatcher.Run call is
// one attempt and gets its own run id; callers that retry (quota decisions,
// corrective retries) call Run again with the next Attempt.
type Request struct {
	// ChatID is the chat the run belongs to; it selects the runner.
	ChatID string
	// UserID identifies the user owning the session for personal proxy keys.
	UserID string
	// Agent is the effective agent (workflow overlay already applied to its
	// config). Its Hash and NodeID identify the definition to the runner.
	Agent      *agentspec.Agent
	Candidates []agentspec.CLITarget
	Prompt     string
	Sessions   run.SessionMap
	// RunDir is the working directory as the backend knows it (a per-chat
	// tmp/session dir or an absolute runner path).
	RunDir optional.Option[string]
	Model  optional.Option[string]
	Scope  run.StatusScope
	// Worktree runs the agent in the chat's own git worktree of RunDir.
	Worktree bool
	// WorktreeChatID is the chat that owns the worktree when Worktree is set
	// and it is not ChatID: a subagent shares its ancestor's worktree.
	WorktreeChatID string
	// SharedChatID is the root chat of the spawn tree the chat belongs to; the
	// runner binds the tree's scratch directory at /shared. Empty: none.
	SharedChatID string
	// Readonly mounts the checkout read-only (a read-only subagent).
	Readonly bool
	// Attempt is the 1-based attempt counter of the logical run.
	Attempt uint32
	// OnEvent receives the RunEvents of the run, in order, before Run
	// returns. It may be nil.
	OnEvent func(*runnerpb.RunEvent)
	// OnStarted is called once the run is registered as the active run of its
	// chat and agent (the runner has it). It may be nil.
	OnStarted func()
}

// Result is the outcome of a run. Target and RunDir are set as far as the run
// got, also when it failed.
type Result struct {
	Output []byte
	Target agentspec.CLITarget
	// RunDir is the resolved working directory on the runner.
	RunDir string
}

// ErrNotDispatched marks a failure that happened before the run reached a
// runner (no runner, offline, not bound, bad request): nothing executed, so
// callers keep queued work and only tell the user. Classify with errors.Is;
// the cause stays reachable too.
var ErrNotDispatched = errors.New("run not dispatched")

// NotDispatched wraps cause as an ErrNotDispatched failure.
func NotDispatched(cause error) error {
	return fmt.Errorf("%w: %w", ErrNotDispatched, cause)
}

// Dispatcher executes agent runs. A quota shortage is reported as
// *run.NoQuotaError.
type Dispatcher interface {
	Run(ctx context.Context, req Request) (Result, error)
}

// Local executes runs in-process through run.RunWithOptions. The backend does
// not use it in production; tests use it to exercise the full flow without a
// runner.
type Local struct {
	conf *config.Config
}

// NewLocal creates a Local dispatcher using conf for provider switches and
// sandbox options.
func NewLocal(conf *config.Config) *Local { return &Local{conf: conf} }

// Run implements Dispatcher.
func (l *Local) Run(ctx context.Context, req Request) (Result, error) {
	if req.Worktree {
		return Result{}, NotDispatched(errors.New("worktrees need a runner"))
	}
	res, err := run.RunWithOptions(ctx, req.Agent, req.Candidates, req.Prompt, req.Sessions, req.RunDir, req.Model, req.ChatID, req.Scope, run.OptionsFromConfig(l.conf))
	return Result{Output: res.Output, Target: res.Target, RunDir: res.RunDir}, err
}

// Unavailable rejects every run with Err. The backend uses it when no runner
// hub is configured.
type Unavailable struct{ Err error }

// Run implements Dispatcher.
func (u Unavailable) Run(context.Context, Request) (Result, error) {
	return Result{}, NotDispatched(u.Err)
}

// noQuotaFromProto converts the wire quota snapshot.
func noQuotaFromProto(nq *runnerpb.NoQuota) *run.NoQuotaError {
	e := &run.NoQuotaError{
		AgentID:       nq.GetAgentId(),
		ExplicitModel: nq.GetExplicitModel(),
		MinThreshold:  nq.GetMinThreshold(),
		PairingNote:   nq.GetPairingNote(),
	}
	for _, t := range nq.GetTargets() {
		e.Targets = append(e.Targets, run.QuotaTargetStatus{CLI: t.GetCli(), Model: t.GetModel(), Remaining: t.GetRemaining(), Enabled: t.GetEnabled(), Reason: t.GetReason()})
	}
	return e
}
