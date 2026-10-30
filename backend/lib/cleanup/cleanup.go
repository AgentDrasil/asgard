package cleanup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/paths"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

const (
	defaultCronTimeHour   = 0
	defaultCronTimeMinute = 0
	defaultCronTimeSecond = 0
)

// Scheduler manages scheduled background tasks.
type Scheduler struct {
	scheduler   gocron.Scheduler
	repo        *dbmodels.SessionRepository
	tmpBase     string
	sessionBase string
	runners     Runners
	now         func() time.Time
}

// Runners is the runner hub view the cleanup needs. With none configured the
// scheduler cleans this host's directories (runner-less development setup).
type Runners interface {
	Client(nodeID string) (runnerpb.RunnerServiceClient, error)
	Nodes() []runnerhub.NodeInfo
}

// WithRunners makes the scheduler ask the runners to clean up their
// directories instead of touching local ones.
func WithRunners(r Runners) SchedulerOption {
	return func(s *Scheduler) { s.runners = r }
}

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*Scheduler)

// WithTmpBase configures a custom temporary base directory for session cleanup.
func WithTmpBase(path string) SchedulerOption {
	return func(s *Scheduler) {
		s.tmpBase = path
	}
}

// WithSessionBase configures a custom persistent session base directory for
// session cleanup.
func WithSessionBase(path string) SchedulerOption {
	return func(s *Scheduler) {
		s.sessionBase = path
	}
}

// NewScheduler creates and starts a gocron scheduler.
func NewScheduler(repo *dbmodels.SessionRepository, opts ...SchedulerOption) (*Scheduler, error) {
	if repo == nil {
		return nil, fmt.Errorf("session repository cannot be nil")
	}

	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("failed to create gocron scheduler: %w", err)
	}

	cs := &Scheduler{
		scheduler: s,
		repo:      repo,
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(cs)
	}

	// Schedule daily task at 00:00 (midnight) with SingletonMode to prevent overlapping runs
	_, err = s.NewJob(
		gocron.DailyJob(
			1,
			gocron.NewAtTimes(
				gocron.NewAtTime(defaultCronTimeHour, defaultCronTimeMinute, defaultCronTimeSecond),
			),
		),
		gocron.NewTask(cs.CleanExpiredSessions),
		gocron.WithSingletonMode(gocron.LimitModeReschedule),
	)
	if err != nil {
		_ = s.Shutdown()
		return nil, fmt.Errorf("failed to schedule daily session cleanup job: %w", err)
	}
	log.Info().Msg("Scheduled daily session cleanup job at 00:00")

	s.Start()
	return cs, nil
}

// localRoots are the backend host's own per-chat roots. With runners the
// chats live on the runners, but chats created before the runner split left
// directories here; they are reclaimed best-effort and never created.
func (cs *Scheduler) localRoots() runnerfs.CleanupRoots {
	r := runnerfs.DefaultCleanupRoots()
	if cs.tmpBase != "" {
		r.Tmp = cs.tmpBase
		r.CA = filepath.Join(cs.tmpBase, paths.CABundleDirName)
	}
	if cs.sessionBase != "" {
		r.Sessions = cs.sessionBase
	}
	return r
}

// reclaimLocal removes the legacy local dirs of chats whose record was just
// deleted. It validates ids, never follows symlinks and only logs failures.
func (cs *Scheduler) reclaimLocal(chats ...string) {
	res := cs.localRoots().Cleanup(runnerfs.CleanupSpec{ChatIDs: chats})
	for _, f := range res.Failures {
		log.Warn().Str("chatID", f.ChatID).Str("error", f.Err).Msg("Failed to remove legacy local session dir")
	}
}

// decommissionedGrace is how long past the expiry cutoff a session bound to a
// runner the hub does not know at all is kept before its record is deleted
// without the runner's confirmation (90 days since the last update).
const decommissionedGrace = 90 * 24 * time.Hour

// finalizedRunRetention is how long finalized run ids are remembered. The
// dedupe only matters for results replayed by a runner after a reconnect or
// backend restart (hours to days); a replay of a pruned id is merely logged
// and ignored when the run is untracked.
const finalizedRunRetention = 90 * 24 * time.Hour

// rpcTimeout bounds one cleanup RPC (a large orphan sweep walks directory
// trees on the runner).
const rpcTimeout = 10 * time.Minute

// CleanExpiredSessions cleans inactive sessions older than 1 month.
func (cs *Scheduler) CleanExpiredSessions() {
	if cs.repo == nil {
		return
	}
	cutoff := cs.now().AddDate(0, -1, 0)
	log.Info().Time("cutoff", cutoff).Msg("Starting scheduled session cleanup for sessions inactive since 1 month ago")
	var err error
	if cs.runners == nil {
		// Runner-less (development) setup: the chat dirs are on this host.
		err = cs.repo.CleanExpiredSessions(dbmodels.CleanExpiredSessionsOptions{
			Cutoff:      cutoff,
			TmpBase:     cs.tmpBase,
			SessionBase: cs.sessionBase,
		})
	} else {
		err = cs.cleanRunners(context.Background(), cutoff)
	}
	if n, perr := cs.repo.PruneFinalizedRuns(cs.now().Add(-finalizedRunRetention)); perr != nil {
		log.Warn().Err(perr).Msg("Failed to prune finalized run ids")
	} else if n > 0 {
		log.Info().Int64("pruned", n).Msg("Pruned old finalized run ids")
	}
	if err != nil {
		log.Error().Err(err).Msg("Scheduled session cleanup encountered errors")
	} else {
		log.Info().Msg("Scheduled session cleanup completed successfully")
	}
}

