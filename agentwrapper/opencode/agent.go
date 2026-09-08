package opencode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// agentsDirName is the opencode global config subdirectory that holds
// user-defined agents. opencode discovers "<name>.md" files there via the
// "{agent,agents}/**/*.md" glob (both spellings are accepted).
const agentsDirName = "agents"

// globalAgentsMD is the global ambient-instructions file opencode reads in
// addition to the active agent. Asgard previously mounted its rendered prompt
// there; the adapter now neutralizes it transiently so the contract-driven
// primary agent is the sole source of system identity.
const globalAgentsMD = "AGENTS.md"

// PrepareAgent materializes the CLI-native opencode agent definition from the
// AW_AGENTS.md contract and returns the agent name to pass to
// `opencode run --agent`, plus a cleanup function that restores the sandbox
// config state (generated agent file and any neutralized ambient file are
// removed / restored). Call cleanup after the run, e.g. via defer.
//
// When no contract is mounted (plain `aw opencode` usage outside the Asgard
// sandbox), PrepareAgent returns ("", no-op, nil) and the caller runs opencode
// with its native defaults.
func PrepareAgent(opts types.PromptOptions) (string, func(), error) {
	noop := func() {}

	contract, ok := common.Load()
	if !ok {
		return "", noop, nil
	}

	agentID := strings.TrimSpace(opts.AgentID)
	if agentID == "" {
		agentID = contract.AgentID
	}
	agentName := common.SanitizeAgentID(agentID)

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", noop, fmt.Errorf("resolving home directory for opencode agent setup: %w", err)
	}

	opencodeDir := filepath.Join(home, ".config", "opencode")
	agentsDir := filepath.Join(opencodeDir, agentsDirName)
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return "", noop, fmt.Errorf("creating opencode agents directory %q: %w", agentsDir, err)
	}

	displayName := strings.TrimSpace(contract.AgentName)
	if displayName == "" {
		displayName = agentID
	}
	if displayName == "" {
		displayName = agentName
	}

	body := common.ComposePrompt(
		(&Client{}).SystemPromptHeader(),
		(&Client{}).SystemPromptPeerHeader(),
		contract.Team,
		contract.Body,
	)
	content := renderAgentFile(displayName, contract, body)

	agentFile := filepath.Join(agentsDir, common.AgentFileName(agentName))
	if err := os.WriteFile(agentFile, []byte(content), 0644); err != nil {
		return "", noop, fmt.Errorf("writing opencode agent file %q: %w", agentFile, err)
	}

	// Transiently neutralize the global ambient AGENTS.md (usually the host
	// user's own file bound rw into the sandbox) so it cannot compete with
	// or duplicate the generated primary agent. Restored by cleanup; a
	// read-only ambient file is chmod'ed writable for the run and its
	// permission bits are restored afterwards.
	ambientPath := filepath.Join(opencodeDir, globalAgentsMD)
	takeover, err := common.BeginFileTakeover(ambientPath)
	if err != nil {
		_ = os.Remove(agentFile)
		return "", noop, err
	}
	if err := os.WriteFile(ambientPath, []byte(common.ManagedMarker+"\n"), 0644); err != nil {
		takeover.Restore()
		_ = os.Remove(agentFile)
		return "", noop, fmt.Errorf("neutralizing ambient file %q: %w", ambientPath, err)
	}

	cleanup := func() {
		_ = os.Remove(agentFile)
		takeover.Restore()
	}
	return agentName, cleanup, nil
}

// renderAgentFile builds the full content of the opencode agent markdown
// file: YAML frontmatter (description, primary mode, permission translation)
// plus the composed system prompt body.
func renderAgentFile(displayName string, contract *common.Contract, body string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("description: " + common.YAMLQuote(displayName) + "\n")
	sb.WriteString("mode: primary\n")
	sb.WriteString("permission:\n")
	appendBasePermissions(&sb, contract)
	appendToolAccessPermissions(&sb, contract)
	sb.WriteString("---\n\n")
	sb.WriteString(strings.TrimSpace(body))
	sb.WriteString("\n")
	return sb.String()
}

// appendBasePermissions denies tools whose responsibilities Asgard routes
// through its own protocol binaries instead — matching the contract headers:
//
//   - native `question` is always denied: user clarification must go through
//     /bin/ask-user (system prompt header).
//   - native `task` is denied for team agents: delegation must go through
//     /bin/call-peer (peer header).
//
// Denies use the pattern-map form ({"*": deny}) rather than a bare action:
// a bare "deny" is bypassed by `opencode run --auto`, while the pattern map
// removes the tool from the session entirely. The only exception is
// `question`, which takes no resource argument and rejects the pattern-map
// schema — it keeps the bare form (non-interactive `run` denies it anyway).
func appendBasePermissions(sb *strings.Builder, contract *common.Contract) {
	sb.WriteString("  question: deny\n")
	if strings.TrimSpace(contract.Team) != "" {
		sb.WriteString("  task:\n")
		sb.WriteString("    " + common.YAMLQuote("*") + ": deny\n")
	}
}

// appendToolAccessPermissions translates the contract's tool access metadata
// into opencode permission rules. Full-access agents need no extra rules
// (opencode run --auto approves everything that is not explicitly denied).
// Doc-only (analysis/documentation) agents lose shell execution entirely
// (mirroring simplest, which drops the bash tool); file editing stays
// available.
func appendToolAccessPermissions(sb *strings.Builder, contract *common.Contract) {
	if strings.TrimSpace(contract.ToolAccess) != types.ToolAccessDocOnly {
		return
	}
	sb.WriteString("  bash:\n")
	sb.WriteString("    " + common.YAMLQuote("*") + ": deny\n")
}
