package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"uuid"

	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

func withNodeTimeout(ctx context.Context, node *workflowspec.NodeSpec) (context.Context, context.CancelFunc) {
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
// The match predicate, when non-nil, restricts delivery to updates it accepts;
// nil delivers every update for the chatID.
type AgentStatusListener interface {
	AddStatusListener(chatID string, match func(AgentStatusUpdate) bool) (<-chan AgentStatusUpdate, func())
}

// AgentStatusUpdate is the JSON payload posted by aw to the internal status
// endpoint whenever the agent produces an incremental transcript update.
// NodeID and RunToken identify the invoking workflow node invocation: parallel
// nodes in the same session share ChatID, so receivers use RunToken to
// attribute each update to the node invocation that produced it.
type AgentStatusUpdate struct {
	ChatID    string         `json:"chat_id"`
	NodeID    string         `json:"node_id,omitempty"`
	RunToken  string         `json:"run_token,omitempty"`
	StepIndex int            `json:"step_index"`
	Source    string         `json:"source"`
	EntryType string         `json:"entry_type"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// agentRunner executes agent nodes by invoking CLI agents inside sandboxes.
type agentRunner struct {
	loader         *agentspec.Loader
	conf           *config.Config
	statusListener AgentStatusListener

	mu     sync.Mutex
	agents map[string]*agentspec.Agent
	loaded bool
}

// NewAgentRunner creates the runner for `agent` nodes. The loader resolves
// agent_id references lazily (and re-resolves after agent reloads on cache miss).
func NewAgentRunner(loader *agentspec.Loader, conf *config.Config) NodeRunner {
	return NewAgentRunnerWithListener(loader, conf, nil)
}

// NewAgentRunnerWithListener creates the runner for `agent` nodes with an optional status listener.
func NewAgentRunnerWithListener(loader *agentspec.Loader, conf *config.Config, listener AgentStatusListener) NodeRunner {
	return &agentRunner{loader: loader, conf: conf, statusListener: listener, agents: make(map[string]*agentspec.Agent)}
}

func (r *agentRunner) Supports(t workflowspec.NodeType) bool {
	return t == workflowspec.NodeTypeAgent
}

// SetAgents preloads or refreshes the agent cache in the runner.
func (r *agentRunner) SetAgents(agentList []*agentspec.Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = make(map[string]*agentspec.Agent, len(agentList))
	for _, a := range agentList {
		if a != nil && a.Config.ID != "" {
			r.agents[a.Config.ID] = a
		}
	}
	r.loaded = true
}

// lookup finds an agent by ID, loading the agent directory on first use.
func (r *agentRunner) lookup(agentID string) (*agentspec.Agent, error) {
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

func (r *agentRunner) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
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
		var promptErr error
		prompt, promptErr = resolveAgentPrompt(nctx, node, resuming)
		if promptErr != nil {
			return nil, promptErr
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

	initialPrompt := prompt

	maxRetries := 0
	if node.MaxRetries != nil {
		maxRetries = *node.MaxRetries
	} else if len(node.RequiredOutputs) > 0 {
		maxRetries = 2 // Default to 2 retries when required_outputs are specified and max_retries is not explicitly set
	}

	var out []byte
	var lastContent string
	var currentSessionID string
	var executionErr error
	var qualityGateErr error
	var nodeArtifacts []string

	totalAttempts := maxRetries + 1
	for attempt := 1; attempt <= totalAttempts; attempt++ {
		executionErr = nil
		qualityGateErr = nil

		log.Info().
			Str("session_id", nctx.SessionID).
			Str("node_id", node.ID).
			Str("agent_id", node.AgentID).
			Str("policy", node.SessionPolicy).
			Int("attempt", attempt).
			Int("max_attempts", totalAttempts).
			Msgf("[AgentRunner] Starting agent %q for node %q (attempt %d/%d)", node.AgentID, node.ID, attempt, totalAttempts)

		// runToken uniquely identifies this node invocation so that, when parallel
		// agent nodes share a session, each runner only consumes the status updates
		// its own sandbox reported. It is injected into the sandbox via env and
		// echoed back by aw in every update payload.
		runToken := uuid.NewV7().String()
		var statusCh <-chan AgentStatusUpdate
		var cancelListener func()
		if r.statusListener != nil && nctx.SessionID != "" {
			statusCh, cancelListener = r.statusListener.AddStatusListener(nctx.SessionID, func(update AgentStatusUpdate) bool {
				return update.RunToken == runToken
			})
		}

		type runOutcome struct {
			out []byte
			err error
		}
		outCh := make(chan runOutcome, 1)

		go func(currentPrompt string, currentSession optional.Option[string]) {
			runOut, runErr := r.runWithQuotaDecisions(ctx, nctx, node, effectiveAgent, currentPrompt, currentSession, runDirOpt, modelOpt, run.StatusScope{NodeID: node.ID, RunToken: runToken, Headless: nctx.Headless})
			outCh <- runOutcome{out: runOut, err: runErr}
		}(prompt, session)

		seenArtifacts := make(map[string]bool)
		for _, a := range nodeArtifacts {
			seenArtifacts[a] = true
		}
		workspaceDir := nctx.RunDir
		if runDirOpt.IsSome() {
			workspaceDir = runDirOpt.Unwrap()
		}

	innerLoop:
		for {
			select {
			case update, ok := <-statusCh:
				if !ok {
					statusCh = nil
					continue
				}
				// Defensive: the server-side match should already filter, but never
				// attribute another node invocation's update to this node.
				if update.RunToken != runToken {
					continue
				}

				var stepArtifacts []string
				if targetFiles, ok := update.Metadata["target_files"].([]string); ok {
					for _, tf := range targetFiles {
						if agents.IsArtifact(tf, &effectiveAgent.Config, workspaceDir) {
							vPath := ViewerArtifactPathInSession(tf, nctx.TmpDir, DefaultSessionDir(nctx.SessionID))
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
								vPath := ViewerArtifactPathInSession(tf, nctx.TmpDir, DefaultSessionDir(nctx.SessionID))
								stepArtifacts = append(stepArtifacts, vPath)
								if !seenArtifacts[vPath] {
									seenArtifacts[vPath] = true
									nodeArtifacts = append(nodeArtifacts, vPath)
								}
							}
						}
					}
				}

				if update.Metadata == nil {
					update.Metadata = make(map[string]any)
				}
				update.Metadata["step_index"] = update.StepIndex
				if len(stepArtifacts) > 0 {
					update.Metadata["artifact_files"] = ToAnySlice(stepArtifacts)
				}

				if nctx.EventEmitter != nil {
					nctx.EventEmitter(WorkflowEvent{
						Type:      EventNodeStatusUpdate,
						NodeID:    node.ID,
						NodeType:  workflowspec.NodeTypeAgent,
						AgentID:   node.AgentID,
						AgentName: effectiveAgent.Config.Name,
						Status:    workflowspec.StatusRunning,
						Message:   update.Content,
						EntryType: update.EntryType,
						Metadata:  SanitizeMetadata(update.Metadata),
						Artifacts: stepArtifacts,
					})
				}
			case outcome := <-outCh:
				out = outcome.out
				executionErr = outcome.err
				if cancelListener != nil {
					cancelListener()
				}
				break innerLoop
			case <-ctx.Done():
				if cancelListener != nil {
					cancelListener()
				}
				return nil, ctx.Err()
			}
		}

		parsedContent, parsedSessionID := parseAgentOutput(out)
		lastContent = parsedContent
		if parsedSessionID != "" {
			currentSessionID = parsedSessionID
			if node.SessionPolicyInherit() {
				nctx.Values.Set(agentSessionKey(node.AgentID), currentSessionID)
			}
		}

		if executionErr != nil {
			log.Error().
				Err(executionErr).
				Str("session_id", nctx.SessionID).
				Str("node_id", node.ID).
				Str("agent_id", node.AgentID).
				Int("attempt", attempt).
				Msgf("[AgentRunner] Agent %q for node %q execution error on attempt %d: %v", node.AgentID, node.ID, attempt, executionErr)
			break
		}

		// Quality gate: check required outputs
		missing := checkRequiredOutputs(node.RequiredOutputs, nctx)
		if len(missing) == 0 {
			log.Info().
				Str("session_id", nctx.SessionID).
				Str("node_id", node.ID).
				Str("agent_id", node.AgentID).
				Int("attempt", attempt).
				Msgf("[AgentRunner] Agent %q for node %q COMPLETED successfully (required outputs satisfied)", node.AgentID, node.ID)
			return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded, Output: lastContent, Artifacts: toArtifactMap(nodeArtifacts), AgentName: effectiveAgent.Config.Name}, nil
		}

		log.Warn().
			Str("session_id", nctx.SessionID).
			Str("node_id", node.ID).
			Str("agent_id", node.AgentID).
			Strs("missing_files", missing).
			Int("attempt", attempt).
			Int("max_attempts", totalAttempts).
			Msgf("[AgentRunner] Required output check failed for node %q (missing %v)", node.ID, missing)

		if attempt < totalAttempts {
			correctiveNotice := fmt.Sprintf("System Notice: Required output file(s) %s are missing or empty. You must write and complete these files now before concluding your turn.", strings.Join(missing, ", "))

			if currentSessionID != "" {
				session = optional.Some(currentSessionID)
				prompt = correctiveNotice
			} else {
				// When no CLI session was returned to resume, keep initial prompt context with the corrective notice appended.
				session = optional.None[string]()
				prompt = initialPrompt + "\n\n" + correctiveNotice
			}

			if nctx.EventEmitter != nil {
				nctx.EventEmitter(WorkflowEvent{
					Type:      EventNodeStatusUpdate,
					NodeID:    node.ID,
					NodeType:  workflowspec.NodeTypeAgent,
					AgentID:   node.AgentID,
					AgentName: effectiveAgent.Config.Name,
					Status:    workflowspec.StatusRunning,
					Message:   fmt.Sprintf("Required outputs missing (%s). Retrying (attempt %d/%d)...", strings.Join(missing, ", "), attempt+1, totalAttempts),
					EntryType: "activity",
				})
			}
		} else {
			qualityGateErr = fmt.Errorf("required outputs missing or empty after %d attempt(s): %s", totalAttempts, strings.Join(missing, ", "))
		}
	}

	if executionErr != nil {
		log.Error().
			Err(executionErr).
			Str("session_id", nctx.SessionID).
			Str("node_id", node.ID).
			Str("agent_id", node.AgentID).
			Msgf("[AgentRunner] Agent %q for node %q execution FAILED: %v", node.AgentID, node.ID, executionErr)
		return &workflowspec.NodeResult{
			Status:    workflowspec.StatusFailed,
			Output:    lastContent,
			Artifacts: toArtifactMap(nodeArtifacts),
			Error:     fmt.Errorf("agent %s run execution failed: %w", node.AgentID, executionErr),
			AgentName: effectiveAgent.Config.Name,
		}, nil
	}

	log.Error().
		Err(qualityGateErr).
		Str("session_id", nctx.SessionID).
		Str("node_id", node.ID).
		Str("agent_id", node.AgentID).
		Msgf("[AgentRunner] Agent %q for node %q quality gate FAILED: %v", node.AgentID, node.ID, qualityGateErr)
	return &workflowspec.NodeResult{
		Status:    workflowspec.StatusFailed,
		Output:    lastContent,
		Artifacts: toArtifactMap(nodeArtifacts),
		Error:     fmt.Errorf("agent %s quality gate failed: %w", node.AgentID, qualityGateErr),
		AgentName: effectiveAgent.Config.Name,
	}, nil
}

// checkRequiredOutputs checks if all required output file paths exist and are non-empty.
// Paths are interpolated against the node context and evaluated against the host filesystem.
// Note: Variable paths like ${tmp_dir}, ${session_dir} and ${run_dir} correspond to directories mounted
// into the sandbox container.
// Returns a slice of missing or empty file paths.
func checkRequiredOutputs(requiredOutputs []string, nctx *NodeContext) []string {
	if len(requiredOutputs) == 0 {
		return nil
	}
	var missing []string
	for _, raw := range requiredOutputs {
		interpolated := strings.TrimSpace(nctx.Interpolate(raw))
		if interpolated == "" || strings.Contains(interpolated, "${") {
			missing = append(missing, raw)
			continue
		}
		path := interpolated
		if !filepath.IsAbs(path) {
			path = filepath.Join(nctx.RunDir, path)
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() == 0 {
			missing = append(missing, interpolated)
		}
	}
	return missing
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
func resolveEffectiveAgent(agent *agentspec.Agent, nctx *NodeContext) *agentspec.Agent {
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

// errQuotaCancelled reports that the user explicitly cancelled the agent run
// while it was suspended waiting for a CLI quota decision.
var errQuotaCancelled = errors.New("run cancelled by user while waiting for CLI quota")

// quotaDecision classifies a user reply to a quota suspension prompt.
type quotaDecision int

const (
	quotaDecisionContinue quotaDecision = iota
	quotaDecisionTarget
	quotaDecisionCancel
)

// runWithQuotaDecisions invokes run.Run and, whenever no CLI target has usable
// quota, suspends the run for a user decision instead of failing the node:
//
//   - "Wait for quota recovery, then continue": re-check quotas and re-suspend
//     with a fresh prompt while still exhausted (manual continue only).
//   - "Use <cli> <model>": force the chosen target on the next attempt even
//     below the automatic-selection threshold (explicit selection only needs
//     quota > 0).
//   - "Cancel run": fail the node with errQuotaCancelled.
//
// Headless runs (or runs without a suspension gateway) keep the legacy
// fail-fast behavior with the informative *run.NoQuotaError.
func (r *agentRunner) runWithQuotaDecisions(ctx context.Context, nctx *NodeContext, node *workflowspec.NodeSpec, agent *agentspec.Agent, prompt string, session optional.Option[string], runDirOpt optional.Option[string], modelOpt optional.Option[string], scope run.StatusScope) ([]byte, error) {
	forcedModel := modelOpt
	for {
		out, runErr := run.Run(ctx, agent, prompt, session, runDirOpt, forcedModel, nctx.SessionID, scope, r.conf)
		var nq *run.NoQuotaError
		if !errors.As(runErr, &nq) {
			return out, runErr
		}
		if nctx.Headless || nctx.SuspendQuota == nil {
			return out, runErr
		}

		log.Warn().
			Err(nq).
			Str("session_id", nctx.SessionID).
			Str("node_id", node.ID).
			Str("agent_id", node.AgentID).
			Msgf("[AgentRunner] Agent %q for node %q has no CLI target with usable quota; suspending for user decision", node.AgentID, node.ID)

		if nctx.EventEmitter != nil {
			nctx.EventEmitter(WorkflowEvent{
				Type:      EventNodeStatusUpdate,
				NodeID:    node.ID,
				NodeType:  workflowspec.NodeTypeAgent,
				AgentID:   node.AgentID,
				AgentName: agent.Config.Name,
				Status:    workflowspec.StatusRunning,
				Message:   "No CLI target has enough quota remaining; waiting for your decision...",
				EntryType: "activity",
			})
		}

		reply, suspErr := nctx.SuspendQuota(buildQuotaPrompt(nq, agent), quotaOptions(nq))
		if suspErr != nil {
			return out, fmt.Errorf("waiting for quota decision: %w", suspErr)
		}

		decision, targetModel := classifyQuotaReply(reply, agent.Config.CLI)
		switch decision {
		case quotaDecisionCancel:
			log.Info().
				Str("session_id", nctx.SessionID).
				Str("node_id", node.ID).
				Str("agent_id", node.AgentID).
				Msgf("[AgentRunner] Agent %q for node %q cancelled by user while waiting for quota", node.AgentID, node.ID)
			return nil, errQuotaCancelled
		case quotaDecisionTarget:
			log.Info().
				Str("session_id", nctx.SessionID).
				Str("node_id", node.ID).
				Str("agent_id", node.AgentID).
				Str("forced_model", targetModel).
				Msgf("[AgentRunner] User forced CLI target %q for agent %q on node %q", targetModel, node.AgentID, node.ID)
			forcedModel = optional.Some(targetModel)
		default:
			// Continue: keep the current selection policy and re-check quota.
		}
	}
}

// buildQuotaPrompt renders the suspension prompt shown to the user, listing
// every configured CLI target with its remaining quota.
func buildQuotaPrompt(nq *run.NoQuotaError, agent *agentspec.Agent) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Agent %q (%s) cannot start: no CLI target has enough quota remaining", agent.Config.Name, agent.Config.ID)
	if nq.ExplicitModel != "" {
		fmt.Fprintf(&sb, " (selected model %s is out of quota)", nq.ExplicitModel)
	}
	sb.WriteString(".\n\nCurrent quota status:\n")
	for _, t := range nq.Targets {
		if !t.Enabled {
			fmt.Fprintf(&sb, "- %s %s: provider disabled\n", t.CLI, t.Model)
			continue
		}
		fmt.Fprintf(&sb, "- %s %s: %.0f%% remaining (automatic selection needs more than %.0f%%)\n", t.CLI, t.Model, t.Remaining*100, nq.MinThreshold*100)
	}
	sb.WriteString("\nYou can wait for quota to recover and continue, force a specific target with whatever quota is left, or cancel the run.")
	return sb.String()
}

// quotaOptions builds the option buttons offered on the quota suspension
// prompt. Labels never contain " / " so the frontend option parser keeps each
// label a single button even though model IDs contain "/".
func quotaOptions(nq *run.NoQuotaError) []string {
	opts := []string{"Wait for quota recovery, then continue"}
	for _, t := range nq.Targets {
		if t.Enabled && t.Remaining > 0 {
			opts = append(opts, fmt.Sprintf("Use %s %s", t.CLI, t.Model))
		}
	}
	return append(opts, "Cancel run")
}

// classifyQuotaReply maps a user reply to a quota decision. Replies match the
// exact option labels produced by quotaOptions, but free-text replies are
// interpreted leniently: any reply naming a configured cli+model forces that
// target, replies containing "cancel" cancel, everything else continues.
func classifyQuotaReply(reply string, targets []agentspec.CLITarget) (quotaDecision, string) {
	lower := strings.ToLower(strings.TrimSpace(reply))
	if lower == "" {
		return quotaDecisionContinue, ""
	}
	if strings.Contains(lower, "cancel") || strings.Contains(lower, "abort") {
		return quotaDecisionCancel, ""
	}
	for _, t := range targets {
		if strings.Contains(lower, strings.ToLower(t.CLI)) && strings.Contains(lower, strings.ToLower(t.Model)) {
			return quotaDecisionTarget, t.Model
		}
	}
	return quotaDecisionContinue, ""
}

func resolveAgentPrompt(nctx *NodeContext, node *workflowspec.NodeSpec, resuming bool) (string, error) {
	switch {
	case resuming:
		return agentFollowUpPrompt, nil
	case node.Entry:
		input := strings.TrimSpace(nctx.Input)
		if input == "" {
			if nctx.Headless {
				return agentStartPrompt, nil
			}
			return "", fmt.Errorf("agent node %q has no prompt: the workflow input is empty", node.ID)
		}
		return input, nil
	default:
		return agentStartPrompt, nil
	}
}
