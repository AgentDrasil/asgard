package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

func TestAgentNodeDisallowsPrompt(t *testing.T) {
	yamlSpec := `
name: test-agent-prompt-disallowed
nodes:
  - id: agent_1
    type: agent
    agent_id: planner
    prompt: "This prompt should be disallowed"
`
	_, err := workflowspec.ParseDefinition([]byte(yamlSpec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt is not allowed for agent nodes")
}

func TestConditionalCycleAllowed(t *testing.T) {
	yamlSpec := `
name: test-conditional-cycle
nodes:
  - id: plan_agent
    type: agent
    agent_id: planner
    entry: true
    depends:
      - node: human_review
        when: "nodes.human_review.output == 'Request Changes'"
    join: always

  - id: human_review
    type: human
    depends:
      - node: plan_agent
    prompt: "Approve or Request Changes?"
    options: ["Approve", "Request Changes"]
`
	defn, err := workflowspec.ParseDefinition([]byte(yamlSpec))
	require.NoError(t, err)
	assert.Equal(t, "test-conditional-cycle", defn.Name)
	assert.Len(t, defn.Nodes, 2)
}

func TestUnconditionalCycleRejected(t *testing.T) {
	yamlSpec := `
name: test-unconditional-cycle
nodes:
  - id: node_a
    type: command
    command: "echo A"
    depends:
      - node: node_b

  - id: node_b
    type: command
    command: "echo B"
    depends:
      - node: node_a
`
	_, err := workflowspec.ParseDefinition([]byte(yamlSpec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency detected")
}
