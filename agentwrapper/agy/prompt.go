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

// SplitModelVariant parses a model string that may contain a variant/effort suffix
// (e.g. "gemini-3.7-flash-low" -> "gemini-3.7-flash", "low" or "gemini-3.7-flash/low" -> "gemini-3.7-flash", "low").
func SplitModelVariant(model string) (string, string) {
	if base, variant := types.SplitModelVariant(model); variant != "" {
		return base, variant
	}
	if idx := strings.LastIndex(model, "-"); idx > 0 {
		suffix := strings.ToLower(model[idx+1:])
		if types.KnownVariants[suffix] {
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

	sessionID, lastContent, inputTokens, maxTokens := parseStream(stdout, opts.ReportCallback)

	// Wait for the subprocess. A non-zero exit after successful output is
	// non-fatal — log and continue.
	if err := cmd.Wait(); err != nil {
		log.Warn().Err(err).Msg("agy/prompt: agy exited with error")
	}

	return &types.PromptResult{
		SessionID:   sessionID,
		InputTokens: inputTokens,
		MaxTokens:   maxTokens,
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
