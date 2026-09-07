package workflow

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/pluginsdk"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

const pairingDefnYAML = `
name: pairing-runtime
nodes:
  - id: coder_node
    type: agent
    agent_id: coder
    entry: true
  - id: fixer_node
    type: agent
    agent_id: fixer
  - id: review_node
    type: agent
    agent_id: reviewer
model_pairings:
  - id: code
    actors: [coder_node, fixer_node]
    reviewer: review_node
    pairs:
      - actor: {cli: agy, model: gemini}
        reviewer:
          - {cli: opencode, model: glm}
          - {cli: openrouter, model: claude}
      - actor: {cli: opencode, model: glm-flash}
        reviewer:
          - {cli: agy, model: gemini-pro}
`

func pairingTestContext(t *testing.T) (*pluginsdk.NodeContext, *workflowspec.NodeSpec) {
	t.Helper()
	defn, err := workflowspec.ParseDefinition([]byte(pairingDefnYAML))
	require.NoError(t, err)
	var reviewNode *workflowspec.NodeSpec
	for _, n := range defn.Nodes {
		if n.ID == "review_node" {
			reviewNode = n
		}
	}
	require.NotNil(t, reviewNode)
	return &pluginsdk.NodeContext{Defn: defn, Values: &pluginsdk.RunValues{}}, reviewNode
}

func mustPairingGroup(t *testing.T) *workflowspec.ModelPairingSpec {
	t.Helper()
	defn, err := workflowspec.ParseDefinition([]byte(pairingDefnYAML))
	require.NoError(t, err)
	group := defn.PairingGroupForReviewer("review_node")
	require.NotNil(t, group)
	return group
}

func TestResolvePairingPlan_NotAPairedReviewer(t *testing.T) {
	nctx, _ := pairingTestContext(t)
	nctx.Node = &workflowspec.NodeSpec{ID: "coder_node"}
	plan, err := resolvePairingPlan(nctx, nctx.Node)
	require.NoError(t, err)
	assert.Nil(t, plan)
}

func TestResolvePairingPlan_ExplicitModelBypassesPairing(t *testing.T) {
	nctx, node := pairingTestContext(t)
	node.Model = "manual-model"
	plan, err := resolvePairingPlan(nctx, node)
	require.NoError(t, err)
	assert.Nil(t, plan)
}

func TestResolvePairingPlan_LatestActorWins(t *testing.T) {
	nctx, node := pairingTestContext(t)
	older := time.Now().Add(-time.Minute)
	nctx.Values.Set(pairingTargetKey("coder_node"), recordedPairingTarget{CLI: "opencode", Model: "glm-flash", At: older})
	nctx.Values.Set(pairingTargetKey("fixer_node"), recordedPairingTarget{CLI: "agy", Model: "gemini", At: time.Now()})

	plan, err := resolvePairingPlan(nctx, node)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Equal(t, "code", plan.GroupID)
	assert.Equal(t, "fixer_node", plan.ActorNodeID)
	assert.Equal(t, workflowspec.PairTarget{CLI: "agy", Model: "gemini"}, plan.ActorTarget)
	assert.Equal(t, []agentspec.CLITarget{
		{CLI: "opencode", Model: "glm"},
		{CLI: "openrouter", Model: "claude"},
	}, plan.Candidates)
}

func TestResolvePairingPlan_UpstreamFallbackAfterRestart(t *testing.T) {
	nctx, node := pairingTestContext(t)
	nctx.Upstreams = map[string]*workflowspec.NodeResult{
		"coder_node": {Status: workflowspec.StatusSucceeded, CLI: "opencode", Model: "glm-flash"},
	}
	plan, err := resolvePairingPlan(nctx, node)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Equal(t, workflowspec.PairTarget{CLI: "opencode", Model: "glm-flash"}, plan.ActorTarget)
}

func TestResolvePairingPlan_KeyNotFoundFailsClosed(t *testing.T) {
	nctx, node := pairingTestContext(t)
	nctx.Values.Set(pairingTargetKey("coder_node"), recordedPairingTarget{CLI: "agy", Model: "unlisted", At: time.Now()})
	_, err := resolvePairingPlan(nctx, node)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pairing key not found: agy/unlisted")
}

