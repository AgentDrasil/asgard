package run

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/agentwrapper"
	"github.com/AgentDrasil/asgard/lib/bwrap"
	"github.com/AgentDrasil/asgard/lib/config"
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
	}
	return false
}

// resolveRunDir resolves the run directory for an agent invocation.
func resolveRunDir(agent *agents.Agent, runDirOpt optional.Option[string]) (string, error) {
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
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting user home directory: %w", err)
	}
	tmpDir := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("creating tmp directory %q: %w", tmpDir, err)
	}
	uuidDir := filepath.Join(tmpDir, uuid.Must(uuid.NewV7()).String())
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

// runTarget executes a single CLI target in its own bubblewrap sandbox.
func runTarget(ctx context.Context, agent *agents.Agent, target agents.CLITarget, prompt string, session optional.Option[string], runDir string, chatID string, conf *config.Config) ([]byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting user home directory: %w", err)
	}
	sockDir := filepath.Join(home, "tmp", "fakebash-sock-"+uuid.Must(uuid.NewV7()).String())
	if err := os.MkdirAll(sockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating sock directory %q: %w", sockDir, err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()

	agentSandboxCmd, err := bwrap.CommandForAgent(&agent.Config, agent.Path, target, prompt, session, runDir, sockDir, chatID)
	if err != nil {
		return nil, fmt.Errorf("creating command for agent: %w", err)
	}

	// Start the command execution sandbox
	cmdSandboxCmd, err := bwrap.CommandForCommandExec(runDir, sockDir, chatID)
	if err != nil {
		return nil, fmt.Errorf("creating command for command exec: %w", err)
	}

	agentSandboxCmd.Env = append(os.Environ(), "ASGARD_CHAT_ID="+chatID)
	cmdSandboxCmd.Env = append(os.Environ(), "ASGARD_CHAT_ID="+chatID)
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
	agentSandboxCmd.Stdout = &stdoutBuf
	agentSandboxCmd.Stderr = os.Stderr

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
		return out, agentErr
	}

	return out, nil
}

const MinAutoQuotaThreshold = 0.10 // 10% minimum remaining quota for automatic selection

// Run checks the remaining quota for each CLI target configured on the agent.
// It runs the bubblewrap command for the selected target or the first target that has more than 10% quota remaining.
// If a specific model is selected (modelOpt is Some), it checks if that model exists in agent.Config.CLI.
// If selected model has <= 0 quota, it returns an error immediately with NO fallback.
func Run(ctx context.Context, agent *agents.Agent, prompt string, session optional.Option[string], runDirOpt optional.Option[string], modelOpt optional.Option[string], chatID string, conf *config.Config) ([]byte, error) {
	if len(agent.Config.CLI) == 0 {
		return nil, fmt.Errorf("no CLI targets configured for agent %s", agent.Config.ID)
	}

	var selectedTarget *agents.CLITarget
	if modelOpt.IsSome() && modelOpt.Unwrap() != "" {
		reqModel := modelOpt.Unwrap()
		for _, target := range agent.Config.CLI {
			if target.Model == reqModel {
				t := target
				selectedTarget = &t
				break
			}
		}
		if selectedTarget == nil {
			return nil, fmt.Errorf("selected model %q is not in configured model list for agent %s", reqModel, agent.Config.ID)
		}
		quota := agentwrapper.CheckQuota(selectedTarget.CLI, selectedTarget.Model)
		if quota <= 0 {
			return nil, fmt.Errorf("model %q has no quota remaining (quota: %.2f)", selectedTarget.Model, quota)
		}
	} else {
		for _, target := range agent.Config.CLI {
			quota := agentwrapper.CheckQuota(target.CLI, target.Model)
			if quota > MinAutoQuotaThreshold {
				selectedTarget = &target
				break
			}
		}
	}

	if selectedTarget == nil {
		return nil, fmt.Errorf("no CLI target with more than 10%% quota remaining is available for agent %s", agent.Config.ID)
	}

	runDir, err := resolveRunDir(agent, runDirOpt)
	if err != nil {
		return nil, err
	}

	// Ensure the resolved runDir exists (e.g. if it was a subdirectory under config run_dirs that was not created yet)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("creating run directory %q: %w", runDir, err)
	}

	return runTarget(ctx, agent, *selectedTarget, prompt, session, runDir, chatID, conf)
}

// RunAll runs ALL CLI targets on the agent concurrently and returns one RunResult per target.
// Note: Model selection is only applicable to sequential execution (Run) and does not apply to parallel execution (RunAll),
// which concurrently executes all configured CLI targets.
// sessions maps "<cli>/<model>" to the session ID to resume for that target.
// Pass an empty or nil map to start fresh sessions for all targets.
func RunAll(ctx context.Context, agent *agents.Agent, prompt string, sessions map[string]string, runDirOpt optional.Option[string], chatID string, conf *config.Config) []RunResult {
	if len(agent.Config.CLI) == 0 {
		return []RunResult{{Err: fmt.Errorf("no CLI targets configured for agent %s", agent.Config.ID)}}
	}

	runDir, err := resolveRunDir(agent, runDirOpt)
	if err != nil {
		return []RunResult{{Err: err}}
	}

	// Ensure the resolved runDir exists.
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return []RunResult{{Err: fmt.Errorf("creating run directory %q: %w", runDir, err)}}
	}

	results := make([]RunResult, len(agent.Config.CLI))
	var wg sync.WaitGroup

	for i, target := range agent.Config.CLI {
		wg.Add(1)
		go func(idx int, t agents.CLITarget) {
			defer wg.Done()
			cliKey := t.CLI + "/" + t.Model
			sessionOpt := optional.None[string]()
			if sid, ok := sessions[cliKey]; ok && sid != "" {
				sessionOpt = optional.Some(sid)
			}
			out, err := runTarget(ctx, agent, t, prompt, sessionOpt, runDir, chatID, conf)
			results[idx] = RunResult{
				CLIKey: cliKey,
				Output: out,
				Err:    err,
			}
		}(i, target)
	}

	wg.Wait()
	return results
}
