package agy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// agyEfforts is the set of --effort values agy actually accepts. Hyphen or
// slash suffixes outside this set are part of the model name itself (e.g.
// "claude-opus-4-6-thinking" is a distinct agy model, not an effort).
var agyEfforts = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
}

// SplitModelVariant parses a model string that may contain a variant/effort suffix
// (e.g. "gemini-3.7-flash-low" -> "gemini-3.7-flash", "low" or "gemini-3.7-flash/low" -> "gemini-3.7-flash", "low").
// Only a trailing segment that is a valid agy effort is treated as a variant;
// name-like suffixes such as "-thinking" stay part of the model name.
func SplitModelVariant(model string) (string, string) {
	parts := strings.Split(model, "/")
	if len(parts) > 1 {
		last := strings.ToLower(parts[len(parts)-1])
		if agyEfforts[last] {
			return strings.Join(parts[:len(parts)-1], "/"), last
		}
	}
	if idx := strings.LastIndex(model, "-"); idx > 0 {
		suffix := strings.ToLower(model[idx+1:])
		if agyEfforts[suffix] {
			return model[:idx], suffix
		}
	}
	return model, ""
}

// buildPromptArgv constructs the command-line arguments for running agy.
func buildPromptArgv(runDir string, prompt string, opts types.PromptOptions) []string {
	argv := []string{"agy", "--dangerously-skip-permissions", "--output-format", "stream-json", "--add-dir", runDir}
	if opts.AddTmpToDir {
		argv = append(argv, "--add-dir", "/tmp")
	}
	if opts.SessionID != "" {
		argv = append(argv, "--conversation="+opts.SessionID)
	}
	if opts.Model != "" {
		baseModel, effort := SplitModelVariant(opts.Model)
		argv = append(argv, "--model", baseModel)
		if effort != "" {
			argv = append(argv, "--effort", effort)
		}
	}
	argv = append(argv, "--print", prompt)
	return argv
}

// Prompt runs `agy --dangerously-skip-permissions --output-format stream-json
// --add-dir <dir> [-p|--print] <prompt>` and streams NDJSON events until the process exits.
//
// Compared to the old PTY-based approach, this requires no terminal emulation,
// no statusline polling, and no transcript file tailing. The agy process
// manages its own I/O and exits cleanly when done.
//
// Session resumption is supported via --conversation=<sessionID>.
// Model selection is supported via --model <model> and --effort <effort>.
func Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	runDir := opts.Dir
	if runDir == "" {
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting current working directory: %w", err)
		}
	}
	if err := ensureWorkspaceTrusted(runDir); err != nil {
		return nil, fmt.Errorf("ensuring workspace is trusted: %w", err)
	}
	if opts.AddTmpToDir {
		if err := ensureWorkspaceTrusted("/tmp"); err != nil {
			return nil, fmt.Errorf("ensuring /tmp workspace is trusted: %w", err)
		}
	}

	// When the AW_AGENTS.md contract is mounted (Asgard sandbox runs),
	// install it as agy's global instructions for the duration of the run.
	restoreContract, err := InstallContract()
	if err != nil {
		return nil, err
	}
	defer restoreContract()

	argv := buildPromptArgv(runDir, prompt, opts)

	log.Debug().Interface("argv", argv).Msg("agy/prompt: starting")

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = runDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agy: %w", err)
	}

	maxTokens := types.GetModelContextWindow(opts.Model)
	sessionID, lastContent, inputTokens, outMaxTokens, runErr := parseStream(stdout, opts.ReportCallback, maxTokens)

	// Wait for the subprocess. A non-zero exit after successful output is
	// non-fatal — log and continue. A non-zero exit with no output at all
	// (e.g. the CLI died before producing a result event) is fatal:
	// swallowing it would report an empty-but-successful run.
	waitErr := cmd.Wait()
	if waitErr != nil {
		if runErr == nil && lastContent == "" && sessionID == "" {
			return nil, fmt.Errorf("waiting for agy: %w", waitErr)
		}
		log.Warn().Err(waitErr).Msg("agy/prompt: agy exited with error")
	}
	if runErr != nil {
		return nil, runErr
	}

	return &types.PromptResult{
		SessionID:   sessionID,
		InputTokens: inputTokens,
		MaxTokens:   outMaxTokens,
		LastContent: lastContent,
	}, nil
}

// ── workspace trust ──────────────────────────────────────────────────────────

func ensureWorkspaceTrusted(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving absolute path for %q: %w", dir, err)
	}
	absDir = filepath.Clean(absDir)

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}
	settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("reading settings file %s: %w", settingsPath, err)
	}

	var config struct {
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parsing settings JSON: %w", err)
	}

	for _, ws := range config.TrustedWorkspaces {
		if filepath.Clean(ws) == absDir {
			return nil
		}
	}

	// Read settings as map to preserve other keys.
	var settingsMap map[string]any
	if err := json.Unmarshal(data, &settingsMap); err != nil {
		return fmt.Errorf("parsing settings JSON for update: %w", err)
	}

	var trustedWorkspaces []any
	if tw, ok := settingsMap["trustedWorkspaces"]; ok {
		if arr, ok := tw.([]any); ok {
			trustedWorkspaces = arr
		}
	}

	trustedWorkspaces = append(trustedWorkspaces, absDir)
	settingsMap["trustedWorkspaces"] = trustedWorkspaces

	newData, err := json.MarshalIndent(settingsMap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling updated settings: %w", err)
	}

	log.Info().Str("path", absDir).Msg("Adding directory to trusted workspaces in settings.json")
	if err := os.WriteFile(settingsPath, newData, 0644); err != nil {
		return fmt.Errorf("writing updated settings file: %w", err)
	}

	return nil
}
