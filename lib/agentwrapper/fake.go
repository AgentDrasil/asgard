package agentwrapper

import (
	"context"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

// FakeClient implements types.CLIClient for testing purposes.
type FakeClient struct {
	UsageFunc  func(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error)
	ModelsFunc func(ctx context.Context, opts types.UsageOptions) ([]string, error)
	PromptFunc func(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error)
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
