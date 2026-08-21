package workflow

import (
	"context"
	"fmt"

	"github.com/AgentDrasil/asgard/backend/lib/llm"
)

// llmRunner executes llm nodes via the injected llm.Client.
type llmRunner struct {
	client llm.Client
}

// NewLLMRunner creates the runner for `llm` nodes.
func NewLLMRunner(client llm.Client) NodeRunner {
	return &llmRunner{client: client}
}

func (r *llmRunner) Supports(t NodeType) bool {
	return t == NodeTypeLLM
}

func (r *llmRunner) Run(ctx context.Context, nctx *NodeContext) (*NodeResult, error) {
	node := nctx.Node
	if r.client == nil {
		return nil, fmt.Errorf("node %s: llm client is not configured", node.ID)
	}

	ctx, cancel := withNodeTimeout(ctx, node)
	defer cancel()

	output, err := r.client.GenerateText(ctx, llm.GenerateOptions{
		Model:        node.Model,
		SystemPrompt: node.SystemPrompt,
		Prompt:       nctx.Interpolate(node.Prompt),
	})
	if err != nil {
		return &NodeResult{Status: StatusFailed, Error: fmt.Errorf("llm generation failed: %w", err)}, nil
	}
	return &NodeResult{Status: StatusSucceeded, Output: output}, nil
}
