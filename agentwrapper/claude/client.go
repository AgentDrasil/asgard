package claude

import (
	"context"
	_ "embed"
	"os"
	"strings"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
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

func (c *Client) SkillsMountPath(home string) string {
	return home + "/.claude/skills"
}

func (c *Client) MountDirectories(home string) []string {
	return []string{
		home + "/.claude",
		home + "/.cache",
		home + "/.npm",
	}
}

func (c *Client) AuthDirectory(home string) string {
	return home + "/.claude"
}

// AuthFiles is empty: with CLAUDE_CONFIG_DIR pointing at ~/.claude, claude
// keeps its global config (.claude.json) inside the auth directory.
func (c *Client) AuthFiles(home string) []string {
	return nil
}

// ExtraEnv relocates claude's config into ~/.claude, a directory the sandbox
// can bind and mask safely (claude rewrites .claude.json via rename, which
// detaches single-file bind mounts), and disables the self-updater, which
// cannot persist anything in the ephemeral sandbox.
func (c *Client) ExtraEnv(home string) map[string]string {
	return map[string]string{
		"CLAUDE_CONFIG_DIR":   ConfigDir(home),
		"DISABLE_AUTOUPDATER": "1",
	}
}

func (c *Client) ExtraArgs() []string {
	return nil
}

// ConfigDir is where claude keeps its credentials and global config.
func ConfigDir(home string) string {
	return home + "/.claude"
}

// commandEnv is the environment for host-side claude invocations. It always
// pins CLAUDE_CONFIG_DIR to ~/.claude, the same place the sandbox
// (ExtraEnv) and setup validation use, so quota checks, prompts and login
// state can never diverge.
func commandEnv() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return os.Environ()
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			env = append(env, kv)
		}
	}
	return append(env, "CLAUDE_CONFIG_DIR="+ConfigDir(home))
}
