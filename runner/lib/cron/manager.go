// Package cron manages scheduled workflow execution locally on the runner.
package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/trigger"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// AgentSource provides access to the effective agents on the runner and
// notifies when agent definitions change.
type AgentSource interface {
	Agents() ([]*agentspec.Agent, error)
	OnAgentsChange(fn func())
}

// Options configures a RunnerCronManager.
type Options struct {
	NodeID           string
	Remote           AgentSource
	Callbacks        func() (runnerpb.AgentCallbackServiceClient, error)
	Conf             func() *config.Config
	AllowUnsandboxed bool
}

type runnerJob struct {
	job      gocron.Job
	schedule string
}

// RunnerCronManager schedules and executes runner-bound workflows locally.
type RunnerCronManager struct {
	nodeID           string
	remote           AgentSource
	callbacks        func() (runnerpb.AgentCallbackServiceClient, error)
	conf             func() *config.Config
	allowUnsandboxed bool

	scheduler gocron.Scheduler
	mu        sync.Mutex
	jobs      map[string]runnerJob // jobKey -> runnerJob
}

// New creates and starts a RunnerCronManager.
func New(opts Options) (*RunnerCronManager, error) {
	scheduler, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("failed to create runner gocron scheduler: %w", err)
	}

	m := &RunnerCronManager{
		nodeID:           opts.NodeID,
		remote:           opts.Remote,
		callbacks:        opts.Callbacks,
		conf:             opts.Conf,
		allowUnsandboxed: opts.AllowUnsandboxed,
		scheduler:        scheduler,
		jobs:             make(map[string]runnerJob),
	}

	scheduler.Start()

	if m.remote != nil {
		m.Reload()
		m.remote.OnAgentsChange(m.Reload)
	}

	return m, nil
}

// Reload synchronizes scheduled jobs with the runner's effective workflow agents.
func (m *RunnerCronManager) Reload() {
	if m == nil || m.scheduler == nil || m.remote == nil {
		return
	}

	agentsList, err := m.remote.Agents()
	if err != nil {
		log.Warn().Err(err).Msg("failed to load runner agents for cron scheduling")
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
			log.Warn().Err(err).Str("agent", a.Config.ID).Msg("failed to load workflow definition for runner cron")
			continue
		}
		if defn.Schedule == "" {
			continue
		}
		if err := defn.ValidateScheduleRunner(a.NodeID); err != nil {
			log.Error().Err(err).Str("agent", a.Config.ID).Msg("workflow cron job not scheduled on runner")
			continue
		}

		// Only schedule workflows owned by this runner. Global agents are scheduled by backend.
		if a.NodeID != m.nodeID {
			continue
		}

		desired[jobKey(a)] = scheduled{agent: a, schedule: defn.Schedule}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, rj := range m.jobs {
		if _, ok := desired[id]; !ok {
			if err := m.scheduler.RemoveJob(rj.job.ID()); err != nil {
				log.Warn().Err(err).Str("job", id).Msg("failed to remove stale runner cron job")
			}
			delete(m.jobs, id)
		}
	}

	for id, item := range desired {
		if existing, ok := m.jobs[id]; ok {
			if existing.schedule == item.schedule {
				continue // Unchanged schedule, skip re-register churn
			}
			if err := m.scheduler.RemoveJob(existing.job.ID()); err != nil {
				log.Warn().Err(err).Str("job", id).Msg("failed to remove outdated runner cron job")
			}
			delete(m.jobs, id)
		}

		job, err := m.scheduler.NewJob(
			gocron.CronJob(item.schedule, false),
			gocron.NewTask(m.RunScheduledWorkflow, item.agent),
			gocron.WithSingletonMode(gocron.LimitModeReschedule),
		)
		if err != nil {
			log.Error().Err(err).Str("agent", id).Str("schedule", item.schedule).Msg("failed to register runner cron job")
			continue
		}
		m.jobs[id] = runnerJob{job: job, schedule: item.schedule}
		log.Info().Str("agent", id).Str("schedule", item.schedule).Msg("Scheduled runner workflow cron job")
	}
}