func TestResolvePairingPlan_NoCompletedActor(t *testing.T) {
	nctx, node := pairingTestContext(t)
	_, err := resolvePairingPlan(nctx, node)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no completed actor execution")
}

func TestRecordActualTarget(t *testing.T) {
	nctx, _ := pairingTestContext(t)
	recordActualTarget(nctx, "coder_node", "agy", "gemini")
	v, ok := nctx.Values.Get(pairingTargetKey("coder_node"))
	require.True(t, ok)
	rec, ok := v.(recordedPairingTarget)
	require.True(t, ok)
	assert.Equal(t, "agy", rec.CLI)
	assert.Equal(t, "gemini", rec.Model)
}

// TestLatestActorTarget_RestartFallback_PrefersLaterActor pins the R1
// semantics: when several actors of a group have successful upstream targets
// (restart fallback, no live RunValues), the actor listed LAST in
// group.Actors wins — matching the documented ambiguity resolution and the
// RunValues tie-break.
func TestLatestActorTarget_RestartFallback_PrefersLaterActor(t *testing.T) {
	nctx, _ := pairingTestContext(t)
	nctx.Upstreams = map[string]*workflowspec.NodeResult{
		"coder_node": {Status: workflowspec.StatusSucceeded, CLI: "opencode", Model: "glm-flash"},
		"fixer_node": {Status: workflowspec.StatusSucceeded, CLI: "agy", Model: "gemini"},
	}
	target, actorNode, ok := latestActorTarget(mustPairingGroup(t), nctx)
	require.True(t, ok)
	assert.Equal(t, "fixer_node", actorNode)
	assert.Equal(t, workflowspec.PairTarget{CLI: "agy", Model: "gemini"}, target)
}

// TestLatestActorTarget_RestartFallback_SingleSuccess: only one actor
// succeeded (the other failed without a usable target) → that one is taken.
func TestLatestActorTarget_RestartFallback_SingleSuccess(t *testing.T) {
	nctx, _ := pairingTestContext(t)
	nctx.Upstreams = map[string]*workflowspec.NodeResult{
		"coder_node": {Status: workflowspec.StatusSucceeded, CLI: "opencode", Model: "glm-flash"},
		"fixer_node": {Status: workflowspec.StatusFailed, CLI: "agy", Model: "gemini"},
	}
	target, actorNode, ok := latestActorTarget(mustPairingGroup(t), nctx)
	require.True(t, ok)
	assert.Equal(t, "coder_node", actorNode)
	assert.Equal(t, workflowspec.PairTarget{CLI: "opencode", Model: "glm-flash"}, target)
}

// TestLatestActorTarget_RestartFallback_LaterFailureFallsBack: a failed or
// target-less upstream never participates — when only the later-listed actor
// failed, the earlier successful one still serves as the baseline.
func TestLatestActorTarget_RestartFallback_LaterFailureFallsBack(t *testing.T) {
	nctx, _ := pairingTestContext(t)
	nctx.Upstreams = map[string]*workflowspec.NodeResult{
		"coder_node": {Status: workflowspec.StatusSucceeded, CLI: "opencode", Model: "glm-flash"},
		"fixer_node": {Status: workflowspec.StatusFailed},
	}
	target, actorNode, ok := latestActorTarget(mustPairingGroup(t), nctx)
	require.True(t, ok)
	assert.Equal(t, "coder_node", actorNode)
	assert.Equal(t, workflowspec.PairTarget{CLI: "opencode", Model: "glm-flash"}, target)

	// No successful upstream at all → no baseline.
	nctx.Upstreams = map[string]*workflowspec.NodeResult{
		"coder_node": {Status: workflowspec.StatusFailed, CLI: "opencode", Model: "glm-flash"},
		"fixer_node": {Status: workflowspec.StatusSkipped, CLI: "agy", Model: "gemini"},
	}
	_, _, ok = latestActorTarget(mustPairingGroup(t), nctx)
	assert.False(t, ok)
}

