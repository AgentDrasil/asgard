package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/agents/run"
	"github.com/AgentDrasil/asgard/lib/config"
)

func withNodeTimeout(ctx context.Context, node *NodeSpec) (context.Context, context.CancelFunc) {
	if d := node.TimeoutDuration(); d > 0 {
		return context.WithTimeout(ctx, d)
	}
	return context.WithCancel(ctx)
}

// agentSessionKey is the RunValues key under which the CLI session ID of an
// agent is inherited between nodes using the same agent_id.
func agentSessionKey(agentID string) string {
	return "agent_session:" + agentID
}

// agentStartPrompt kicks off a non-entry agent node. Its inputs are files
// produced by earlier nodes, referenced from its AGENTS.md.
const agentStartPrompt = "Read the files your AGENTS.md instructions reference (they may have been written by earlier steps), then execute your task."

// agentFollowUpPrompt is sent when an agent node resumes an existing CLI
// session (session_policy: inherit). The agent's AGENTS.md defines how to
// handle continuation; the re-read reminder matters because other agents may
// have updated the referenced files since the previous turn.
const agentFollowUpPrompt = "This is a follow-up on your existing session. Files referenced by your AGENTS.md instructions may have been updated by other agents since your last turn — re-read them, then follow your instructions for handling this continuation."

// AgentStatusListener can be provided to agentRunner to receive live status updates.
type AgentStatusListener interface {
	AddStatusListener(chatID string) (<-chan AgentStatusUpdate, func())
}

