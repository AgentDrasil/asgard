package agentwrapper

import (
	"context"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

// FakeClient implements types.CLIClient and types.SandboxSpec for testing purposes.
type FakeClient struct {
	UsageFunc  func(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error)
	ModelsFunc func(ctx context.Context, opts types.UsageOptions) ([]string, error)
	PromptFunc func(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error)

	SystemPromptHeaderFunc     func() string
	SystemPromptPeerHeaderFunc func() string
	SystemPromptConfigPathFunc func(home string) string
	SkillsMountPathFunc        func(home string) string
	MountDirectoriesFunc       func(home string) []string
	AuthDirectoryFunc          func(home string) string
	ExtraArgsFunc              func() []string
}

// NewFakeClient returns a new instance of FakeClient.
func NewFakeClient() *FakeClient {
	return &FakeClient{}
}

// Usage implements types.CLIClient.Usage.
func (c *FakeClient) Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	if c.UsageFunc != nil {
		return c.UsageFunc(ctx, opts)
	}
	return nil, nil
}

// Models implements types.CLIClient.Models.
func (c *FakeClient) Models(ctx context.Context, opts types.UsageOptions) ([]string, error) {
	if c.ModelsFunc != nil {
		return c.ModelsFunc(ctx, opts)
	}
	return nil, nil
}

// Prompt implements types.CLIClient.Prompt.
func (c *FakeClient) Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	if c.PromptFunc != nil {
		return c.PromptFunc(ctx, prompt, opts)
	}
	return nil, nil
}

func (c *FakeClient) SystemPromptHeader() string {
	if c.SystemPromptHeaderFunc != nil {
		return c.SystemPromptHeaderFunc()
	}
	return ""
}

func (c *FakeClient) SystemPromptPeerHeader() string {
	if c.SystemPromptPeerHeaderFunc != nil {
		return c.SystemPromptPeerHeaderFunc()
	}
	return ""
}

func (c *FakeClient) SystemPromptConfigPath(home string) string {
	if c.SystemPromptConfigPathFunc != nil {
		return c.SystemPromptConfigPathFunc(home)
	}
	return home + "/.gemini/GEMINI.md"
}

func (c *FakeClient) SkillsMountPath(home string) string {
	if c.SkillsMountPathFunc != nil {
		return c.SkillsMountPathFunc(home)
	}
	return ""
}

func (c *FakeClient) MountDirectories(home string) []string {
	if c.MountDirectoriesFunc != nil {
		return c.MountDirectoriesFunc(home)
	}
	return nil
}

func (c *FakeClient) AuthDirectory(home string) string {
	if c.AuthDirectoryFunc != nil {
		return c.AuthDirectoryFunc(home)
	}
	return ""
}

func (c *FakeClient) ExtraArgs() []string {
	if c.ExtraArgsFunc != nil {
		return c.ExtraArgsFunc()
	}
	return nil
}
