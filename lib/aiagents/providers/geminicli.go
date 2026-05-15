package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/samber/mo"

	"github.com/AgentDrasil/asgard/lib/aiagents"
)

// GeminiCLI provides the execution logic for running gemini-cli based agents.
type GeminiCLI struct {
	Agent *aiagents.Agent
}

// NewGeminiCLI creates a new GeminiCLI provider.
func NewGeminiCLI(agent *aiagents.Agent) *GeminiCLI {
	return &GeminiCLI{
		Agent: agent,
	}
}

// Execute runs the gemini-cli with the provided agent configuration, prompt, and optional session ID.
// It assembles CLI arguments, injects necessary environment variables, and captures combined output.
func (p *GeminiCLI) Execute(ctx context.Context, prompt string, sessionID mo.Option[string]) (*aiagents.ExecutionResult, error) {
	// Build arguments
	var args []string

	// Add default args from agent config
	args = append(args, p.Agent.Config.Args...)

	// TODO: replace yolo with approval-mode
	// Add flags required for orchestrator integration
	// --yolo: Skip confirmation prompts for tool execution
	// --output=json: Ensure output is machine-readable for session/token extraction
	args = append(args, "--yolo", "--output=json")

	// Resume existing session if provided
	if id, ok := sessionID.Get(); ok {
		args = append(args, "--resume", id)
	}

	// Add the prompt as the final positional argument
	args = append(args, prompt)

	// Determine the CLI binary to execute
	cliCmd := p.Agent.Config.CLI
	if cliCmd == "" {
		cliCmd = "gemini"
	}

	cmd := exec.CommandContext(ctx, cliCmd, args...)

	// Inject environment variables
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("GEMINI_CLI_SYSTEM_SETTINGS_PATH=%s", p.Agent.Path))

	// Set working directory to the agent's absolute path
	cmd.Dir = p.Agent.Path

	// Capture combined output for parsing and debugging
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gemini-cli execution failed: %w (output: %s)", err, string(output))
	}

	// Parse JSON output
	var result aiagents.ExecutionResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse gemini-cli output: %w", err)
	}

	// Extract tokens from stats.models
	// Since gemini-cli output structure is a bit nested, we use a temporary struct for unmarshaling the stats part if needed,
	// but actually we can just modify ExecutionResult to match the output or use a custom unmarshaler.
	
	// Let's use a specialized struct for gemini-cli output to handle the nesting.
	// When subagents are used, token usage is split into roles ("main" and "subagent").
	// We only count the "main" role to avoid double-counting subagent usage.
	var cliOutput struct {
		SessionID string `json:"session_id"`
		Response  string `json:"response"`
		Stats     struct {
			Models map[string]struct {
				Roles map[string]struct {
					Tokens aiagents.TokenStats `json:"tokens"`
				} `json:"roles"`
			} `json:"models"`
		} `json:"stats"`
	}

	if err := json.Unmarshal(output, &cliOutput); err != nil {
		return nil, fmt.Errorf("failed to parse gemini-cli stats: %w", err)
	}

	result.SessionID = cliOutput.SessionID
	result.Response = cliOutput.Response
	result.RawOutput = output

	// Aggregate tokens across all models using only the "main" role.
	// The "main" role is always present — it exists whether or not subagents were used.
	for _, m := range cliOutput.Stats.Models {
		main := m.Roles["main"]
		result.Stats.Tokens.Input += main.Tokens.Input
		result.Stats.Tokens.Prompt += main.Tokens.Prompt
		result.Stats.Tokens.Candidates += main.Tokens.Candidates
		result.Stats.Tokens.Total += main.Tokens.Total
		result.Stats.Tokens.Cached += main.Tokens.Cached
		result.Stats.Tokens.Thoughts += main.Tokens.Thoughts
	}

	return &result, nil
}