// cleanRunners is the cleanup with runners configured. The backend DB decides
// what is expired and what is kept; the runners own the directories and
// remove them, so the backend touches no local tmp/sessions directory.
//
// An expired session is deleted from the DB only after its runner confirmed
// the removal of the chat's directories. When the runner is offline (or
// reported a failure for the chat) the record is KEPT and the chat is
// retried by the next day's run: simpler and safer than a journal of pending
// removals, and the record is what lets the next run find the directories
// again. Sessions bound to no runner have no known directories: only their
// record is deleted (any stray directory is caught by the orphan sweep once
// the record is gone).
func (cs *Scheduler) cleanRunners(ctx context.Context, cutoff time.Time) error {
	var errs []error
	expired, err := cs.repo.ExpiredIdleSessions(cutoff)
	if err != nil {
		return err
	}
	byRunner := make(map[string][]string)
	updated := make(map[string]time.Time)
	for _, sess := range expired {
		switch {
		case sess.RunnerNodeID == "" || !agents.IsValidChatID(sess.ChatID):
			// Nothing can exist on a runner for it (invalid ids are refused
			// by runners and cannot name a per-chat directory).
			if err := cs.repo.DeleteSessionRecord(sess.ChatID); err != nil {
				errs = append(errs, fmt.Errorf("delete session %s: %w", sess.ChatID, err))
			} else {
				cs.reclaimLocal(sess.ChatID)
			}
		default:
			byRunner[sess.RunnerNodeID] = append(byRunner[sess.RunnerNodeID], sess.ChatID)
			updated[sess.ChatID] = sess.UpdatedAt
		}
	}
	known := make(map[string]bool)
	for _, n := range cs.runners.Nodes() {
		known[n.NodeID] = true
	}
	for node, chats := range byRunner {
		client, err := cs.runners.Client(node)
		if err != nil {
			if !known[node] {
				// A decommissioned runner never comes back to confirm the
				// removal; after a long grace the record is dropped anyway.
				grace := cs.now().Add(-decommissionedGrace)
				for _, chat := range chats {
					if !updated[chat].Before(grace) {
						continue
					}
					log.Warn().Str("runner", node).Str("chatID", chat).Msg("Deleting expired session of an unknown runner; its data may be left behind on that runner")
					if err := cs.repo.DeleteSessionRecord(chat); err != nil {
						errs = append(errs, fmt.Errorf("delete session %s: %w", chat, err))
					} else {
						cs.reclaimLocal(chat)
					}
				}
			}
			log.Warn().Err(err).Str("runner", node).Int("chats", len(chats)).Msg("Runner offline: expired sessions are kept and retried next run")
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
		out, err := runnerfs.Cleanup(rctx, client, runnerfs.CleanupRequest{ChatIDs: chats})
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("runner %s cleanup: %w", node, err))
			continue
		}
		for _, chat := range chats {
			if fs := out.Failures[chat]; len(fs) > 0 {
				errs = append(errs, fmt.Errorf("runner %s: remove chat %s: %s", node, chat, strings.Join(fs, "; ")))
				continue
			}
			if err := cs.repo.DeleteSessionRecord(chat); err != nil {
				errs = append(errs, fmt.Errorf("delete session %s: %w", chat, err))
			} else {
				cs.reclaimLocal(chat)
			}
		}
	}

	// Orphan sweep on every online runner. keep is every chat id left in the
	// DB (not just the runner's own chats, to be safe); a chat created while
	// the sweep runs has a fresh mtime far newer than the cutoff.
	keep, err := cs.repo.AllChatIDs()
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	// Legacy orphans on the backend host itself (sweep only; creates nothing).
	// Intentionally removes ANY directory below tmp/sessions older than the
	// cutoff, not just chat-id-named ones: transient entries such as
	// fakebash-sock-<uuid> and per-run <uuid> dirs are leftovers of crashed
	// runs once they are that old.
	for _, f := range cs.localRoots().Cleanup(runnerfs.CleanupSpec{Cutoff: cutoff, Keep: keep}).Failures {
		log.Warn().Str("error", f.Err).Msg("Failed to sweep legacy local orphan dir")
	}
	wfChats, err := cs.repo.WorkflowChatsByRunner()
	if err != nil {
		errs = append(errs, err)
	}
	for _, n := range cs.runners.Nodes() {
		if !n.Online {
			continue
		}
		client, err := cs.runners.Client(n.NodeID)
		if err != nil {
			continue
		}
		req := runnerfs.CleanupRequest{Cutoff: cutoff, Keep: keep}
		// The runner sweeps leftover atomic-write temp files of the
		// workflow files itself (the backend's remote store cannot).
		for _, chat := range wfChats[n.NodeID] {
			req.TmpSweeps = append(req.TmpSweeps, runnerfs.TmpSweepRef{ChatID: chat, Scope: "session", Rel: "workflows"})
		}
		rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
		out, err := runnerfs.Cleanup(rctx, client, req)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("runner %s orphan sweep: %w", n.NodeID, err))
			continue
		}
		for chat, fs := range out.Failures {
			errs = append(errs, fmt.Errorf("runner %s orphan sweep %q: %s", n.NodeID, chat, strings.Join(fs, "; ")))
		}
		if out.Removed > 0 {
			log.Info().Str("runner", n.NodeID).Int("removed", out.Removed).Msg("Runner removed orphan session dirs")
		}
	}
	return errors.Join(errs...)
}

// Shutdown stops the scheduler.
func (cs *Scheduler) Shutdown() error {
	return cs.scheduler.Shutdown()
}