func TestResolvePairingPlan_RestartFallbackResolvesLaterActor(t *testing.T) {
	nctx, node := pairingTestContext(t)
	nctx.Upstreams = map[string]*workflowspec.NodeResult{
		"coder_node": {Status: workflowspec.StatusSucceeded, CLI: "opencode", Model: "glm-flash"},
		"fixer_node": {Status: workflowspec.StatusSucceeded, CLI: "agy", Model: "gemini"},
	}
	plan, err := resolvePairingPlan(nctx, node)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Equal(t, "fixer_node", plan.ActorNodeID, "restart fallback must baseline on the actor listed last")
	assert.Equal(t, workflowspec.PairTarget{CLI: "agy", Model: "gemini"}, plan.ActorTarget)
	assert.Equal(t, []agentspec.CLITarget{
		{CLI: "opencode", Model: "glm"},
		{CLI: "openrouter", Model: "claude"},
	}, plan.Candidates)
}

// captureEmitter returns a NodeContext whose EventEmitter appends every event
// to the returned slice (guarded by a mutex for safety).
func captureEmitter() (*NodeContext, func() []WorkflowEvent) {
	nctx := &NodeContext{Values: &RunValues{}}
	var mu sync.Mutex
	var events []WorkflowEvent
	nctx.EventEmitter = func(e WorkflowEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	return nctx, func() []WorkflowEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]WorkflowEvent{}, events...)
	}
}

func TestEmitModelSelection_ExplicitModelNotReportedAsFallback(t *testing.T) {
	nctx, events := captureEmitter()
	node := &workflowspec.NodeSpec{ID: "n1", AgentID: "a", Model: "m2"}
	agent := &agentspec.Agent{Config: agentspec.AgentConfig{
		Name: "A",
		CLI:  []agentspec.CLITarget{{CLI: "agy", Model: "m1"}, {CLI: "opencode", Model: "m2"}},
	}}

	emitModelSelection(nctx, node, agent, agentspec.CLITarget{CLI: "opencode", Model: "m2"}, nil, false)
	assert.Empty(t, events(), "an explicit node-level model is a user takeover, never a fallback")
}

func TestEmitModelSelection_UserForcedNotReportedAsFallback(t *testing.T) {
	nctx, events := captureEmitter()
	node := &workflowspec.NodeSpec{ID: "n1", AgentID: "a"}
	agent := &agentspec.Agent{Config: agentspec.AgentConfig{
		Name: "A",
		CLI:  []agentspec.CLITarget{{CLI: "agy", Model: "m1"}, {CLI: "opencode", Model: "m2"}},
	}}

	emitModelSelection(nctx, node, agent, agentspec.CLITarget{CLI: "opencode", Model: "m2"}, nil, true)
	assert.Empty(t, events(), "a model forced via quota suspension is a user takeover, never a fallback")
}

func TestEmitModelSelection_AutoFallbackReported(t *testing.T) {
	nctx, events := captureEmitter()
	node := &workflowspec.NodeSpec{ID: "n1", AgentID: "a"}
	agent := &agentspec.Agent{Config: agentspec.AgentConfig{
		Name: "A",
		CLI:  []agentspec.CLITarget{{CLI: "agy", Model: "m1"}, {CLI: "opencode", Model: "m2"}},
	}}

	emitModelSelection(nctx, node, agent, agentspec.CLITarget{CLI: "opencode", Model: "m2"}, nil, false)
	evts := events()
	require.Len(t, evts, 1)
	assert.Equal(t, EventNodeStatusUpdate, evts[0].Type)
	assert.Equal(t, "n1", evts[0].NodeID)
	assert.Contains(t, evts[0].Message, "Model fallback")
	assert.Contains(t, evts[0].Message, "opencode/m2", "message must carry the actually-used target")
	assert.Contains(t, evts[0].Message, "agy/m1", "message must carry the preferred target")
	assert.Equal(t, map[string]any{"cli": "opencode", "model": "m2"}, evts[0].Metadata)
}

