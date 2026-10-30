package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"uuid"

	"github.com/moznion/go-optional"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

func withNodeTimeout(ctx context.Context, node *workflowspec.NodeSpec) (context.Context, context.CancelFunc) {
	if d := node.TimeoutDuration(); d > 0 {
		return context.WithTimeout(ctx, d)
	}
	return context.WithCancel(ctx)
}

// agentSessionKey is the RunValues key under which the per-CLI session IDs of
// an agent (map[cli]sessionID) are inherited between nodes using the same
// agent_id. Session IDs are only resumable by the CLI that created them, so
// they are tracked per CLI: when a later node switches CLI (quota fallback,
// model pairing), it resumes its own CLI's session instead of receiving a
// foreign session ID.
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

	// ArtifactsKnown marks Artifacts as computed by the runner (RunEvent
	// artifacts): receivers use them instead of re-deriving artifacts from
	// target_files on the backend filesystem. Never set from the wire.
	ArtifactsKnown bool `json:"-"`
	// Artifacts are the viewer paths of the run's artifact files so far.
	Artifacts []string `json:"-"`
}

// StatusUpdateFromRunEvent converts a runner's RunEvent into the status update
// the rest of the backend consumes (the same shape POST /agent-status
// delivers), carrying the runner-computed artifacts.
func StatusUpdateFromRunEvent(ev *runnerpb.RunEvent) AgentStatusUpdate {
	u := AgentStatusUpdate{
		ChatID:         ev.GetChatId(),
		NodeID:         ev.GetNodeId(),
		RunToken:       ev.GetRunToken(),
		StepIndex:      int(ev.GetStepIndex()),
		Source:         ev.GetSource(),
		EntryType:      ev.GetEntryType(),
		Content:        ev.GetContent(),
		ArtifactsKnown: true,
	}
	if b := ev.GetMetadataJson(); len(b) > 0 {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			log.Warn().Err(err).Str("run_id", ev.GetRunId()).Msg("dropping undecodable run event metadata")
		} else {
			u.Metadata = m
		}
	}
	for _, a := range ev.GetArtifacts() {
		if a.GetViewerPath() != "" {
			u.Artifacts = append(u.Artifacts, a.GetViewerPath())
		}
	}
	return u
}

// StatusPublisher is implemented by status listeners that can also accept
// updates produced outside the HTTP status endpoint (runner RunEvents).
type StatusPublisher interface {
	PublishStatusUpdate(AgentStatusUpdate)
}

// agentRunner executes agent nodes by invoking CLI agents inside sandboxes.
type agentRunner struct {
	loader         agentspec.Lister
	conf           *config.Config
	statusListener AgentStatusListener
	dispatcher     dispatch.Dispatcher
	// sessionAgents, when set, returns the agents visible to a chat (global
	// plus its runner's own); ok=false falls back to the global cache.
	sessionAgents func(sessionID string) ([]*agentspec.Agent, bool)

	mu     sync.Mutex
	agents map[string]*agentspec.Agent
	loaded bool
}

// NewAgentRunner creates the runner for `agent` nodes. The loader resolves
// agent_id references lazily (and re-resolves after agent reloads on cache miss).
func NewAgentRunner(loader agentspec.Lister, conf *config.Config) NodeRunner {
	return NewAgentRunnerWithListener(loader, conf, nil)
}

// NewAgentRunnerWithListener creates the runner for `agent` nodes with an optional status listener.
//
// Runs execute in-process (dispatch.Local); production wiring uses
// NewAgentRunnerWithDispatcher to send them to the chat's runner.
func NewAgentRunnerWithListener(loader agentspec.Lister, conf *config.Config, listener AgentStatusListener) NodeRunner {
	return NewAgentRunnerWithDispatcher(loader, conf, listener, dispatch.NewLocal(conf), nil)
}

