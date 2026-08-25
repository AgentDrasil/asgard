// Package trigger hosts scheduled (cron) triggers for workflow agentspec.
package trigger

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

const (
	// cronSessionIDPrefix is the prefix for synthetic cron-triggered session IDs.
	cronSessionIDPrefix = "wf-cron-"
	// maxChatIDLength matches the IsValidChatID constraint (^[a-zA-Z0-9_-]{1,64}$).
	maxChatIDLength = 64
)

var cronSessionIDInvalidChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// TriggerFunc delegates a workflow trigger to the host layer (Server.runWorkflow).
type TriggerFunc func(ctx context.Context, agent *agentspec.Agent, chatID string, prompt string, headless bool) error

// WorkflowCronManager schedules workflow agents that declare a `schedule` in
// their workflow definition, triggering them headlessly on a synthetic session.
type WorkflowCronManager struct {
	scheduler gocron.Scheduler
	repo      *dbmodels.SessionRepository
	trigger   TriggerFunc
	mu        sync.Mutex
	jobs      map[string]gocron.Job // agent.Config.ID -> gocron Job
}

// NewWorkflowCronManager creates and starts a gocron scheduler.
func NewWorkflowCronManager(repo *dbmodels.SessionRepository, trigger TriggerFunc) (*WorkflowCronManager, error) {
	scheduler, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("failed to create gocron scheduler: %w", err)
	}
	m := &WorkflowCronManager{
		scheduler: scheduler,
		repo:      repo,
		trigger:   trigger,
		jobs:      make(map[string]gocron.Job),
	}
	scheduler.Start()
	return m, nil
}

// CleanCronSessionID derives a deterministic synthetic session ID from the
// workflow name. Invalid characters are replaced with '-' and the result is
// truncated to 64 characters so it always satisfies IsValidChatID.
// Note: Different workflow names that map to the same string after sanitization
// or truncation will deterministically share the same synthetic session.
func CleanCronSessionID(workflowName string) string {
	id := cronSessionIDPrefix + cronSessionIDInvalidChars.ReplaceAllString(workflowName, "-")
	if len(id) > maxChatIDLength {
		id = id[:maxChatIDLength]
	}
	return id
}

// Reload synchronizes scheduled jobs with the latest workflow agents:
// jobs of removed agents or agents whose schedule was dropped are removed,
// while new or updated scheduled workflows are (re-)registered.
func (m *WorkflowCronManager) Reload(agentsList []*agentspec.Agent) {
	if m == nil || m.scheduler == nil {
		return
	}

	type scheduled struct {
		agent    *agentspec.Agent
		schedule string
	}
	desired := make(map[string]scheduled)
	for _, a := range agentsList {
		if a == nil || a.Config.Type != "workflow" || a.WorkflowPath == "" {
			continue
		}
		defn, err := workflowspec.LoadDefinition(a.WorkflowPath)
		if err != nil {
			log.Warn().Err(err).Str("agent", a.Config.ID).Msg("failed to load workflow definition for cron scheduling")
			continue
		}
		if defn.Schedule != "" {
			desired[a.Config.ID] = scheduled{agent: a, schedule: defn.Schedule}
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, job := range m.jobs {
		if _, ok := desired[id]; !ok {
			if err := m.scheduler.RemoveJob(job.ID()); err != nil {
				log.Warn().Err(err).Str("agent", id).Msg("failed to remove stale workflow cron job")
			}
			delete(m.jobs, id)
		}
	}

	for id, item := range desired {
		// Re-register unconditionally so schedule changes take effect.
		if job, ok := m.jobs[id]; ok {
			if err := m.scheduler.RemoveJob(job.ID()); err != nil {
				log.Warn().Err(err).Str("agent", id).Msg("failed to remove outdated workflow cron job")
			}
			delete(m.jobs, id)
		}
		job, err := m.addJobLocked(item.agent, gocron.CronJob(item.schedule, false))
		if err != nil {
			log.Error().Err(err).Str("agent", id).Str("schedule", item.schedule).Msg("failed to register workflow cron job")
			continue
		}
		m.jobs[id] = job
		log.Info().Str("agent", id).Str("schedule", item.schedule).Msg("Scheduled workflow cron job")
	}
}

// addJobLocked registers a job with singleton mode so overlapping cycles are
// skipped while a previous run is still in flight. Callers must hold m.mu.
func (m *WorkflowCronManager) addJobLocked(agent *agentspec.Agent, defn gocron.JobDefinition) (gocron.Job, error) {
	job, err := m.scheduler.NewJob(
		defn,
		gocron.NewTask(m.runScheduledWorkflow, agent),
		gocron.WithSingletonMode(gocron.LimitModeReschedule),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to schedule workflow cron job: %w", err)
	}
	return job, nil
}

// runScheduledWorkflow is the task body executed on every schedule cycle.
func (m *WorkflowCronManager) runScheduledWorkflow(agent *agentspec.Agent) {
	ctx := context.Background()
	sessionID := CleanCronSessionID(agent.Config.Name)

	if m.repo != nil {
		session := &dbmodels.Session{
			ChatID:       sessionID,
			CurrentAgent: agent.Config.ID,
			Title:        fmt.Sprintf("Scheduled: %s", agent.Config.Name),
		}
		if err := m.repo.UpsertSession(session); err != nil {
			log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to upsert synthetic cron session")
		}
	}

	if m.trigger == nil {
		return
	}
	if err := m.trigger(ctx, agent, sessionID, "", true); err != nil {
		log.Error().Err(err).Str("chat_id", sessionID).Str("agent", agent.Config.ID).Msg("workflow cron trigger failed")
	}
}

// Shutdown stops the scheduler and releases timer resources.
func (m *WorkflowCronManager) Shutdown() error {
	if m == nil || m.scheduler == nil {
		return nil
	}
	return m.scheduler.Shutdown()
}
