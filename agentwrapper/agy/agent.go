package agy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
)

// globalInstructionsFile is the agy CLI's native global instructions file.
// The Asgard sandbox bind-mounts ~/.gemini read-write, so the adapter takes
// it over transiently for the duration of the run and restores any
// pre-existing user content afterwards.
const globalInstructionsFile = "GEMINI.md"

// InstallContract writes the AW_AGENTS.md contract (composed with the agy
// protocol headers) into agy's native global instructions file and returns a
// cleanup function that restores the previous state. It returns a no-op
// cleanup when no contract is mounted (plain `aw agy` usage outside the
// Asgard sandbox).
func InstallContract() (func(), error) {
	noop := func() {}

	contract, ok := common.Load()
	if !ok {
		return noop, nil
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return noop, fmt.Errorf("resolving home directory for agy contract setup: %w", err)
	}
	geminiDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(geminiDir, 0755); err != nil {
		return noop, fmt.Errorf("creating gemini directory %q: %w", geminiDir, err)
	}

	target := filepath.Join(geminiDir, globalInstructionsFile)
	var previous []byte
	existed := false
	if data, readErr := os.ReadFile(target); readErr == nil {
		previous = data
		existed = true
	} else if !os.IsNotExist(readErr) {
		return noop, fmt.Errorf("reading %q: %w", target, readErr)
	}

	composed := common.ComposePrompt(
		(&Client{}).SystemPromptHeader(),
		(&Client{}).SystemPromptPeerHeader(),
		contract.Team,
		contract.Body,
	)
	content := common.ManagedMarker + "\n\n" + strings.TrimSpace(composed) + "\n"
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		return noop, fmt.Errorf("writing %q: %w", target, err)
	}

	cleanup := func() {
		if !existed {
			data, err := os.ReadFile(target)
			if err != nil {
				return
			}
			if strings.HasPrefix(string(data), common.ManagedMarker) {
				_ = os.Remove(target)
			}
			return
		}
		_ = os.WriteFile(target, previous, 0644)
	}
	return cleanup, nil
}
