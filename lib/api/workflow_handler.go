package api

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"strings"

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
func newWorkflowEngine(conf *config.Config, statusListener workflow.AgentStatusListener) (*workflow.Engine, error) {
	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(true))
	if conf != nil {
		registry.Register(workflow.NewAgentRunnerWithListener(agents.NewLoader(conf.AgentDir), conf, statusListener))
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
		engine, err = newWorkflowEngine(s.conf, s)
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
	handler := a2asrv.NewHandler(&workflowTitleExecutor{
		inner:  executor,
		server: s,
		agent:  agent,
	})
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

// workflowTitleExecutor wraps the workflow executor so workflow chats get a
// Gemini-generated session title on first contact, mirroring the single-agent
// code path.
type workflowTitleExecutor struct {
	inner  a2asrv.AgentExecutor
	server *Server
	agent  *agents.Agent
	// llmClient may be nil; in that case a genai-backed client is created
	// lazily using the configured (or env) API key.
	llmClient llm.Client
}

var _ a2asrv.AgentExecutor = (*workflowTitleExecutor)(nil)

// Execute generates the session title on first contact (when the session has
// no stored title yet) before delegating to the wrapped workflow executor.
func (e *workflowTitleExecutor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	e.maybeGenerateTitle(ctx, execCtx)
	return e.inner.Execute(ctx, execCtx)
}

// Cancel delegates cancellation to the wrapped workflow executor.
func (e *workflowTitleExecutor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return e.inner.Cancel(ctx, execCtx)
}

// maybeGenerateTitle spawns the background title-generation goroutine when the
// chat session exists and has no title yet.
func (e *workflowTitleExecutor) maybeGenerateTitle(ctx context.Context, execCtx *a2asrv.ExecutorContext) {
	if e.server == nil || e.server.repo == nil {
		return
	}
	chatID := execCtx.ContextID
	if chatID == "" || !IsValidChatID(chatID) {
		return
	}
	session, err := e.server.repo.GetSession(chatID)
	if err != nil {
		log.Warn().Err(err).Str("chat_id", chatID).Msg("failed to get session for workflow title generation")
		return
	}
	if session != nil && session.Title != "" {
		return
	}
	goGenerateSessionTitle(ctx, e.server, e.llmClient, e.server.repo, chatID, messagePromptText(execCtx.Message), e.agent.Config.ID, e.agent.Config.Description)
}

// messagePromptText concatenates the text parts of an A2A message.
func messagePromptText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var sb strings.Builder
	for _, part := range msg.Parts {
		if part != nil && part.Text() != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(part.Text())
		}
	}
	return sb.String()
}
