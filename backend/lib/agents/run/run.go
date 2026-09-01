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
)

func IsAllowedDir(path string, allowedDirs []string) bool {
	path = filepath.Clean(path)
	home, _ := os.UserHomeDir()
	for _, dir := range allowedDirs {
		dir = filepath.Clean(dir)
		if path == dir {
			return true
		}
		// Check if it is a subdirectory
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
		// If allowedDirs contains "/tmp" or "tmp", also allow subdirectories under $HOME/tmp (sandbox session dirs)
		if (dir == "/tmp" || dir == "tmp") && home != "" {
			userTmp := filepath.Join(home, "tmp")
			if path == userTmp || strings.HasPrefix(path, userTmp+string(filepath.Separator)) {
				return true
			}
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
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting user home directory: %w", err)
	}
	tmpDir := filepath.Join(home, "tmp")
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
	NodeID   string
	RunToken string
	Headless bool
}

// runTarget executes a single CLI target in its own bubblewrap sandbox.
func runTarget(ctx context.Context, agent *agentspec.Agent, target agentspec.CLITarget, prompt string, session optional.Option[string], runDir string, chatID string, statusScope StatusScope, conf *config.Config) ([]byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting user home directory: %w", err)
	}
	sockDir := filepath.Join(home, "tmp", "fakebash-sock-"+uuid.NewV7().String())
	if err := os.MkdirAll(sockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating sock directory %q: %w", sockDir, err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()

	var langRules string
	var configPath string
	if conf != nil {
		langRules = conf.LanguageRules()
		configPath = conf.GetConfigPath()
	}

	agentSandboxCmd, err := bwrap.CommandForAgent(&agent.Config, agent.Path, target, prompt, session, runDir, sockDir, chatID, langRules, configPath)
	if err != nil {
		return nil, fmt.Errorf("creating command for agent: %w", err)
	}

	// Start the command execution sandbox
	cmdSandboxCmd, err := bwrap.CommandForCommandExec(runDir, sockDir, chatID, configPath)
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

// Run checks the remaining quota for each CLI target configured on the agent.
// It runs the bubblewrap command for the selected target or the first target that has more than 10% quota remaining.
// If a specific model is selected (modelOpt is Some), it checks if that model exists in agent.Config.CLI.
// If selected model has <= 0 quota, it returns an error immediately with NO fallback.
func Run(ctx context.Context, agent *agentspec.Agent, prompt string, session optional.Option[string], runDirOpt optional.Option[string], modelOpt optional.Option[string], chatID string, statusScope StatusScope, conf *config.Config) ([]byte, error) {
	if len(agent.Config.CLI) == 0 {
		return nil, fmt.Errorf("no CLI targets configured for agent %s", agent.Config.ID)
	}

	var selectedTarget *agentspec.CLITarget
	if modelOpt.IsSome() && modelOpt.Unwrap() != "" {
		reqModel := modelOpt.Unwrap()
		for _, target := range agent.Config.CLI {
			if target.Model == reqModel {
				selectedTarget = &target
				break
			}
		}
		if selectedTarget == nil {
			return nil, fmt.Errorf("selected model %q is not in configured model list for agent %s", reqModel, agent.Config.ID)
		}
		if !conf.IsProviderEnabled(selectedTarget.CLI) {
			return nil, fmt.Errorf("provider %q for model %q is disabled in configuration", selectedTarget.CLI, reqModel)
		}
		quota := agentwrapper.CheckQuota(selectedTarget.CLI, selectedTarget.Model)
		if quota <= 0 {
			return nil, fmt.Errorf("model %q has no quota remaining (quota: %.2f)", selectedTarget.Model, quota)
		}
	} else {
		hasEnabledTarget := false
		for _, target := range agent.Config.CLI {
			if !conf.IsProviderEnabled(target.CLI) {
				continue
			}
			hasEnabledTarget = true
			quota := agentwrapper.CheckQuota(target.CLI, target.Model)
			if quota > MinAutoQuotaThreshold {
				selectedTarget = &target
				break
			}
		}

		if selectedTarget == nil {
			if !hasEnabledTarget {
				return nil, fmt.Errorf("no enabled CLI targets available for agent %s", agent.Config.ID)
			}
			return nil, fmt.Errorf("no CLI target with more than 10%% quota remaining is available for agent %s", agent.Config.ID)
		}
	}

	runDir, err := resolveRunDir(agent, runDirOpt)
	if err != nil {
		return nil, err
	}

	// Ensure the resolved runDir exists (e.g. if it was a subdirectory under config run_dirs that was not created yet)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("creating run directory %q: %w", runDir, err)
	}

	return runTarget(ctx, agent, *selectedTarget, prompt, session, runDir, chatID, statusScope, conf)
}
