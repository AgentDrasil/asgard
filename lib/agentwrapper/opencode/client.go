package opencode

import (
	"context"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

type Client struct{}

func NewClient() *Client {
	return &Client{}
}

func (c *Client) Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	return Usage(ctx, opts)
}

func (c *Client) Models(ctx context.Context, opts types.UsageOptions) ([]string, error) {
	return Models(ctx, opts)
}

func (c *Client) Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	return Prompt(ctx, prompt, opts)
}

func (c *Client) SystemPromptHeader() string {
	return "## Important Instructions\n\n" +
		"- When you need to ask the user a question or request user confirmation/input, execute the CLI tool `/bin/ask-user <question>`.\n" +
		"- When you need to call a subagent or agent teammate, execute the CLI tool `/bin/call-peer <agent-id> <message>`."
}

func (c *Client) SystemPromptConfigPath(home string) string {
	return home + "/.config/opencode/AGENTS.md"
}

func (c *Client) SkillsMountPath(home string) string {
	return home + "/.config/opencode/skills"
}

func (c *Client) MountDirectories(home string) []string {
	return []string{
		home + "/.cache",
		home + "/.config",
		home + "/.local",
		home + "/.npm",
	}
}

func (c *Client) AuthDirectory(home string) string {
	return home + "/.local/share/opencode"
}
