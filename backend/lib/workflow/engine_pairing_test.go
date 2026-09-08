package workflow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

const pairingGateCoveredYAML = `
name: pairing-gate-covered
nodes:
  - id: coder_node
    type: agent
    agent_id: coder
    entry: true
  - id: review_node
    type: agent
    agent_id: reviewer
    model: glm
model_pairings:
  - id: code
    actors: [coder_node]
    reviewer: review_node
    pairs:
      - actor: {cli: agy, model: gemini}
        reviewer:
          - {cli: opencode, model: glm}
`

const pairingGateGapYAML = `
name: pairing-gate-gap
nodes:
  - id: coder_node
    type: agent
    agent_id: coder
    entry: true
  - id: review_node
    type: agent
    agent_id: reviewer
model_pairings:
  - id: code
    actors: [coder_node]
    reviewer: review_node
    pairs:
      - actor: {cli: agy, model: gemini}
        reviewer:
          - {cli: opencode, model: glm}
`

const pairingGateMissingAgentYAML = `
name: pairing-gate-missing-agent
nodes:
  - id: coder_node
    type: agent
    agent_id: ghost
    entry: true
  - id: review_node
    type: agent
    agent_id: reviewer
model_pairings:
  - id: code
    actors: [coder_node]
    reviewer: review_node
    pairs:
      - actor: {cli: agy, model: gemini}
        reviewer:
          - {cli: opencode, model: glm}
`

func mustParsePairingGateDefn(t *testing.T, yaml string) *workflowspec.WorkflowDefinition {
	t.Helper()
	defn, err := workflowspec.ParseDefinition([]byte(yaml))
	require.NoError(t, err)
	return defn
}

func newPairingGateEngine(t *testing.T, agents []*agentspec.Agent) *Engine {
	t.Helper()
	runner := NewAgentRunner(nil, nil)
	runner.(interface{ SetAgents([]*agentspec.Agent) }).SetAgents(agents)
	reg := NewNodeRunnerRegistry()
	reg.Register(runner)
	return NewEngine(reg)
}

// TestExecute_ModelPairingCoverageGapFailsClosed pins the Execute-level gate:
// an actor agent whose cli list contains a target missing from the pairs keys
// fails the run before any node executes, with an error naming the group,
// actor node, agent, and missing target.
func TestExecute_ModelPairingCoverageGapFailsClosed(t *testing.T) {
	t.Parallel()

	defn := mustParsePairingGateDefn(t, pairingGateGapYAML)
	eng := newPairingGateEngine(t, []*agentspec.Agent{
		{
			Config: agentspec.AgentConfig{
				ID:   "coder",
				Name: "Coder",
				CLI:  []agentspec.CLITarget{{CLI: "agy", Model: "gemini"}, {CLI: "agy", Model: "gemini-pro"}},
			},
		},
		{
			Config: agentspec.AgentConfig{
				ID:   "reviewer",
				Name: "Reviewer",
				CLI:  []agentspec.CLITarget{{CLI: "opencode", Model: "glm"}},
			},
		},
	})

	var mu sync.Mutex
	var events []WorkflowEvent
	run, err := eng.Execute(t.Context(), defn, RunContext{
		SessionID: "gate-gap",
		RunDir:    t.TempDir(),
		Input:     "code something",
		Headless:  true,
		EmitEvent: func(e WorkflowEvent) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, e)
		},
	})

	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), "does not cover")
	assert.Contains(t, err.Error(), `"code"`, "error must name the pairing group")
	assert.Contains(t, err.Error(), "coder_node", "error must name the actor node")
	assert.Contains(t, err.Error(), `"coder"`, "error must name the agent id")
	assert.Contains(t, err.Error(), "agy/gemini-pro", "error must name the missing target")

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, events, "the coverage gate must fail before any node runs or event is emitted")
}

