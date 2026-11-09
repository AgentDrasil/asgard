package exec

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/worktree"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// prepareWorktree creates (or finds) the chat's worktree of repoDir and returns
// the agent to run in it: its only run directory is the worktree, so the main
// checkout is never mounted writable. repoDir must be one of the agent's run
// directories; the worktree root comes from the agent's own configuration,
// never from the request.
func prepareWorktree(ctx context.Context, agent *agentspec.Agent, chatID, repoDir string) (*agentspec.Agent, worktree.Plan, error) {
	cfg := agent.Config
	if !cfg.Worktree.Enabled {
		return nil, worktree.Plan{}, fmt.Errorf("agent %s does not offer worktrees", cfg.ID)
	}
	if repoDir == "" {
		for _, d := range cfg.RunDirs {
			if !strings.ContainsAny(d, "*?[") {
				repoDir = d
				break
			}
		}
	}
	if repoDir == "" || !run.IsAllowedDir(repoDir, cfg.RunDirs) {
		return nil, worktree.Plan{}, fmt.Errorf("run directory %q is not allowed by agent configuration", repoDir)
	}
	// The backend locks the worktree path it derives from the run directory
	// as written; a symlinked path would put the lock and the worktree on
	// different directories.
	real, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		return nil, worktree.Plan{}, fmt.Errorf("resolving run directory: %w", err)
	}
	if real != filepath.Clean(repoDir) {
		return nil, worktree.Plan{}, fmt.Errorf("run directory %q is a symlink to %q: worktrees need the real path", repoDir, real)
	}
	for _, rw := range cfg.MountDirs.EffectiveReadWriteEntries() {
		if pathsOverlap(rw.Path, real) {
			return nil, worktree.Plan{}, fmt.Errorf("readwrite mount %q overlaps %q: it would expose the main checkout to the worktree run", rw.Path, real)
		}
	}
	plan, err := worktree.PlanFor(cfg.Worktree.Root, real, chatID, runnerpaths.SessionDir(chatID))
	if err != nil {
		return nil, worktree.Plan{}, err
	}
	wr, err := plan.Ensure(ctx)
	if err != nil {
		return nil, worktree.Plan{}, fmt.Errorf("preparing worktree: %w", err)
	}
	eff := *agent
	eff.Config.RunDirs = []string{plan.Path}
	eff.Config.WorktreeRun = wr
	return &eff, plan, nil
}

func pathsOverlap(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// handleMerge serves POST /merge of the agent sandbox: it takes the exclusive
// lock on the main checkout through the backend (queued behind every chat that
// works there), fast-forwards the checkout to the worktree branch and answers
// with what the agent has to do next.
func (s *statusServer) handleMerge(w http.ResponseWriter, r *http.Request) {
	reply := func(code int, st, msg string, ok bool) {
		writeJSON(w, code, sandboxsock.MergeResponse{Status: st, Message: msg, OK: ok})
	}
	if s.worktree == nil {
		reply(http.StatusBadRequest, "", "This run has no worktree, so there is nothing to merge.", false)
		return
	}
	if s.callbacks == nil {
		reply(http.StatusServiceUnavailable, "", "Merging is not available: the runner has no backend connection.", false)
		return
	}
	seq := s.nextCallSeq()
	ctx, cancel := s.callContext(r)
	defer cancel()

	// A lost connection means the lock may be gone, so the hold is a single
	// attempt: unlike other callbacks it is never retried.
	cb, err := s.callbacks()
	if err != nil {
		reply(http.StatusServiceUnavailable, "", "Merging is not available right now (no backend connection). Try again shortly.", false)
		return
	}
	holdCtx, stopHold := context.WithCancel(ctx)
	defer stopHold()
	stream, err := cb.HoldMergeLock(holdCtx, &runnerpb.HoldMergeLockRequest{Meta: s.meta(seq), RepoDir: s.worktree.RepoDir})
	if err != nil {
		s.failMerge(w, ctx, err)
		return
	}
	if resp, err := stream.Recv(); err != nil || !resp.GetGranted() {
		if err == nil {
			err = errors.New("lock was not granted")
		}
		s.failMerge(w, ctx, err)
		return
	}

	// From here the main checkout is ours until the stream ends; if it breaks
	// the merge is abandoned.
	mergeCtx, cancelMerge := context.WithCancel(ctx)
	defer cancelMerge()
	go func() {
		_, _ = stream.Recv() // returns when the backend drops the lock or we cancel
		cancelMerge()
	}()

	res, err := s.worktree.Merge(mergeCtx)
	stopHold()
	if err != nil {
		log.Warn().Err(err).Str("run_id", s.sr.GetRunId()).Msg("worktree merge failed")
		msg, _ := truncateError(err.Error())
		reply(http.StatusInternalServerError, "", "The merge failed: "+msg, false)
		return
	}
	ok := res.Status == worktree.MergeMerged || res.Status == worktree.MergeNothing
	reply(http.StatusOK, string(res.Status), res.Guidance(), ok)
}

func (s *statusServer) failMerge(w http.ResponseWriter, ctx context.Context, err error) {
	log.Warn().Err(err).Str("run_id", s.sr.GetRunId()).Msg("failed to take the merge lock")
	if ctx.Err() != nil {
		http.Error(w, "cancelled", http.StatusServiceUnavailable)
		return
	}
	msg, _ := truncateError(status.Convert(err).Message())
	writeJSON(w, http.StatusBadGateway, sandboxsock.MergeResponse{Message: "Could not take the lock on the main checkout: " + msg})
}
