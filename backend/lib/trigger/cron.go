// Package trigger hosts scheduled (cron) triggers for workflow agentspec.
package trigger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	jobs      map[string]gocron.Job // jobKey(agent) -> gocron Job
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

// CronSessionID is the synthetic session of a scheduled workflow on a runner.
// A chat's runner is immutable, so the runner is part of the id: changing the
// definition's runner_id yields a new session instead of silently running on
// the old binding. Over-long ids are shortened with a hash of the full name.
func CronSessionID(runnerID, workflowName string) string {
	if runnerID == "" {
		return CleanCronSessionID(workflowName)
	}
	id := CleanCronSessionID(runnerID + "-" + workflowName)
	if len(id) < maxChatIDLength && id == cronSessionIDPrefix+runnerID+"-"+workflowName {
		return id
	}
	sum := sha256.Sum256([]byte(runnerID + "\x00" + workflowName))
	suffix := "-" + hex.EncodeToString(sum[:4])
	if len(id)+len(suffix) > maxChatIDLength {
		id = id[:maxChatIDLength-len(suffix)]
	}
	return id + suffix
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
		runnerID string
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
			if err := defn.ValidateScheduleRunner(a.NodeID); err != nil {
				log.Error().Err(err).Str("agent", a.Config.ID).Msg("workflow cron job not scheduled")
				continue
			}
			runnerID := defn.RunnerID
			if a.NodeID != "" {
				runnerID = a.NodeID
			}
			desired[jobKey(a)] = scheduled{agent: a, schedule: defn.Schedule, runnerID: runnerID}
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
		job, err := m.addJobLocked(item.agent, item.runnerID, gocron.CronJob(item.schedule, false))
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
func (m *WorkflowCronManager) addJobLocked(agent *agentspec.Agent, runnerID string, defn gocron.JobDefinition) (gocron.Job, error) {
	job, err := m.scheduler.NewJob(
		defn,
		gocron.NewTask(m.runScheduledWorkflow, agent, runnerID),
		gocron.WithSingletonMode(gocron.LimitModeReschedule),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to schedule workflow cron job: %w", err)
	}
	return job, nil
}

// runScheduledWorkflow is the task body executed on every schedule cycle.
func (m *WorkflowCronManager) runScheduledWorkflow(agent *agentspec.Agent, runnerID string) {
	ctx := context.Background()
	sessionID := CronSessionID(runnerID, agent.Config.Name)

	if m.repo != nil {
		session := &dbmodels.Session{
			ChatID:       sessionID,
			CurrentAgent: agent.Config.ID,
			Title:        fmt.Sprintf("Scheduled: %s", agent.Config.Name),
			RunnerNodeID: runnerID,
		}
		if err := m.repo.UpsertSession(session); err != nil {
			log.Warn().Err(err).Str("chat_id", sessionID).Msg("failed to upsert synthetic cron session")
		}
		// Never run on a different runner than the definition says.
		if sess, err := m.repo.GetSession(sessionID); err != nil || sess == nil || sess.RunnerNodeID != runnerID {
			log.Error().Err(err).Str("chat_id", sessionID).Str("runner", runnerID).Msg("cron session is not bound to the definition's runner; skipping cycle")
			return
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

// jobKey identifies a scheduled agent: runner agents may reuse ids across
// runners.
func jobKey(a *agentspec.Agent) string { return a.NodeID + "/" + a.Config.ID }
