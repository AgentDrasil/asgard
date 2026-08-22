package workflow

import (
	"context"
	"fmt"

	"github.com/AgentDrasil/asgard/backend/lib/llm"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// llmRunner executes llm nodes via the injected llm.Client.
type llmRunner struct {
	client llm.Client
}

// NewLLMRunner creates the runner for `llm` nodes.
func NewLLMRunner(client llm.Client) NodeRunner {
	return &llmRunner{client: client}
}

func (r *llmRunner) Supports(t workflowspec.NodeType) bool {
	return t == workflowspec.NodeTypeLLM
}

func (r *llmRunner) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
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
		return &workflowspec.NodeResult{Status: workflowspec.StatusFailed, Error: fmt.Errorf("llm generation failed: %w", err)}, nil
	}
	return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded, Output: output}, nil
}