func TestEmitModelSelection_PairingFallbackReported(t *testing.T) {
	nctx, events := captureEmitter()
	node := &workflowspec.NodeSpec{ID: "review_node", AgentID: "reviewer"}
	agent := &agentspec.Agent{Config: agentspec.AgentConfig{
		Name: "Reviewer",
		CLI:  []agentspec.CLITarget{{CLI: "unrelated", Model: "own-model"}},
	}}
	plan := &pairingPlan{
		GroupID:     "code",
		ActorNodeID: "coder_node",
		ActorTarget: workflowspec.PairTarget{CLI: "agy", Model: "gemini"},
		Candidates:  []agentspec.CLITarget{{CLI: "opencode", Model: "glm"}, {CLI: "openrouter", Model: "claude"}},
	}

	emitModelSelection(nctx, node, agent, agentspec.CLITarget{CLI: "openrouter", Model: "claude"}, plan, false)
	evts := events()
	require.Len(t, evts, 1)
	assert.Contains(t, evts[0].Message, "Model pairing fallback")
	assert.Contains(t, evts[0].Message, `"code"`)
	assert.Contains(t, evts[0].Message, "coder_node")
	assert.Contains(t, evts[0].Message, "agy/gemini")
	assert.Contains(t, evts[0].Message, "openrouter/claude")

	// Hitting the paired first choice is not a deviation: no event.
	emitModelSelection(nctx, node, agent, agentspec.CLITarget{CLI: "opencode", Model: "glm"}, plan, false)
	assert.Len(t, events(), 1)

	// Even a user-forced model inside a pairing plan keeps the accurate
	// pairing wording (candidates already replaced the agent cli list).
	emitModelSelection(nctx, node, agent, agentspec.CLITarget{CLI: "openrouter", Model: "claude"}, plan, true)
	assert.Len(t, events(), 2)
}

func TestEmitModelSelection_NilAgentOrEmitterNoPanic(t *testing.T) {
	node := &workflowspec.NodeSpec{ID: "n1", AgentID: "a"}
	target := agentspec.CLITarget{CLI: "opencode", Model: "m2"}

	// Nil emitter is a no-op.
	emitModelSelection(nil, node, nil, target, nil, false)
	// Nil agent with pairing nil and no cli list → nothing to compare, no panic.
	nctx, events := captureEmitter()
	emitModelSelection(nctx, node, nil, target, nil, false)
	assert.Empty(t, events())
}

// TestAgentRunner_PairingCandidatesReplaceAgentList drives the real
// agentRunner against a mock sandbox: the reviewer's agent cli list would
// exhaust quota, but the pairing plan replaces it with the candidate list
// keyed by the actor's recorded actual target, so the run succeeds on the
// paired first choice and records it for downstream reviewers.
func TestAgentRunner_PairingCandidatesReplaceAgentList(t *testing.T) {
	tmpDir := setupQuotaRunnerEnv(t)

	remainingByModel := map[string]float64{
		"own-model": 0.0, // reviewer's own cli head: exhausted
		"gemini":    0.9, // actor target (baseline only, never checked here)
		"glm":       0.5, // paired first choice: usable
	}
	allUsages := func(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
		usages := make([]types.ModelUsage, 0, len(remainingByModel))
		for model, remaining := range remainingByModel {
			usages = append(usages, types.ModelUsage{Model: model, Remaining: remaining})
		}
		return usages, nil
	}
	agentwrapper.SetClients(map[string]types.CLIClient{
		"agy":      &agentwrapper.FakeClient{UsageFunc: allUsages},
		"opencode": &agentwrapper.FakeClient{UsageFunc: allUsages},
	})
	t.Cleanup(func() { agentwrapper.SetClients(nil) })

	runDir := filepath.Join(tmpDir, "rd")
	require.NoError(t, os.MkdirAll(runDir, 0o755))

	defn, err := workflowspec.ParseDefinition([]byte(pairingDefnYAML))
	require.NoError(t, err)
	var reviewNode *workflowspec.NodeSpec
	for _, n := range defn.Nodes {
		if n.ID == "review_node" {
			reviewNode = n
		}
	}
	require.NotNil(t, reviewNode)

	runner := NewAgentRunner(nil, nil)
	runner.(interface{ SetAgents([]*agentspec.Agent) }).SetAgents([]*agentspec.Agent{
		{
			Config: agentspec.AgentConfig{
				ID:      "reviewer",
				Name:    "Reviewer",
				CLI:     []agentspec.CLITarget{{CLI: "agy", Model: "own-model"}},
				RunDirs: []string{runDir},
			},
		},
	})

	var mu sync.Mutex
	var events []WorkflowEvent
	nctx := &NodeContext{
		SessionID: "psess",
		RunID:     "prun",
		RunDir:    runDir,
		TmpDir:    t.TempDir(),
		Input:     "review the code",
		Defn:      defn,
		Node:      reviewNode,
		Values:    &RunValues{},
		EventEmitter: func(e WorkflowEvent) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, e)
		},
	}
	// Pre-seed the actor's actually-used target (live-run path).
	recordActualTarget(nctx, "coder_node", "agy", "gemini")

	res, err := runner.Run(t.Context(), nctx)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, workflowspec.StatusSucceeded, res.Status)
	assert.Equal(t, "opencode", res.CLI, "the paired candidate list must replace the agent's own cli list")
	assert.Equal(t, "glm", res.Model)

	// The reviewer's actual target is recorded for downstream pairing.
	v, ok := nctx.Values.Get(pairingTargetKey("review_node"))
	require.True(t, ok)
	rec, ok := v.(recordedPairingTarget)
	require.True(t, ok)
	assert.Equal(t, "opencode", rec.CLI)
	assert.Equal(t, "glm", rec.Model)

	// Hitting the paired first choice is not a deviation: no fallback event.
	mu.Lock()
	defer mu.Unlock()
	for _, e := range events {
		assert.NotContains(t, e.Message, "fallback")
	}
}

