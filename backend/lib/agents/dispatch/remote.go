package dispatch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"uuid"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// cancelGrace bounds how long a cancelled run waits for the runner to report
// its (cancelled) result before giving up.
const cancelGrace = 10 * time.Second

// Handle is a dispatched run (*runnerhub.Run).
type Handle interface {
	Next(ctx context.Context) (*runnerpb.RunEvent, error)
	Wait(ctx context.Context) (*runnerhub.Result, error)
}

// Hub is the part of the runner hub the Remote dispatcher uses.
type Hub interface {
	Dispatch(ctx context.Context, nodeID string, sr *runnerpb.StartRun) (Handle, error)
	Cancel(nodeID, runID string) error
}

type hubAdapter struct{ *runnerhub.Hub }

func (a hubAdapter) Dispatch(ctx context.Context, nodeID string, sr *runnerpb.StartRun) (Handle, error) {
	return a.Hub.Dispatch(ctx, nodeID, sr)
}

// FromHub adapts the runner hub.
func FromHub(h *runnerhub.Hub) Hub { return hubAdapter{h} }

// RunnerResolver returns the runner a chat is bound to.
type RunnerResolver func(chatID string) (string, error)

// Remote dispatches runs to the runner of the chat through the runner hub.
type Remote struct {
	hub     Hub
	runner  RunnerResolver
	conf    *config.Config
	newRun  func() string
	timeout time.Duration
}

// NewRemote creates a Remote dispatcher. conf supplies the provider switches
// and language rules the runner applies.
func NewRemote(hub Hub, runner RunnerResolver, conf *config.Config) *Remote {
	return &Remote{hub: hub, runner: runner, conf: conf, newRun: func() string { return uuid.NewV7().String() }, timeout: cancelGrace}
}

// Run implements Dispatcher: it builds a StartRun with a fresh run id,
// dispatches it, feeds the RunEvents to req.OnEvent and maps the RunResult.
func (d *Remote) Run(ctx context.Context, req Request) (Result, error) {
	nodeID, err := d.runner(req.ChatID)
	if err != nil {
		return Result{}, NotDispatched(err)
	}
	if nodeID == "" {
		return Result{}, NotDispatched(fmt.Errorf("chat %s is not bound to a runner", req.ChatID))
	}
	sr, err := d.StartRun(req)
	if err != nil {
		return Result{}, NotDispatched(err)
	}
	sr.RunId = d.newRun()

	handle, err := d.hub.Dispatch(ctx, nodeID, sr)
	if err != nil {
		if errors.Is(err, runnerhub.ErrTooLarge) {
			return Result{}, fmt.Errorf("prompt too large to dispatch: %w", err)
		}
		return Result{}, NotDispatched(fmt.Errorf("dispatching run to runner %s: %w", nodeID, err))
	}

	pumpCtx, stopPump := context.WithCancel(context.Background())
	defer stopPump()
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		for {
			ev, err := handle.Next(pumpCtx)
			if err != nil {
				return
			}
			if req.OnEvent != nil {
				req.OnEvent(ev)
			}
		}
	}()

	res, err := handle.Wait(ctx)
	if err != nil && ctx.Err() != nil {
		// Context cancellation: stop the run on the runner and give it a
		// moment to report, so the sandboxes are gone when Run returns.
		if cerr := d.hub.Cancel(nodeID, sr.GetRunId()); cerr != nil {
			log.Warn().Err(cerr).Str("run_id", sr.GetRunId()).Msg("failed to cancel run on runner")
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), d.timeout)
		defer cancel()
		_, _ = handle.Wait(waitCtx)
		stopPump()
		<-pumped
		return Result{}, ctx.Err()
	}
	if err != nil {
		stopPump()
		<-pumped
		if errors.Is(err, runnerhub.ErrRunLost) {
			return Result{}, fmt.Errorf("runner %s lost the run: %w", nodeID, err)
		}
		return Result{}, err
	}

	// Every event precedes the result: the pump ends on io.EOF once it has
	// delivered them all.
	<-pumped
	return mapResult(ctx, res)
}

func mapResult(ctx context.Context, res *runnerhub.Result) (Result, error) {
	rr := res.RunResult
	out := Result{Output: res.Output, RunDir: rr.GetRunDir()}
	if t := rr.GetSelectedTarget(); t != nil {
		out.Target.CLI, out.Target.Model = t.GetCli(), t.GetModel()
	}
	switch {
	case rr.GetNoQuota() != nil:
		return out, noQuotaFromProto(rr.GetNoQuota())
	case rr.GetError() != "":
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New(rr.GetError())
	}
	return out, nil
}

// StartRun builds the StartRun of req (without a run id). It is exported for
// tests.
func (d *Remote) StartRun(req Request) (*runnerpb.StartRun, error) {
	if req.Agent == nil {
		return nil, errors.New("dispatch: no agent")
	}
	sr := &runnerpb.StartRun{
		ChatId:    req.ChatID,
		AgentId:   req.Agent.Config.ID,
		AgentHash: req.Agent.Hash,
		Prompt:    req.Prompt,
		Model:     req.Model.TakeOr(""),
		Sessions:  map[string]string(req.Sessions),
		RunDir:    dirRefFor(req.ChatID, req.RunDir.TakeOr("")),
		Attempt:   req.Attempt,
		RunDirs:   append([]string(nil), req.Agent.Config.RunDirs...),
		StatusScope: &runnerpb.StatusScope{
			NodeId:            req.Scope.NodeID,
			RunToken:          req.Scope.RunToken,
			Headless:          req.Scope.Headless,
			AllowCrossSession: req.Scope.AllowCrossSession,
		},
		MountDirsReadonly:  append([]string(nil), req.Agent.Config.MountDirs.ReadOnly...),
		MountDirsReadwrite: append([]string(nil), req.Agent.Config.MountDirs.ReadWrite...),
	}
	if req.Agent.NodeID != "" {
		sr.AgentSource = runnerpb.AgentSource_AGENT_SOURCE_RUNNER
	}
	for _, c := range req.Candidates {
		sr.Candidates = append(sr.Candidates, &runnerpb.Target{Cli: c.CLI, Model: c.Model})
	}
	if d.conf != nil {
		for _, p := range config.SupportedProviders {
			if d.conf.IsProviderEnabled(p) {
				sr.EnabledProviders = append(sr.EnabledProviders, p)
			}
		}
		if len(sr.EnabledProviders) == 0 {
			return nil, errors.New("no CLI provider is enabled in configuration")
		}
		sr.LangRules = d.conf.LanguageRules()
	}
	return sr, nil
}

// dirRefFor expresses a backend-side run directory as a DirRef: the chat's own
// tmp and session dirs become TMP/SESSION refs (the runner maps them onto its
// own layout), everything else is passed as an absolute runner path. The
// backend never sends its own host paths for per-chat dirs.
func dirRefFor(chatID, dir string) *runnerpb.DirRef {
	if dir == "" {
		return nil
	}
	if chatID != "" {
		for _, c := range []struct {
			scope runnerpb.FileScope
			base  string
		}{
			{runnerpb.FileScope_FILE_SCOPE_TMP, agents.GetSessionTmpBaseDir(chatID)},
			{runnerpb.FileScope_FILE_SCOPE_SESSION, agents.GetSessionScopedBaseDir("session", chatID)},
		} {
			if dir == c.base {
				return &runnerpb.DirRef{Scope: c.scope}
			}
			if rel, ok := strings.CutPrefix(dir, c.base+string(filepath.Separator)); ok {
				return &runnerpb.DirRef{Scope: c.scope, RelOrPath: rel}
			}
		}
	}
	return &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: dir}
}