// NewAgentRunnerWithDispatcher creates the runner for `agent` nodes executing
// runs through d. sessionAgents optionally resolves the agents visible to a
// chat (its runner's effective list).
func NewAgentRunnerWithDispatcher(loader agentspec.Lister, conf *config.Config, listener AgentStatusListener, d dispatch.Dispatcher, sessionAgents func(sessionID string) ([]*agentspec.Agent, bool)) NodeRunner {
	return &agentRunner{loader: loader, conf: conf, statusListener: listener, dispatcher: d, sessionAgents: sessionAgents, agents: make(map[string]*agentspec.Agent)}
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

// lookupFor resolves an agent for a chat: the chat's runner-specific list
// first (global agents plus the runner's own), the global cache otherwise.
func (r *agentRunner) lookupFor(sessionID, agentID string) (*agentspec.Agent, error) {
	if r.sessionAgents != nil && sessionID != "" {
		if list, ok := r.sessionAgents(sessionID); ok {
			for _, a := range list {
				if a != nil && a.Config.ID == agentID {
					return a, nil
				}
			}
			return nil, fmt.Errorf("agent %q not found", agentID)
		}
	}
	return r.lookup(agentID)
}

// Lookup resolves an agent by ID (lazily loading the agent directory).
func (r *agentRunner) Lookup(agentID string) (*agentspec.Agent, error) { return r.lookup(agentID) }

func (r *agentRunner) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	node := nctx.Node

	agent, err := r.lookupFor(nctx.SessionID, node.AgentID)
	if err != nil {
		return nil, err
	}

	// Agent nodes carry no YAML prompt; the runner seeds the CLI prompt from
	// the workflow context: entry nodes receive the raw user input, other
	// fresh nodes get a kickoff directive (their inputs are files produced
	// by earlier nodes), resumed sessions get a follow-up directive.
	prompt := strings.TrimSpace(nctx.Interpolate(node.Prompt))

	// Session policy: `fresh` always starts a clean CLI session; `inherit`
	// (default) resumes the per-CLI sessions a previous node opened for this
	// agent.
	sessions := run.SessionMap{}
	resuming := false
	if node.SessionPolicyInherit() {
		if v, ok := nctx.Values.Get(agentSessionKey(node.AgentID)); ok {
			if m, ok := v.(run.SessionMap); ok && len(m) > 0 {
				sessions = m.Clone()
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

	// Model pairing: when this node is the reviewer of a model_pairings
	// group, its candidate list comes from the pairing table keyed by the
	// actually-used target of the group's latest completed actor. Explicit
	// node-level model selection bypasses pairing entirely.
	pairing, err := resolvePairingPlan(nctx, node)
	if err != nil {
		return nil, err
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
	var lastTarget agentspec.CLITarget
	var targetKnown bool
	var userForced bool

	// dispatched counts the StartRuns of this node invocation (corrective
	// retries and quota re-checks included): it is the logical attempt number.
	var dispatched uint32

	totalAttempts := maxRetries + 1
	for attempt := 1; attempt <= totalAttempts; attempt++ {
		executionErr = nil
		qualityGateErr = nil
		userForced = false

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
			out        []byte
			target     agentspec.CLITarget
			userForced bool
			err        error
		}
		outCh := make(chan runOutcome, 1)

		go func(currentPrompt string, currentSessions run.SessionMap) {
			runOut, runTarget, runForced, runErr := r.runWithQuotaDecisions(ctx, nctx, node, effectiveAgent, currentPrompt, currentSessions, runDirOpt, modelOpt, run.StatusScope{NodeID: node.ID, RunToken: runToken, Headless: nctx.Headless, AllowCrossSession: nctx.AllowCrossSession}, pairing, &dispatched)
			outCh <- runOutcome{out: runOut, target: runTarget, userForced: runForced, err: runErr}
		}(prompt, sessions)

		seenArtifacts := make(map[string]bool)
		for _, a := range nodeArtifacts {
			seenArtifacts[a] = true
		}
		workspaceDir := nctx.RunDir
		if runDirOpt.IsSome() {
			workspaceDir = runDirOpt.Unwrap()
		}

		processUpdate := func(update AgentStatusUpdate) {
			// Defensive: the server-side match should already filter, but never
			// attribute another node invocation's update to this node.
			if update.RunToken != runToken {
				return
			}

			var stepArtifacts []string
			if update.ArtifactsKnown {
				// The runner already decided which files are artifacts.
				for _, vPath := range update.Artifacts {
					stepArtifacts = append(stepArtifacts, vPath)
					if !seenArtifacts[vPath] {
						seenArtifacts[vPath] = true
						nodeArtifacts = append(nodeArtifacts, vPath)
					}
				}
			} else if targetFiles, ok := update.Metadata["target_files"].([]string); ok {
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
		}

	innerLoop:
		for {
			select {
			case update, ok := <-statusCh:
				if !ok {
					statusCh = nil
					continue
				}
				processUpdate(update)
			case outcome := <-outCh:
				// Events precede the result on the wire: flush what is
				// already buffered so none is lost to the select order.
				for draining := statusCh != nil; draining; {
					select {
					case update, ok := <-statusCh:
						if !ok {
							draining = false
							break
						}
						processUpdate(update)
					default:
						draining = false
					}
				}
				out = outcome.out
				executionErr = outcome.err
				userForced = outcome.userForced
				if outcome.target.CLI != "" || outcome.target.Model != "" {
					lastTarget = outcome.target
					targetKnown = true
				}
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
			if node.SessionPolicyInherit() && targetKnown && lastTarget.CLI != "" {
				// Record the session under the CLI that opened it so later
				// nodes (possibly on a different CLI) resume correctly.
				nctx.Values.Set(agentSessionKey(node.AgentID), sessions.With(lastTarget.CLI, currentSessionID))
			}
		}

		if targetKnown && executionErr == nil {
			emitModelSelection(nctx, node, effectiveAgent, lastTarget, pairing, userForced)
			recordActualTarget(nctx, node.ID, lastTarget.CLI, lastTarget.Model)
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
		missing := checkRequiredOutputs(ctx, node.RequiredOutputs, nctx)
		if len(missing) == 0 {
			log.Info().
				Str("session_id", nctx.SessionID).
				Str("node_id", node.ID).
				Str("agent_id", node.AgentID).
				Int("attempt", attempt).
				Msgf("[AgentRunner] Agent %q for node %q COMPLETED successfully (required outputs satisfied)", node.AgentID, node.ID)
			return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded, Output: lastContent, Artifacts: toArtifactMap(nodeArtifacts), AgentName: effectiveAgent.Config.Name, CLI: lastTarget.CLI, Model: lastTarget.Model}, nil
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

			if currentSessionID != "" && targetKnown && lastTarget.CLI != "" {
				// Retry in the session this attempt's target opened; if quota
				// fallback picks a different CLI, run.Run resolves its own
				// session from the map (fresh if none).
				sessions = sessions.With(lastTarget.CLI, currentSessionID)
				prompt = correctiveNotice
			} else {
				// When no CLI session was returned to resume, keep initial prompt context with the corrective notice appended.
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
			CLI:       lastTarget.CLI,
			Model:     lastTarget.Model,
		}, nil
	}

	log.Error().
		Err(qualityGateErr).
		Str("session_id", nctx.SessionID).
		Str("node_id", node.ID).
		Str("agent_id", node.AgentID).
		Msgf("[AgentRunner] Agent %q for node %q quality gate FAILED: %v", node.AgentID, node.ID, qualityGateErr)
	// The node did run on lastTarget even though it settled FAILED. The
	// recorded target is informational here (surfaced via node results and
	// persistence for attribution): pairing baselines come from the live
	// RunValues records within a run, and the restart fallback in
	// latestActorTarget only reads succeeded upstream results — a FAILED
	// node's CLI/Model never re-drives a pairing decision.
	return &workflowspec.NodeResult{
		Status:    workflowspec.StatusFailed,
		Output:    lastContent,
		Artifacts: toArtifactMap(nodeArtifacts),
		Error:     fmt.Errorf("agent %s quality gate failed: %w", node.AgentID, qualityGateErr),
		AgentName: effectiveAgent.Config.Name,
		CLI:       lastTarget.CLI,
		Model:     lastTarget.Model,
	}, nil
}

// checkRequiredOutputs checks if all required output file paths exist and are non-empty.
// Paths are interpolated against the node context and evaluated through the chat's runnerfs.
// Note: Variable paths like ${tmp_dir}, ${session_dir} and ${run_dir} correspond to directories mounted
// into the sandbox container. Missing paths are returned as sandbox-visible paths (e.g. /session/plan.md,
// /tmp/output.md) so corrective notices sent to sandbox agents do not leak host physical paths.
// Returns a slice of missing or empty file paths.
func checkRequiredOutputs(ctx context.Context, requiredOutputs []string, nctx *NodeContext) []string {
	if len(requiredOutputs) == 0 {
		return nil
	}
	sessionDir := nctx.SessionDir
	if sessionDir == "" && nctx.SessionID != "" {
		sessionDir = DefaultSessionDir(nctx.SessionID)
	}
	ops := opsOf(ctx, nctx)
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
		info, err := ops.stat(ctx, path)
		if err != nil || !info.Exists || info.IsDir || info.Size == 0 {
			missing = append(missing, ViewerArtifactPathInSession(path, nctx.TmpDir, sessionDir))
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
	effectiveConfig.MountDirs = agent.Config.MountDirs.Clone()

	if len(effectiveConfig.RunDirs) == 0 && len(nctx.WorkflowRunDirs) > 0 {
		effectiveConfig.RunDirs = append([]string(nil), nctx.WorkflowRunDirs...)
	}
	if len(effectiveConfig.MountDirs.ReadOnly) == 0 && len(nctx.WorkflowMountDirs.ReadOnly) > 0 {
		effectiveConfig.MountDirs.ReadOnly = append([]string(nil), nctx.WorkflowMountDirs.ReadOnly...)
	}
	if !effectiveConfig.IsReadOnly() && len(effectiveConfig.MountDirs.ReadWrite) == 0 && len(nctx.WorkflowMountDirs.ReadWrite) > 0 {
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

// QuotaDecision classifies a user reply to a quota suspension prompt.
type QuotaDecision int

const (
	QuotaDecisionContinue QuotaDecision = iota
	QuotaDecisionTarget
	QuotaDecisionCancel
)

// runWithQuotaDecisions dispatches the run and, whenever no CLI
// target has usable quota, suspends the run for a user decision instead of
// failing the node:
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
//
// pairing, when non-nil, supplies the reviewer candidate list from the
// workflow model-pairing table (replacing the agent's own cli list) and
// annotates quota suspensions as pairing-unsatisfiable.
//
// The userForced return reports that the target was forced by the user
// through a suspension option ("Use <cli> <model>"), so callers can suppress
// fallback-style observability for that execution.
func (r *agentRunner) runWithQuotaDecisions(ctx context.Context, nctx *NodeContext, node *workflowspec.NodeSpec, agent *agentspec.Agent, prompt string, sessions run.SessionMap, runDirOpt optional.Option[string], modelOpt optional.Option[string], scope run.StatusScope, pairing *pairingPlan, attempts *uint32) (out []byte, target agentspec.CLITarget, userForced bool, err error) {
	var candidates []agentspec.CLITarget
	if pairing != nil {
		candidates = pairing.Candidates
	}
	effectiveTargets := agent.Config.CLI
	if len(candidates) > 0 {
		effectiveTargets = candidates
	}

	forcedModel := modelOpt
	for {
		*attempts++
		res, runErr := r.dispatcher.Run(ctx, dispatch.Request{
			ChatID:     nctx.SessionID,
			UserID:     nctx.UserSubject,
			Agent:      agent,
			Candidates: candidates,
			Prompt:     prompt,
			Sessions:   sessions,
			RunDir:     runDirOpt,
			Model:      forcedModel,
			Scope:      scope,
			Attempt:    *attempts,
			OnEvent:    r.publishRunEvent(nctx.SessionID),
		})
		out, target := res.Output, res.Target
		var nq *run.NoQuotaError
		if errors.As(runErr, &nq) {
			if pairing != nil && nq.PairingNote == "" {
				nq.PairingNote = fmt.Sprintf("group %q: actor %s used %s, all paired reviewer targets are exhausted", pairing.GroupID, pairing.ActorNodeID, pairing.ActorTarget)
			}
			if nctx.Headless || nctx.SuspendQuota == nil {
				return out, target, false, runErr
			}

			log.Warn().
				Err(nq).
				Str("session_id", nctx.SessionID).
				Str("node_id", node.ID).
				Str("agent_id", node.AgentID).
				Msgf("[AgentRunner] Agent %q for node %q has no CLI target with usable quota; suspending for user decision", node.AgentID, node.ID)

			if nctx.EventEmitter != nil {
				message := "No CLI target has enough quota remaining; waiting for your decision..."
				if nq.PairingNote != "" {
					message = "Model pairing cannot be satisfied (" + nq.PairingNote + "); waiting for your decision..."
				}
				nctx.EventEmitter(WorkflowEvent{
					Type:      EventNodeStatusUpdate,
					NodeID:    node.ID,
					NodeType:  workflowspec.NodeTypeAgent,
					AgentID:   node.AgentID,
					AgentName: agent.Config.Name,
					Status:    workflowspec.StatusRunning,
					Message:   message,
					EntryType: "activity",
				})
			}

			reply, suspErr := nctx.SuspendQuota(BuildQuotaPrompt(nq, agent), QuotaOptions(nq))
			if suspErr != nil {
				return out, target, false, fmt.Errorf("waiting for quota decision: %w", suspErr)
			}

			decision, targetModel := ClassifyQuotaReply(reply, effectiveTargets)
			switch decision {
			case QuotaDecisionCancel:
				log.Info().
					Str("session_id", nctx.SessionID).
					Str("node_id", node.ID).
					Str("agent_id", node.AgentID).
					Msgf("[AgentRunner] Agent %q for node %q cancelled by user while waiting for quota", node.AgentID, node.ID)
				return nil, target, false, errQuotaCancelled
			case QuotaDecisionTarget:
				log.Info().
					Str("session_id", nctx.SessionID).
					Str("node_id", node.ID).
					Str("agent_id", node.AgentID).
					Str("forced_model", targetModel).
					Msgf("[AgentRunner] User forced CLI target %q for agent %q on node %q", targetModel, node.AgentID, node.ID)
				forcedModel = optional.Some(targetModel)
				userForced = true
			default:
				// Continue: keep the current selection policy and re-check quota.
			}
			continue
		}
		return out, target, userForced, runErr
	}
}

// publishRunEvent forwards a runner's RunEvents into the status listener of
// the chat, the same path POST /agent-status updates take. It returns nil when
// the listener cannot accept published updates.
func (r *agentRunner) publishRunEvent(sessionID string) func(*runnerpb.RunEvent) {
	pub, ok := r.statusListener.(StatusPublisher)
	if !ok || sessionID == "" {
		return nil
	}
	return func(ev *runnerpb.RunEvent) {
		u := StatusUpdateFromRunEvent(ev)
		if u.ChatID == "" {
			u.ChatID = sessionID
		}
		pub.PublishStatusUpdate(u)
	}
}

// BuildQuotaPrompt renders the suspension prompt shown to the user, listing
// every configured CLI target with its remaining quota. Pairing-unsatisfiable
// suspensions lead with the pairing context (group/actor/actual target) from
// NoQuotaError.PairingNote so the decision surface explains why the reviewer
// is restricted to its paired candidates.
func BuildQuotaPrompt(nq *run.NoQuotaError, agent *agentspec.Agent) string {
	var sb strings.Builder
	if nq.PairingNote != "" {
		fmt.Fprintf(&sb, "Model pairing cannot be satisfied (%s).\n", nq.PairingNote)
	}
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

// QuotaOptions builds the option buttons offered on the quota suspension
// prompt. Labels never contain " / " so the frontend option parser keeps each
// label a single button even though model IDs contain "/".
func QuotaOptions(nq *run.NoQuotaError) []string {
	opts := []string{"Wait for quota recovery, then continue"}
	for _, t := range nq.Targets {
		if t.Enabled && t.Remaining > 0 {
			opts = append(opts, fmt.Sprintf("Use %s %s", t.CLI, t.Model))
		}
	}
	return append(opts, "Cancel run")
}

// ClassifyQuotaReply maps a user reply to a quota decision. Replies match the
// exact option labels produced by QuotaOptions, but free-text replies are
// interpreted leniently: any reply naming a configured cli+model forces that
// target, replies containing "cancel" cancel, everything else continues.
func ClassifyQuotaReply(reply string, targets []agentspec.CLITarget) (QuotaDecision, string) {
	lower := strings.ToLower(strings.TrimSpace(reply))
	if lower == "" {
		return QuotaDecisionContinue, ""
	}
	if strings.Contains(lower, "cancel") || strings.Contains(lower, "abort") {
		return QuotaDecisionCancel, ""
	}
	for _, t := range targets {
		if strings.Contains(lower, strings.ToLower(t.CLI)) && strings.Contains(lower, strings.ToLower(t.Model)) {
			return QuotaDecisionTarget, t.Model
		}
	}
	return QuotaDecisionContinue, ""
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
