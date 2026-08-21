package opencode

import (
	"context"
	_ "embed"
	"strings"

	"github.com/AgentDrasil/asgard/backend/lib/agentwrapper/types"
)

//go:embed system_prompt_header.md
var systemPromptHeader string

//go:embed system_prompt_peer.md
var systemPromptPeerHeader string

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
	return strings.TrimSpace(systemPromptHeader)
}

func (c *Client) SystemPromptPeerHeader() string {
	return strings.TrimSpace(systemPromptPeerHeader)
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

func (c *Client) ExtraArgs() []string {
	return nil
}
