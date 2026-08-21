package cleanup

import (
	"fmt"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

const (
	defaultCronTimeHour   = 0
	defaultCronTimeMinute = 0
	defaultCronTimeSecond = 0
)

// Scheduler manages scheduled background tasks.
type Scheduler struct {
	scheduler gocron.Scheduler
	repo      *dbmodels.SessionRepository
	tmpBase   string
}

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*Scheduler)

// WithTmpBase configures a custom temporary base directory for session cleanup.
func WithTmpBase(path string) SchedulerOption {
	return func(s *Scheduler) {
		s.tmpBase = path
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

// CleanExpiredSessions cleans inactive sessions older than 1 month.
func (cs *Scheduler) CleanExpiredSessions() {
	if cs.repo == nil {
		return
	}
	cutoff := time.Now().AddDate(0, -1, 0)
	log.Info().Time("cutoff", cutoff).Msg("Starting scheduled session cleanup for sessions inactive since 1 month ago")
	if err := cs.repo.CleanExpiredSessions(dbmodels.CleanExpiredSessionsOptions{
		Cutoff:  cutoff,
		TmpBase: cs.tmpBase,
	}); err != nil {
		log.Error().Err(err).Msg("Scheduled session cleanup encountered errors")
	} else {
		log.Info().Msg("Scheduled session cleanup completed successfully")
	}
}

// Shutdown stops the scheduler.
func (cs *Scheduler) Shutdown() error {
	return cs.scheduler.Shutdown()
}
