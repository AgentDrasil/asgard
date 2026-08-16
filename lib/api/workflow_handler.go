package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/llm"
	"github.com/AgentDrasil/asgard/lib/workflow"
)

// newWorkflowEngine builds the shared workflow engine with all node runners
// registered via the IoC registry.
func newWorkflowEngine(conf *config.Config) (*workflow.Engine, error) {
	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(true))
	if conf != nil {
		registry.Register(workflow.NewAgentRunner(agents.NewLoader(conf.AgentDir), conf))
		if conf.GeminiAPIKey != "" {
			client, err := llm.NewClient(context.Background(), conf.GeminiAPIKey)
			if err != nil {
				return nil, fmt.Errorf("creating llm client for workflow engine: %w", err)
			}
			registry.Register(workflow.NewLLMRunner(client))
		}
	}
	return workflow.NewEngine(registry), nil
}

// newWorkflowHandler creates the A2A REST handler and agent card for a
// workflow-type agent.
func (s *Server) newWorkflowHandler(agent *agents.Agent) (http.Handler, *a2a.AgentCard) {
	defn, err := workflow.LoadDefinition(agent.WorkflowPath)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.Config.ID).Msg("failed to load workflow definition")
		return nil, nil
	}

	engine := s.workflowEngine
	if engine == nil {
		var err error
		engine, err = newWorkflowEngine(s.conf)
		if err != nil {
			log.Error().Err(err).Str("agent", agent.Config.ID).Msg("failed to create workflow engine")
			return nil, nil
		}
	}

	executor := workflow.NewWorkflowExecutor(engine, defn)
	executor.AgentName = agent.Config.Name
	executor.WorkflowRunDirs = agent.Config.RunDirs
	executor.WorkflowMountDirs = workflow.MountDirsConfig{
		ReadOnly:  agent.Config.MountDirs.ReadOnly,
		ReadWrite: agent.Config.MountDirs.ReadWrite,
	}
	executor.OnEvent = s.handleWorkflowEvent
	handler := a2asrv.NewHandler(executor)
	restHandler := a2asrv.NewRESTHandler(handler)

	host := "http://localhost:8080"
	if s.conf != nil && s.conf.Host != "" {
		host = s.conf.Host
	}

	card := &a2a.AgentCard{
		Name:        agent.Config.Name,
		Description: agent.Config.Description,
		Version:     "1.0.0",
		Capabilities: a2a.AgentCapabilities{
			Streaming: true,
		},
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(fmt.Sprintf("%s/agents/%s", host, agent.Config.ID), a2a.TransportProtocolHTTPJSON),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}

	return restHandler, card
}