// TestExecute_ModelPairingCoverageCompletePassesGate: with the pairs keys
// fully covering the actor agent's cli list, Execute proceeds past the gate;
// nodes may still fail for environment reasons, but never for pairing
// coverage reasons.
func TestExecute_ModelPairingCoverageCompletePassesGate(t *testing.T) {
	t.Parallel()

	defn := mustParsePairingGateDefn(t, pairingGateCoveredYAML)
	eng := newPairingGateEngine(t, []*agentspec.Agent{
		{
			Config: agentspec.AgentConfig{
				ID:   "coder",
				Name: "Coder",
				CLI:  []agentspec.CLITarget{{CLI: "agy", Model: "gemini"}},
			},
		},
		{
			Config: agentspec.AgentConfig{
				ID:   "reviewer",
				Name: "Reviewer",
				CLI:  []agentspec.CLITarget{{CLI: "opencode", Model: "glm"}},
			},
		},
	})

	run, err := eng.Execute(t.Context(), defn, RunContext{
		SessionID: "gate-covered",
		RunDir:    t.TempDir(),
		Input:     "code something",
		Headless:  true,
	})

	// The gate passed and the DAG ran; node errors (whatever their
	// environment-dependent cause) are asserted below to be free of
	// pairing-related messages only.
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Contains(t, run.Nodes, "coder_node")
	assert.Contains(t, run.Nodes, "review_node")
	for nodeID, res := range run.Nodes {
		if res.Error != nil {
			assert.NotContains(t, res.Error.Error(), "does not cover", "node %s", nodeID)
			assert.NotContains(t, res.Error.Error(), "model_pairings", "node %s", nodeID)
		}
	}
}

// TestExecute_ModelPairingMissingAgentFailsClosed: a pairing actor
// referencing an unregistered agent fails closed with an explicit error.
func TestExecute_ModelPairingMissingAgentFailsClosed(t *testing.T) {
	t.Parallel()

	defn := mustParsePairingGateDefn(t, pairingGateMissingAgentYAML)
	eng := newPairingGateEngine(t, []*agentspec.Agent{
		{
			Config: agentspec.AgentConfig{
				ID:   "reviewer",
				Name: "Reviewer",
				CLI:  []agentspec.CLITarget{{CLI: "opencode", Model: "glm"}},
			},
		},
	})

	run, err := eng.Execute(t.Context(), defn, RunContext{
		SessionID: "gate-missing-agent",
		RunDir:    t.TempDir(),
		Input:     "code something",
		Headless:  true,
	})

	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), `agent "ghost" not found`)
	assert.Contains(t, err.Error(), `"code"`)
	assert.Contains(t, err.Error(), "coder_node")
}

// TestExecute_ModelPairingNoAgentRunnerFailsClosed: a workflow declaring
// model_pairings without a registered agent runner fails closed.
func TestExecute_ModelPairingNoAgentRunnerFailsClosed(t *testing.T) {
	t.Parallel()

	defn := mustParsePairingGateDefn(t, pairingGateCoveredYAML)
	eng := NewEngine(NewNodeRunnerRegistry())

	run, err := eng.Execute(t.Context(), defn, RunContext{
		SessionID: "gate-no-runner",
		RunDir:    t.TempDir(),
		Input:     "code something",
		Headless:  true,
	})

	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), "no agent runner is registered")
}

// stubAgentRunnerNoLookup satisfies NodeRunner for agent nodes but does not
// implement the Lookup capability.
type stubAgentRunnerNoLookup struct{}

func (s *stubAgentRunnerNoLookup) Supports(t workflowspec.NodeType) bool {
	return t == workflowspec.NodeTypeAgent
}

func (s *stubAgentRunnerNoLookup) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded}, nil
}

// TestExecute_ModelPairingRunnerWithoutLookupFailsClosed: a registered agent
// runner without the Lookup capability fails the gate closed instead of
// silently skipping the coverage check.
func TestExecute_ModelPairingRunnerWithoutLookupFailsClosed(t *testing.T) {
	t.Parallel()

	defn := mustParsePairingGateDefn(t, pairingGateCoveredYAML)
	reg := NewNodeRunnerRegistry()
	reg.Register(&stubAgentRunnerNoLookup{})
	eng := NewEngine(reg)

	run, err := eng.Execute(t.Context(), defn, RunContext{
		SessionID: "gate-no-lookup",
		RunDir:    t.TempDir(),
		Input:     "code something",
		Headless:  true,
	})

	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), "does not support agent lookup")
}

// TestExecute_NoPairingsUnaffected: a workflow without model_pairings takes
// the zero-behavior path — the gate is not consulted at all (here: no agent
// runner is registered, yet the run settles normally).
func TestExecute_NoPairingsUnaffected(t *testing.T) {
	t.Parallel()

	defn := mustParsePairingGateDefn(t, `
name: no-pairings
nodes:
  - id: solo
    type: agent
    agent_id: coder
    entry: true
`)
	reg := NewNodeRunnerRegistry()
	reg.Register(&stubAgentRunnerNoLookup{})
	eng := NewEngine(reg)

	run, err := eng.Execute(t.Context(), defn, RunContext{
		SessionID: "gate-unaffected",
		RunDir:    t.TempDir(),
		Input:     "code something",
		Headless:  true,
	})

	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, RunStatusCompleted, run.Status)
}
