package agy

import (
	"context"
	_ "embed"
	"strings"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

//go:embed system_prompt_header.md
var systemPromptHeader string

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

func (c *Client) SystemPromptConfigPath(home string) string {
	return home + "/.gemini/GEMINI.md"
}

func (c *Client) SkillsMountPath(home string) string {
	return home + "/.gemini/antigravity-cli/skills"
}

func (c *Client) MountDirectories(home string) []string {
	return []string{home + "/.gemini"}
}

func (c *Client) AuthDirectory(home string) string {
	return home + "/.gemini"
}