// RunScheduledWorkflow executes one scheduled workflow run locally.
func (m *RunnerCronManager) RunScheduledWorkflow(agent *agentspec.Agent) {
	ctx := context.Background()
	sessionID := trigger.CronSessionID(m.nodeID, agent.Config.Name)

	if m.callbacks != nil {
		if cb, err := m.callbacks(); err == nil && cb != nil {
			initCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := cb.InitWorkflowSession(initCtx, &runnerpb.InitWorkflowSessionRequest{
				SessionId:    sessionID,
				RunnerId:     m.nodeID,
				AgentId:      agent.Config.ID,
				WorkflowName: agent.Config.Name,
			})
			cancel()
			if err != nil {
				log.Warn().Err(err).Str("session_id", sessionID).Msg("failed to init workflow session on backend")
			}
		}
	}

	defn, err := workflowspec.LoadDefinition(agent.WorkflowPath)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.Config.ID).Msg("failed to load workflow definition for execution")
		return
	}

	engine, err := m.buildEngine(agent, defn)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.Config.ID).Msg("failed to build workflow engine for runner execution")
		return
	}

	runDir := ""
	if len(agent.Config.RunDirs) > 0 {
		runDir = agent.Config.RunDirs[0]
	}

	executor := workflow.NewWorkflowExecutor(engine, defn)
	executor.AgentName = agent.Config.Name
	executor.WorkflowRunDirs = agent.Config.RunDirs
	executor.WorkflowMountDirs = workflowspec.MountDirsConfig{
		ReadOnly:  agent.Config.MountDirs.ReadOnly,
		ReadWrite: agent.Config.MountDirs.ReadWrite,
	}

	executor.OnEvent = func(sessionID string, ev workflow.WorkflowEvent) {
		if m.callbacks == nil {
			return
		}
		cb, err := m.callbacks()
		if err != nil || cb == nil {
			return
		}
		var rawMeta []byte
		if ev.Metadata != nil {
			rawMeta, _ = json.Marshal(ev.Metadata)
		}
		eventCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err = cb.ReportWorkflowEvent(eventCtx, &runnerpb.ReportWorkflowEventRequest{
			SessionId:         sessionID,
			RunnerId:          m.nodeID,
			Workflow:          ev.Workflow,
			EventType:         string(ev.Type),
			NodeId:            ev.NodeID,
			NodeType:          string(ev.NodeType),
			AgentId:           ev.AgentID,
			Status:            string(ev.Status),
			SkipReason:        string(ev.SkipReason),
			Message:           ev.Message,
			EntryType:         ev.EntryType,
			MetadataJson:      rawMeta,
			MessageId:         ev.MessageID,
			Artifacts:         ev.Artifacts,
			Output:            ev.Output,
			AgentName:         ev.AgentName,
			Cli:               ev.CLI,
			Model:             ev.Model,
			TimestampUnixNano: ev.Timestamp.UnixNano(),
		})
		if err != nil {
			log.Warn().Err(err).Str("session_id", sessionID).Str("type", string(ev.Type)).Msg("failed to report workflow event to backend")
		}
	}

	res, err := executor.Execute(ctx, workflow.WorkflowRunParams{
		SessionID: sessionID,
		RunDir:    runDir,
		Headless:  true,
	})
	if err != nil {
		log.Error().Err(err).Str("session_id", sessionID).Str("agent", agent.Config.ID).Msg("scheduled workflow execution failed on runner")
	} else {
		log.Info().Str("session_id", sessionID).Str("status", string(res.Status)).Msg("scheduled workflow execution completed on runner")
	}
}

type agentListerFunc func() ([]*agentspec.Agent, error)

func (l agentListerFunc) LoadAll() ([]*agentspec.Agent, error) { return l() }

func (m *RunnerCronManager) buildEngine(agent *agentspec.Agent, defn *workflowspec.WorkflowDefinition) (*workflow.Engine, error) {
	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewFunctionRunner(nil))

	var conf *config.Config
	if m.conf != nil {
		conf = m.conf()
	}
	registry.Register(workflow.NewCommandRunnerWithConfig(m.allowUnsandboxed, conf))

	// SubWorkflow runner
	subRunner := workflow.NewSubWorkflowRunner(func(name string) (*workflowspec.WorkflowDefinition, error) {
		if m.remote == nil {
			return nil, fmt.Errorf("remote agents source unavailable")
		}
		agents, err := m.remote.Agents()
		if err != nil {
			return nil, err
		}
		for _, a := range agents {
			if a.Config.Type != "workflow" || a.WorkflowPath == "" {
				continue
			}
			if a.Config.ID == name || a.Config.Name == name {
				return workflowspec.LoadDefinition(a.WorkflowPath)
			}
		}
		for _, a := range agents {
			if a.Config.Type != "workflow" || a.WorkflowPath == "" {
				continue
			}
			d, err := workflowspec.LoadDefinition(a.WorkflowPath)
			if err == nil && d.Name == name {
				return d, nil
			}
		}
		return nil, fmt.Errorf("sub-workflow %q not found on runner", name)
	})
	registry.Register(subRunner)

	if m.remote != nil {
		lister := agentListerFunc(m.remote.Agents)
		agentRunner := workflow.NewAgentRunner(lister, conf)
		registry.Register(agentRunner)
	}

	engine := workflow.NewEngine(registry)
	subRunner.SetEngine(engine)

	if m.callbacks != nil {
		engine.SetRunStore(newRemoteRunStore(m.callbacks))
	}

	return engine, nil
}

// Jobs returns the list of currently registered job keys.
func (m *RunnerCronManager) Jobs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.jobs))
	for k := range m.jobs {
		keys = append(keys, k)
	}
	return keys
}

// Shutdown stops the scheduler and releases resources.
func (m *RunnerCronManager) Shutdown() error {
	if m == nil || m.scheduler == nil {
		return nil
	}
	return m.scheduler.Shutdown()
}

func jobKey(a *agentspec.Agent) string { return a.NodeID + "/" + a.Config.ID }