// TestAgentRunner_PairingRestartFallbackCandidates pins the R1 wiring end to
// end: with empty RunValues (post-restart / re-drive), the reviewer resolves
// its candidates from the actor listed LAST among successful upstreams.
func TestAgentRunner_PairingRestartFallbackCandidates(t *testing.T) {
	tmpDir := setupQuotaRunnerEnv(t)

	remainingByModel := map[string]float64{
		"glm":       0.5, // paired first choice for the fixer (agy/gemini) target
		"claude":    0.5, // paired second choice for the fixer target
		"glm-flash": 0.9, // coder upstream target (baseline key only)
		"gemini":    0.9, // fixer upstream target (baseline key only)
	}
	allUsages := func(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
		usages := make([]types.ModelUsage, 0, len(remainingByModel))
		for model, remaining := range remainingByModel {
			usages = append(usages, types.ModelUsage{Model: model, Remaining: remaining})
		}
		return usages, nil
	}
	agentwrapper.SetClients(map[string]types.CLIClient{
		"opencode":   &agentwrapper.FakeClient{UsageFunc: allUsages},
		"openrouter": &agentwrapper.FakeClient{UsageFunc: allUsages},
	})
	t.Cleanup(func() { agentwrapper.SetClients(nil) })

	runDir := filepath.Join(tmpDir, "rd")
	require.NoError(t, os.MkdirAll(runDir, 0o755))

	defn, err := workflowspec.ParseDefinition([]byte(pairingDefnYAML))
	require.NoError(t, err)
	var reviewNode *workflowspec.NodeSpec
	for _, n := range defn.Nodes {
		if n.ID == "review_node" {
			reviewNode = n
		}
	}
	require.NotNil(t, reviewNode)

	runner := NewAgentRunner(nil, nil)
	runner.(interface{ SetAgents([]*agentspec.Agent) }).SetAgents([]*agentspec.Agent{
		{
			Config: agentspec.AgentConfig{
				ID:      "reviewer",
				Name:    "Reviewer",
				CLI:     []agentspec.CLITarget{{CLI: "opencode", Model: "glm"}},
				RunDirs: []string{runDir},
			},
		},
	})

	nctx := &NodeContext{
		SessionID: "psess-rf",
		RunID:     "prun-rf",
		RunDir:    runDir,
		TmpDir:    t.TempDir(),
		Input:     "review the code",
		Defn:      defn,
		Node:      reviewNode,
		Values:    &RunValues{},
		Upstreams: map[string]*workflowspec.NodeResult{
			"coder_node": {Status: workflowspec.StatusSucceeded, CLI: "opencode", Model: "glm-flash"},
			"fixer_node": {Status: workflowspec.StatusSucceeded, CLI: "agy", Model: "gemini"},
		},
	}

	res, err := runner.Run(t.Context(), nctx)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, workflowspec.StatusSucceeded, res.Status)
	// fixer_node (listed last) wins → baseline agy/gemini → candidates
	// [opencode/glm, openrouter/claude]; the paired first choice runs instead
	// of the agent's own glm head.
	assert.Equal(t, "opencode", res.CLI)
	assert.Equal(t, "glm", res.Model)
}
