package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"uuid"

	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/lib/bwrap"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

func IsAllowedDir(path string, allowedDirs []string) bool {
	path = filepath.Clean(path)
	for _, dir := range allowedDirs {
		dir = filepath.Clean(dir)
		if path == dir {
			return true
		}
		// Check if it is a subdirectory
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
		// If allowedDirs contains "/tmp" or "tmp", also allow subdirectories under the host
		// session tmp root (~/asgard/data/tmp); likewise "/session"/"session" allows
		// subdirectories under the host sessions root (~/asgard/data/sessions).
		var nsUserBase string
		switch dir {
		case "/tmp", "tmp":
			nsUserBase = paths.TmpDir()
		case "/session", "session":
			nsUserBase = paths.SessionsDir()
		}
		if nsUserBase != "" && (path == nsUserBase || strings.HasPrefix(path, nsUserBase+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// resolveRunDir resolves the run directory for an agent invocation.
func resolveRunDir(agent *agentspec.Agent, runDirOpt optional.Option[string]) (string, error) {
	if runDirOpt.IsSome() && runDirOpt.Unwrap() != "" {
		rd := runDirOpt.Unwrap()
		if !IsAllowedDir(rd, agent.Config.RunDirs) {
			return "", fmt.Errorf("run directory %q is not allowed by agent configuration", rd)
		}
		return rd, nil
	}
	if len(agent.Config.RunDirs) > 0 && agent.Config.RunDirs[0] != "" {
		return agent.Config.RunDirs[0], nil
	}
	tmpDir := paths.TmpDir()
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("creating tmp directory %q: %w", tmpDir, err)
	}
	uuidDir := filepath.Join(tmpDir, uuid.NewV7().String())
	if err := os.MkdirAll(uuidDir, 0755); err != nil {
		return "", fmt.Errorf("creating uuid run directory %q: %w", uuidDir, err)
	}
	return uuidDir, nil
}

// RunResult holds the output of a single CLI target execution.
type RunResult struct {
	// CLIKey is "<cli>/<model>" identifying the target that produced this result.
	CLIKey string
	Output []byte
	Err    error
}

// StatusScope identifies the workflow node invocation that started an agent
// run. It is injected into the sandbox as ASGARD_NODE_ID / ASGARD_RUN_TOKEN so
// aw can echo them back in status updates, letting the server attribute each
// update to the right node when parallel nodes share a chat ID. The zero value
// (plain single-agent chats) injects nothing.
type StatusScope struct {
	NodeID            string
	RunToken          string
	Headless          bool
	AllowCrossSession bool
}

// runTarget executes a single CLI target in its own bubblewrap sandbox.
func runTarget(ctx context.Context, agent *agentspec.Agent, target agentspec.CLITarget, prompt string, session optional.Option[string], runDir string, chatID string, statusScope StatusScope, conf *config.Config) ([]byte, error) {
	sockDir := paths.SockDir("fakebash-sock-" + uuid.NewV7().String())
	if err := os.MkdirAll(sockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating sock directory %q: %w", sockDir, err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()

	var langRules string
	var configPath string
	var proxyCfg bwrap.ProxySandboxConfig
	if conf != nil {
		langRules = conf.LanguageRules()
		configPath = conf.GetConfigPath()
		proxyCfg = conf.SandboxProxyOptions()
	}

	agentSandboxCmd, err := bwrap.CommandForAgent(&agent.Config, agent.Path, target, prompt, session, runDir, sockDir, chatID, langRules, configPath, proxyCfg)
	if err != nil {
		return nil, fmt.Errorf("creating command for agent: %w", err)
	}

	// Start the command execution sandbox
	cmdSandboxCmd, err := bwrap.CommandForCommandExec(runDir, sockDir, chatID, configPath, statusScope.AllowCrossSession, proxyCfg)
	if err != nil {
		return nil, fmt.Errorf("creating command for command exec: %w", err)
	}

	agentSandboxCmd.Env = append(os.Environ(), "ASGARD_CHAT_ID="+chatID)
	cmdSandboxCmd.Env = append(os.Environ(), "ASGARD_CHAT_ID="+chatID)
	if statusScope.NodeID != "" {
		agentSandboxCmd.Env = append(agentSandboxCmd.Env, "ASGARD_NODE_ID="+statusScope.NodeID)
		cmdSandboxCmd.Env = append(cmdSandboxCmd.Env, "ASGARD_NODE_ID="+statusScope.NodeID)
	}
	if statusScope.RunToken != "" {
		agentSandboxCmd.Env = append(agentSandboxCmd.Env, "ASGARD_RUN_TOKEN="+statusScope.RunToken)
		cmdSandboxCmd.Env = append(cmdSandboxCmd.Env, "ASGARD_RUN_TOKEN="+statusScope.RunToken)
	}
	if statusScope.Headless {
		agentSandboxCmd.Env = append(agentSandboxCmd.Env, "ASGARD_HEADLESS=1")
		cmdSandboxCmd.Env = append(cmdSandboxCmd.Env, "ASGARD_HEADLESS=1")
	}
	if agent != nil {
		agentSandboxCmd.Env = append(agentSandboxCmd.Env, "ASGARD_AGENT_ID="+agent.Config.ID, "ASGARD_AGENT_NAME="+agent.Config.Name)
		cmdSandboxCmd.Env = append(cmdSandboxCmd.Env, "ASGARD_AGENT_ID="+agent.Config.ID, "ASGARD_AGENT_NAME="+agent.Config.Name)
	}
	if conf != nil {
		statusURL := conf.StatusURL()
		internalHost := conf.InternalAPIHost()
		apiHost := conf.APIHost()
		agentSandboxCmd.Env = append(agentSandboxCmd.Env,
			"ASGARD_STATUS_URL="+statusURL,
			"ASGARD_INTERNAL_API_HOST="+internalHost,
			"ASGARD_API_HOST="+apiHost,
		)
		cmdSandboxCmd.Env = append(cmdSandboxCmd.Env,
			"ASGARD_STATUS_URL="+statusURL,
			"ASGARD_INTERNAL_API_HOST="+internalHost,
			"ASGARD_API_HOST="+apiHost,
		)
	} else {
		if envHost := os.Getenv("ASGARD_API_HOST"); envHost != "" {
			agentSandboxCmd.Env = append(agentSandboxCmd.Env, "ASGARD_API_HOST="+envHost)
			cmdSandboxCmd.Env = append(cmdSandboxCmd.Env, "ASGARD_API_HOST="+envHost)
		}
		if statusURL := os.Getenv("ASGARD_STATUS_URL"); statusURL != "" {
			internalHost := strings.TrimSuffix(statusURL, "/agent-status")
			agentSandboxCmd.Env = append(agentSandboxCmd.Env, "ASGARD_STATUS_URL="+statusURL, "ASGARD_INTERNAL_API_HOST="+internalHost)
			cmdSandboxCmd.Env = append(cmdSandboxCmd.Env, "ASGARD_STATUS_URL="+statusURL, "ASGARD_INTERNAL_API_HOST="+internalHost)
		}
	}

	cmdSandboxCmd.Stdout = os.Stdout
	cmdSandboxCmd.Stderr = os.Stderr

	if err := cmdSandboxCmd.Start(); err != nil {
		return nil, fmt.Errorf("starting command execution sandbox: %w", err)
	}

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	agentSandboxCmd.Stdout = &stdoutBuf
	agentSandboxCmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)

	if err := agentSandboxCmd.Start(); err != nil {
		_ = cmdSandboxCmd.Process.Kill()
		_, _ = cmdSandboxCmd.Process.Wait()
		return nil, fmt.Errorf("starting agent sandbox command: %w", err)
	}

	defer func() {
		if cmdSandboxCmd.Process != nil {
			_ = cmdSandboxCmd.Process.Kill()
			_, _ = cmdSandboxCmd.Process.Wait()
		}
	}()

	var agentErr error
	done := make(chan struct{})
	go func() {
		agentErr = agentSandboxCmd.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		if agentSandboxCmd.Process != nil {
			_ = agentSandboxCmd.Process.Kill()
		}
		<-done
		return stdoutBuf.Bytes(), ctx.Err()
	}

	// Kill the command execution sandbox (fakebashd) now that the agent process has finished.
	if cmdSandboxCmd.Process != nil {
		_ = cmdSandboxCmd.Process.Kill()
		_, _ = cmdSandboxCmd.Process.Wait()
	}

	out := stdoutBuf.Bytes()
	if agentErr != nil {
		return out, fmt.Errorf("%w%s", agentErr, stderrTail(stderrBuf.String()))
	}

	return out, nil
}

// stderrTail appends the last non-empty lines of the agent's stderr output to
// an error message so CLI failures (e.g. missing required flags) are visible
// to callers instead of only in server logs.
func stderrTail(stderr string) string {
	const maxTailLen = 1000
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	tail := strings.Join(lines, "\n")
	if len(tail) > maxTailLen {
		tail = tail[len(tail)-maxTailLen:]
	}
	if strings.TrimSpace(tail) == "" {
		return ""
	}
	return ": " + tail
}

const MinAutoQuotaThreshold = 0.10 // 10% minimum remaining quota for automatic selection

// QuotaTargetStatus describes the quota state of one configured CLI target.
type QuotaTargetStatus struct {
	CLI       string  `json:"cli"`
	Model     string  `json:"model"`
	Remaining float64 `json:"remaining"`
	Enabled   bool    `json:"enabled"`
}

// NoQuotaError reports that no CLI target of an agent can currently be run:
// either every enabled target is at or below MinAutoQuotaThreshold (automatic
// selection) or the explicitly selected model has no quota left. It carries a
// per-target snapshot so callers (e.g. the workflow engine) can surface the
// quota situation to the user and offer targeted overrides.
type NoQuotaError struct {
	AgentID       string              `json:"agent_id"`
	ExplicitModel string              `json:"explicit_model,omitempty"`
	MinThreshold  float64             `json:"min_threshold"`
	Targets       []QuotaTargetStatus `json:"targets"`
	// PairingNote, when set, marks the error as a workflow model-pairing
	// failure (the exhausted list came from a pairing table rather than the
	// agent's own cli list) and carries a human-readable pairing context.
	PairingNote string `json:"pairing_note,omitempty"`
}

func (e *NoQuotaError) Error() string {
	var sb strings.Builder
	if e.PairingNote != "" {
		fmt.Fprintf(&sb, "model pairing unsatisfiable (%s): ", e.PairingNote)
	}
	if e.ExplicitModel != "" {
		fmt.Fprintf(&sb, "model %q has no quota remaining for agent %s", e.ExplicitModel, e.AgentID)
	} else {
		fmt.Fprintf(&sb, "no CLI target with more than %.0f%% quota remaining is available for agent %s", e.MinThreshold*100, e.AgentID)
	}
	for _, t := range e.Targets {
		state := fmt.Sprintf("%.0f%% quota remaining", t.Remaining*100)
		if !t.Enabled {
			state = "provider disabled"
		}
		fmt.Fprintf(&sb, "; %s %s: %s", t.CLI, t.Model, state)
	}
	return sb.String()
}

// quotaStatuses snapshots the quota state of every given CLI target.
// Disabled providers are recorded without a usage query.
func quotaStatuses(targets []agentspec.CLITarget, conf *config.Config) []QuotaTargetStatus {
	statuses := make([]QuotaTargetStatus, 0, len(targets))
	for _, target := range targets {
		st := QuotaTargetStatus{
			CLI:     target.CLI,
			Model:   target.Model,
			Enabled: conf.IsProviderEnabled(target.CLI),
		}
		if st.Enabled {
			st.Remaining = agentwrapper.CheckQuota(target.CLI, target.Model)
		}
		statuses = append(statuses, st)
	}
	return statuses
}

// SessionMap maps a CLI name to the session ID that CLI previously opened
// (e.g. {"simplest": "<uuid>"}). Session IDs are only resumable by the CLI
// that created them, so the selected target only resumes the session stored
// under its own CLI name; a target switch (quota fallback, user override)
// intentionally starts a fresh session instead of leaking a foreign session
// ID into an unrelated CLI (which fails with "Session not found").
type SessionMap map[string]string

// Clone returns a copy of m (nil-safe), so callers can mutate the copy
// without aliasing the original.
func (m SessionMap) Clone() SessionMap {
	out := make(SessionMap, len(m))
	for cli, sid := range m {
		out[cli] = sid
	}
	return out
}

// With returns a clone of m in which sessions[cli] = sid.
func (m SessionMap) With(cli, sid string) SessionMap {
	out := m.Clone()
	out[cli] = sid
	return out
}

// Run checks the remaining quota for each CLI target configured on the agent.
// It runs the bubblewrap command for the selected target or the first target that has more than 10% quota remaining.
// If a specific model is selected (modelOpt is Some), it checks if that model exists in agent.Config.CLI.
// If selected model has <= 0 quota, it returns an error immediately with NO fallback.
// Quota exhaustion is reported as *NoQuotaError so callers can distinguish it
// from execution failures and react (e.g. suspend for a user decision).
func Run(ctx context.Context, agent *agentspec.Agent, prompt string, sessions SessionMap, runDirOpt optional.Option[string], modelOpt optional.Option[string], chatID string, statusScope StatusScope, conf *config.Config) ([]byte, agentspec.CLITarget, error) {
	return RunWithCandidates(ctx, agent, nil, prompt, sessions, runDirOpt, modelOpt, chatID, statusScope, conf)
}

// RunWithCandidates extends Run with an explicit ordered candidate list.
// When candidates is non-empty it replaces agent.Config.CLI as the selection
// list for both automatic and explicit model selection (workflow model
// pairing); when nil the agent's own list is used unchanged. It additionally
// reports the selected target (also returned when execution itself fails, so
// callers can attribute failed runs) for callers to record the actual
// (cli, model) per node execution.
//
// sessions carries per-CLI session IDs (see SessionMap); only the entry
// matching the selected target's CLI is resumed.
func RunWithCandidates(ctx context.Context, agent *agentspec.Agent, candidates []agentspec.CLITarget, prompt string, sessions SessionMap, runDirOpt optional.Option[string], modelOpt optional.Option[string], chatID string, statusScope StatusScope, conf *config.Config) ([]byte, agentspec.CLITarget, error) {
	targets := agent.Config.CLI
	if len(candidates) > 0 {
		targets = candidates
	}
	if len(targets) == 0 {
		return nil, agentspec.CLITarget{}, fmt.Errorf("no CLI targets configured for agent %s", agent.Config.ID)
	}

	var selectedTarget *agentspec.CLITarget
	if modelOpt.IsSome() && modelOpt.Unwrap() != "" {
		reqModel := modelOpt.Unwrap()
		for _, target := range targets {
			if target.Model == reqModel {
				selectedTarget = &target
				break
			}
		}
		if selectedTarget == nil {
			return nil, agentspec.CLITarget{}, fmt.Errorf("selected model %q is not in configured model list for agent %s", reqModel, agent.Config.ID)
		}
		if !conf.IsProviderEnabled(selectedTarget.CLI) {
			return nil, agentspec.CLITarget{}, fmt.Errorf("provider %q for model %q is disabled in configuration", selectedTarget.CLI, reqModel)
		}
		quota := agentwrapper.CheckQuota(selectedTarget.CLI, selectedTarget.Model)
		if quota <= 0 {
			return nil, agentspec.CLITarget{}, &NoQuotaError{
				AgentID:       agent.Config.ID,
				ExplicitModel: reqModel,
				MinThreshold:  MinAutoQuotaThreshold,
				Targets:       quotaStatuses(targets, conf),
			}
		}
	} else {
		hasEnabledTarget := false
		var statuses []QuotaTargetStatus
		for _, target := range targets {
			if !conf.IsProviderEnabled(target.CLI) {
				statuses = append(statuses, QuotaTargetStatus{CLI: target.CLI, Model: target.Model, Enabled: false})
				continue
			}
			hasEnabledTarget = true
			quota := agentwrapper.CheckQuota(target.CLI, target.Model)
			statuses = append(statuses, QuotaTargetStatus{CLI: target.CLI, Model: target.Model, Remaining: quota, Enabled: true})
			if quota > MinAutoQuotaThreshold {
				selectedTarget = &target
				break
			}
		}

		if selectedTarget == nil {
			if !hasEnabledTarget {
				return nil, agentspec.CLITarget{}, fmt.Errorf("no enabled CLI targets available for agent %s", agent.Config.ID)
			}
			return nil, agentspec.CLITarget{}, &NoQuotaError{
				AgentID:      agent.Config.ID,
				MinThreshold: MinAutoQuotaThreshold,
				Targets:      statuses,
			}
		}
	}

	runDir, err := resolveRunDir(agent, runDirOpt)
	if err != nil {
		return nil, agentspec.CLITarget{}, err
	}

	// Ensure the resolved runDir exists (e.g. if it was a subdirectory under config run_dirs that was not created yet)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, agentspec.CLITarget{}, fmt.Errorf("creating run directory %q: %w", runDir, err)
	}

	session := optional.None[string]()
	if sid := sessions[selectedTarget.CLI]; sid != "" {
		session = optional.Some(sid)
	}

	out, err := runTarget(ctx, agent, *selectedTarget, prompt, session, runDir, chatID, statusScope, conf)
	return out, *selectedTarget, err
}