// AgentStatusUpdate mirrors the api.AgentStatusUpdate struct for live streaming.
type AgentStatusUpdate struct {
	ChatID    string         `json:"chat_id"`
	StepIndex int            `json:"step_index"`
	Source    string         `json:"source"`
	EntryType string         `json:"entry_type"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// agentRunner executes agent nodes by invoking CLI agents inside sandboxes.
type agentRunner struct {
	loader         *agents.Loader
	conf           *config.Config
	statusListener AgentStatusListener

	mu     sync.Mutex
	agents map[string]*agents.Agent
	loaded bool
}

// NewAgentRunner creates the runner for `agent` nodes. The loader resolves
// agent_id references lazily (and re-resolves after agent reloads on cache miss).
func NewAgentRunner(loader *agents.Loader, conf *config.Config) NodeRunner {
	return NewAgentRunnerWithListener(loader, conf, nil)
}

// NewAgentRunnerWithListener creates the runner for `agent` nodes with an optional status listener.
func NewAgentRunnerWithListener(loader *agents.Loader, conf *config.Config, listener AgentStatusListener) NodeRunner {
	return &agentRunner{loader: loader, conf: conf, statusListener: listener, agents: make(map[string]*agents.Agent)}
}

func (r *agentRunner) Supports(t NodeType) bool {
	return t == NodeTypeAgent
}

// lookup finds an agent by ID, loading the agent directory on first use.
func (r *agentRunner) lookup(agentID string) (*agents.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agent, ok := r.agents[agentID]; ok {
		return agent, nil
	}
	if r.loaded && r.loader == nil {
		return nil, fmt.Errorf("agent %q not found", agentID)
	}

	if r.loader != nil {
		loaded, err := r.loader.LoadAll()
		if err != nil {
			return nil, fmt.Errorf("loading agents: %w", err)
		}
		r.loaded = true
		for _, agent := range loaded {
			r.agents[agent.Config.ID] = agent
		}
	}

	agent, ok := r.agents[agentID]
	if !ok {
		return nil, fmt.Errorf("agent %q not found", agentID)
	}
	return agent, nil
}

func (r *agentRunner) Run(ctx context.Context, nctx *NodeContext) (*NodeResult, error) {
	node := nctx.Node

	agent, err := r.lookup(node.AgentID)
	if err != nil {
		return nil, err
	}

	// Agent nodes carry no YAML prompt; the runner seeds the CLI prompt from
	// the workflow context: entry nodes receive the raw user input, other
	// fresh nodes get a kickoff directive (their inputs are files produced
	// by earlier nodes), resumed sessions get a follow-up directive.
	prompt := strings.TrimSpace(nctx.Interpolate(node.Prompt))

	// Session policy: `fresh` always starts a clean CLI session; `inherit`
	// (default) resumes the session a previous node opened for this agent.
	session := optional.None[string]()
	resuming := false
	if node.SessionPolicyInherit() {
		if v, ok := nctx.Values.Get(agentSessionKey(node.AgentID)); ok {
			if sid, ok := v.(string); ok && sid != "" {
				session = optional.Some(sid)
				resuming = true
			}
		}
	}

	if prompt == "" {
		switch {
		case resuming:
			prompt = agentFollowUpPrompt
		case node.Entry:
			input := strings.TrimSpace(nctx.Input)
			if input == "" {
				return nil, fmt.Errorf("agent node %q has no prompt: the workflow input is empty", node.ID)
			}
			prompt = input
		default:
			prompt = agentStartPrompt
		}
	}

	runDirOpt := optional.None[string]()
	workingDir := nctx.Interpolate(node.WorkingDir)
	if workingDir != "" {
		if !filepath.IsAbs(workingDir) {
			workingDir = filepath.Join(nctx.RunDir, workingDir)
		}
		runDirOpt = optional.Some(workingDir)
	} else if nctx.RunDir != "" {
		runDirOpt = optional.Some(nctx.RunDir)
	}

	modelOpt := optional.None[string]()
	if node.Model != "" {
		modelOpt = optional.Some(node.Model)
	}

	ctx, cancel := withNodeTimeout(ctx, node)
	defer cancel()

	effectiveAgent := resolveEffectiveAgent(agent, nctx)

	log.Info().
		Str("session_id", nctx.SessionID).
		Str("node_id", node.ID).
		Str("agent_id", node.AgentID).
		Str("policy", node.SessionPolicy).
		Msgf("[AgentRunner] Starting agent %q for node %q", node.AgentID, node.ID)

	var statusCh <-chan AgentStatusUpdate
	var cancelListener func()
	if r.statusListener != nil && nctx.SessionID != "" {
		statusCh, cancelListener = r.statusListener.AddStatusListener(nctx.SessionID)
		defer cancelListener()
	}

	type runOutcome struct {
		out []byte
		err error
	}
	outCh := make(chan runOutcome, 1)

	go func() {
		out, err := run.Run(ctx, effectiveAgent, prompt, session, runDirOpt, modelOpt, nctx.SessionID, r.conf)
		outCh <- runOutcome{out: out, err: err}
	}()

	var nodeArtifacts []string
	seenArtifacts := make(map[string]bool)
	workspaceDir := nctx.RunDir
	if runDirOpt.IsSome() {
		workspaceDir = runDirOpt.Unwrap()
	}

	var out []byte
loop:
	for {
		select {
		case update, ok := <-statusCh:
			if !ok {
				statusCh = nil
				continue
			}
			var stepArtifacts []string
			if targetFiles, ok := update.Metadata["target_files"].([]string); ok {
				for _, tf := range targetFiles {
					if agents.IsArtifact(tf, &effectiveAgent.Config, workspaceDir) {
						vPath := ViewerArtifactPath(tf, nctx.TmpDir)
						stepArtifacts = append(stepArtifacts, vPath)
						if !seenArtifacts[vPath] {
							seenArtifacts[vPath] = true
							nodeArtifacts = append(nodeArtifacts, vPath)
						}
					}
				}
			} else if targetFilesAny, ok := update.Metadata["target_files"].([]any); ok {
				for _, item := range targetFilesAny {
					if tf, ok := item.(string); ok && tf != "" {
						if agents.IsArtifact(tf, &effectiveAgent.Config, workspaceDir) {
							vPath := ViewerArtifactPath(tf, nctx.TmpDir)
							stepArtifacts = append(stepArtifacts, vPath)
							if !seenArtifacts[vPath] {
								seenArtifacts[vPath] = true
								nodeArtifacts = append(nodeArtifacts, vPath)
							}
						}
					}
				}
			}

			if nctx.EventEmitter != nil {
				nctx.EventEmitter(WorkflowEvent{
					Type:      EventNodeStatusUpdate,
					NodeID:    node.ID,
					NodeType:  NodeTypeAgent,
					AgentID:   node.AgentID,
					AgentName: effectiveAgent.Config.Name,
					Status:    StatusRunning,
					Message:   update.Content,
					EntryType: update.EntryType,
					Metadata:  update.Metadata,
					Artifacts: stepArtifacts,
				})
			}
		case outcome := <-outCh:
			out = outcome.out
			err = outcome.err
			break loop
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	lastContent, sessionID := parseAgentOutput(out)
	if sessionID != "" && node.SessionPolicyInherit() {
		nctx.Values.Set(agentSessionKey(node.AgentID), sessionID)
	}

	if err != nil {
		log.Error().
			Err(err).
			Str("session_id", nctx.SessionID).
			Str("node_id", node.ID).
			Str("agent_id", node.AgentID).
			Msgf("[AgentRunner] Agent %q for node %q FAILED: %v", node.AgentID, node.ID, err)
		return &NodeResult{
			Status:    StatusFailed,
			Output:    lastContent,
			Artifacts: toArtifactMap(nodeArtifacts),
			Error:     fmt.Errorf("agent %s run failed: %w", node.AgentID, err),
		}, nil
	}

	log.Info().
		Str("session_id", nctx.SessionID).
		Str("node_id", node.ID).
		Str("agent_id", node.AgentID).
		Msgf("[AgentRunner] Agent %q for node %q COMPLETED successfully", node.AgentID, node.ID)
	return &NodeResult{Status: StatusSucceeded, Output: lastContent, Artifacts: toArtifactMap(nodeArtifacts)}, nil
}

// agentPromptResult mirrors the JSON structure returned by CLI agents.
type agentPromptResult struct {
	SessionID   string `json:"session_id"`
	LastContent string `json:"last_content"`
}

// parseAgentOutput extracts the last assistant content and session ID from a
// CLI agent's JSON output, falling back to the raw output text.
func parseAgentOutput(out []byte) (lastContent string, sessionID string) {
	var result agentPromptResult
	if err := json.Unmarshal(out, &result); err == nil {
		content := strings.TrimSpace(result.LastContent)
		if content == "" {
			content = strings.TrimSpace(string(out))
		}
		return content, result.SessionID
	}
	return strings.TrimSpace(string(out)), ""
}

// resolveEffectiveAgent returns a clone of the agent with missing RunDirs and MountDirs
// inherited from the enclosing workflow context.
// ReadOnly and ReadWrite mounts are inherited independently to avoid dropping explicit configs.
func resolveEffectiveAgent(agent *agents.Agent, nctx *NodeContext) *agents.Agent {
	if agent == nil {
		return nil
	}
	effectiveAgent := *agent
	effectiveConfig := agent.Config

	if len(effectiveConfig.RunDirs) == 0 && len(nctx.WorkflowRunDirs) > 0 {
		effectiveConfig.RunDirs = append([]string(nil), nctx.WorkflowRunDirs...)
	}
	if len(effectiveConfig.MountDirs.ReadOnly) == 0 && len(nctx.WorkflowMountDirs.ReadOnly) > 0 {
		effectiveConfig.MountDirs.ReadOnly = append([]string(nil), nctx.WorkflowMountDirs.ReadOnly...)
	}
	if len(effectiveConfig.MountDirs.ReadWrite) == 0 && len(nctx.WorkflowMountDirs.ReadWrite) > 0 {
		effectiveConfig.MountDirs.ReadWrite = append([]string(nil), nctx.WorkflowMountDirs.ReadWrite...)
	}
	effectiveAgent.Config = effectiveConfig
	return &effectiveAgent
}

func toArtifactMap(paths []string) map[string]string {
	if len(paths) == 0 {
		return nil
	}
	m := make(map[string]string, len(paths))
	for _, p := range paths {
		m[p] = p
	}
	return m
}
