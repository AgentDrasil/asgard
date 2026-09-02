package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// quotaAgentRunner emulates the real agent runner's quota decision loop: while
// its (fake) CLI quota is exhausted it calls nctx.SuspendQuota and applies the
// user reply exactly like runWithQuotaDecisions does.
type quotaAgentRunner struct {
	mu             sync.Mutex
	quotaExhausted bool
	targets        []agentspec.CLITarget
	options        []string
	suspensions    int
	runs           int
}

func (r *quotaAgentRunner) Supports(t workflowspec.NodeType) bool {
	return t == workflowspec.NodeTypeAgent
}

func (r *quotaAgentRunner) Run(ctx context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	r.mu.Lock()
	r.runs++
	r.mu.Unlock()

	if nctx.SuspendQuota == nil {
		return nil, errors.New("no quota suspension gateway wired into NodeContext")
	}

	for {
		r.mu.Lock()
		exhausted := r.quotaExhausted
		r.mu.Unlock()
		if !exhausted {
			break
		}

		r.mu.Lock()
		r.suspensions++
		r.mu.Unlock()

		reply, err := nctx.SuspendQuota("Agent \"Q\" (q-agent) cannot start: no CLI target has enough quota remaining.", r.options)
		if err != nil {
			return nil, err
		}
		switch decision, model := classifyQuotaReply(reply, r.targets); decision {
		case quotaDecisionCancel:
			return nil, errQuotaCancelled
		case quotaDecisionTarget:
			_ = model // forced target would be applied to the next run.Run call
		}
		// continue: re-check quota
	}

	return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded, ExitCode: 0, Output: "agent done"}, nil
}

const quotaWorkflowYAML = `
name: quota-wf
nodes:
  - id: sole_agent
    type: agent
    agent_id: q-agent
    entry: true
`

type quotaTestHarness struct {
	engine  *Engine
	runner  *quotaAgentRunner
	store   *memStore
	rec     *suspendRecorder
	waited  chan string
	runID   string
	result  *WorkflowRunResult
	err     error
	done    chan struct{}
	session string
}

func startQuotaWorkflow(t *testing.T, quotaExhausted bool) *quotaTestHarness {
	t.Helper()
	defn, err := workflowspec.ParseDefinition([]byte(quotaWorkflowYAML))
	require.NoError(t, err)

	runner := &quotaAgentRunner{
		quotaExhausted: quotaExhausted,
		targets:        []agentspec.CLITarget{{CLI: "agy", Model: "q-model"}},
		options:        []string{"Wait for quota recovery, then continue", "Use agy q-model", "Cancel run"},
	}
	registry := NewNodeRunnerRegistry()
	registry.Register(runner)
	store := newMemStore()
	rec := &suspendRecorder{}
	waited := make(chan string, 16)
	engine := NewEngine(registry)
	engine.SetRunStore(store)
	engine.SetHumanSuspender(func(req SuspendRequest) error {
		rec.record(req)
		select {
		case waited <- req.NodeID:
		default:
		}
		return nil
	})

	h := &quotaTestHarness{
		engine:  engine,
		runner:  runner,
		store:   store,
		rec:     rec,
		waited:  waited,
		runID:   "qrun-" + strings.ReplaceAll(t.Name(), "/", "-"),
		session: "qsess-" + t.Name(),
		done:    make(chan struct{}),
	}

	rc := RunContext{
		RunID:     h.runID,
		SessionID: h.session,
		RunDir:    t.TempDir(),
		Input:     "do the thing",
	}
	go func() {
		defer close(h.done)
		h.result, h.err = engine.Execute(context.Background(), defn, rc)
	}()
	return h
}

func (h *quotaTestHarness) awaitSuspension(t *testing.T, wantSeq int) SuspendRequest {
	t.Helper()
	select {
	case nodeID := <-h.waited:
		require.Equal(t, "sole_agent", nodeID)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for quota suspension")
	}
	reqs := h.rec.all()
	require.Len(t, reqs, wantSeq)
	return reqs[wantSeq-1]
}

func (h *quotaTestHarness) settle(t *testing.T) *WorkflowRunResult {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for workflow settlement")
	}
	require.NoError(t, h.err)
	return h.result
}

func TestAgentNode_QuotaExhaustion_SuspendsAndContinuesAfterRecovery(t *testing.T) {
	h := startQuotaWorkflow(t, true)

	// First suspension: deterministic quota message ID, WAITING_HUMAN persisted.
	req1 := h.awaitSuspension(t, 1)
	assert.Equal(t, QuotaMessageID(h.runID, "sole_agent", 1, 1), req1.MessageID)
	assert.Equal(t, "wf-"+h.runID+"-sole_agent-quota", req1.MessageID)
	assert.Contains(t, req1.Prompt, "no CLI target has enough quota remaining")
	assert.Contains(t, req1.Prompt, "Options: Wait for quota recovery, then continue / Use agy q-model / Cancel run")

	snap, err := h.store.GetRun(h.runID)
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.Equal(t, PersistStatusWaitingHuman, snap.Status)
	assert.Equal(t, "sole_agent", snap.SuspendedNodeID)
	assert.Equal(t, req1.MessageID, snap.SuspendedMessageID)

	// User clicks "continue" while quota is still exhausted: the node
	// re-suspends with a fresh ask_user message (seq suffix).
	outcome, _, err := h.engine.ResumeByMessageID(context.Background(), req1.MessageID, "Wait for quota recovery, then continue", nil)
	require.NoError(t, err)
	assert.Equal(t, ResumeDeliveredLive, outcome)

	req2 := h.awaitSuspension(t, 2)
	assert.Equal(t, "wf-"+h.runID+"-sole_agent-quota-2", req2.MessageID)

	// Quota recovers; user clicks "continue" again: the run settles COMPLETED.
	h.runner.mu.Lock()
	h.runner.quotaExhausted = false
	h.runner.mu.Unlock()

	outcome, _, err = h.engine.ResumeByMessageID(context.Background(), req2.MessageID, "Wait for quota recovery, then continue", nil)
	require.NoError(t, err)
	assert.Equal(t, ResumeDeliveredLive, outcome)

	res := h.settle(t)
	assert.Equal(t, RunStatusCompleted, res.Status)
	assert.Equal(t, 1, h.runner.runs, "the node execution must not restart from scratch on continue")

	snap, err = h.store.GetRun(h.runID)
	require.NoError(t, err)
	assert.Equal(t, PersistStatusCompleted, snap.Status)
}

func TestAgentNode_QuotaExhaustion_CancelFailsRun(t *testing.T) {
	h := startQuotaWorkflow(t, true)

	req := h.awaitSuspension(t, 1)

	outcome, _, err := h.engine.ResumeByMessageID(context.Background(), req.MessageID, "Cancel run", nil)
	require.NoError(t, err)
	assert.Equal(t, ResumeDeliveredLive, outcome)

	res := h.settle(t)
	assert.Equal(t, RunStatusFailed, res.Status)
	require.Error(t, res.Error)
	assert.Contains(t, res.Error.Error(), "cancelled by user while waiting for CLI quota")
}

func TestAgentNode_QuotaExhaustion_UserForcesTarget(t *testing.T) {
	h := startQuotaWorkflow(t, true)

	req := h.awaitSuspension(t, 1)

	// The user forces the below-threshold target; the fake runner treats any
	// non-cancel reply naming a target as a force and stops suspending.
	h.runner.mu.Lock()
	h.runner.quotaExhausted = false // next quota check succeeds with the forced target
	h.runner.mu.Unlock()

	outcome, _, err := h.engine.ResumeByMessageID(context.Background(), req.MessageID, "Use agy q-model", nil)
	require.NoError(t, err)
	assert.Equal(t, ResumeDeliveredLive, outcome)

	res := h.settle(t)
	assert.Equal(t, RunStatusCompleted, res.Status)
}

// setupQuotaRunnerEnv prepares a sandboxed HOME with a mock bwrap so the real
// agentRunner (and run.Run) execute without touching the host.
func setupQuotaRunnerEnv(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	if origGopath := os.Getenv("GOPATH"); origGopath != "" {
		t.Setenv("GOPATH", origGopath)
	}
	if origGocache := os.Getenv("GOCACHE"); origGocache != "" {
		t.Setenv("GOCACHE", origGocache)
	}
	t.Setenv("HOME", tmpDir)

	for _, subDir := range []string{".gemini", ".cache", ".config", ".local"} {
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, subDir), 0o755))
	}

	mockBwrapPath := filepath.Join(tmpDir, "bwrap")
	scriptContent := "#!/bin/sh\nfor arg in \"$@\"; do\n  echo \"$arg\"\ndone\necho \"mock bwrap execution succeeded\"\n"
	require.NoError(t, os.WriteFile(mockBwrapPath, []byte(scriptContent), 0o755))

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath)

	return tmpDir
}

// TestAgentRunner_QuotaSuspension_Integration drives the REAL agentRunner:
// run.Run returns *run.NoQuotaError (fake quota client reports exhaustion),
// the runner suspends through the NodeContext hook, the "continue" reply flips
// quota and the re-run executes against the mock bwrap sandbox.
func TestAgentRunner_QuotaSuspension_Integration(t *testing.T) {
	tmpDir := setupQuotaRunnerEnv(t)

	var mu sync.Mutex
	remaining := 0.0
	agentwrapper.SetClients(map[string]types.CLIClient{
		"agy": &agentwrapper.FakeClient{
			UsageFunc: func(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
				mu.Lock()
				defer mu.Unlock()
				return []types.ModelUsage{{Model: "m1", Remaining: remaining}}, nil
			},
		},
	})
	t.Cleanup(func() { agentwrapper.SetClients(nil) })

	runDir := filepath.Join(tmpDir, "rd")
	require.NoError(t, os.MkdirAll(runDir, 0o755))

	runner := NewAgentRunner(nil, nil)
	runner.(interface{ SetAgents([]*agentspec.Agent) }).SetAgents([]*agentspec.Agent{
		{
			Config: agentspec.AgentConfig{
				ID:      "qa",
				Name:    "Quota Agent",
				CLI:     []agentspec.CLITarget{{CLI: "agy", Model: "m1"}},
				RunDirs: []string{runDir},
			},
		},
	})

	suspendCalls := 0
	nctx := &NodeContext{
		SessionID: "qsess",
		RunID:     "qrun",
		RunDir:    runDir,
		TmpDir:    t.TempDir(),
		Input:     "do work",
		Node:      &workflowspec.NodeSpec{ID: "n1", Type: workflowspec.NodeTypeAgent, AgentID: "qa", Entry: true},
		Values:    &RunValues{},
		SuspendQuota: func(prompt string, options []string) (string, error) {
			suspendCalls++
			assert.Contains(t, prompt, "no CLI target has enough quota remaining")
			assert.Contains(t, prompt, "agy m1: 0% remaining")
			assert.Equal(t, "Wait for quota recovery, then continue", options[0])
			assert.Equal(t, "Cancel run", options[len(options)-1])
			mu.Lock()
			remaining = 0.9 // quota recovers while waiting
			mu.Unlock()
			return "Wait for quota recovery, then continue", nil
		},
	}

	res, err := runner.Run(t.Context(), nctx)
	require.NoError(t, err)
	assert.Equal(t, workflowspec.StatusSucceeded, res.Status)
	assert.Equal(t, 1, suspendCalls)
	assert.Contains(t, res.Output, "mock bwrap execution succeeded")
}

// TestAgentRunner_QuotaExhausted_HeadlessFailsFast keeps the legacy behavior:
// headless runs cannot suspend and fail with the informative quota error.
func TestAgentRunner_QuotaExhausted_HeadlessFailsFast(t *testing.T) {
	tmpDir := setupQuotaRunnerEnv(t)

	agentwrapper.SetClients(map[string]types.CLIClient{
		"agy": &agentwrapper.FakeClient{
			UsageFunc: func(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
				return []types.ModelUsage{{Model: "m1", Remaining: 0.0}}, nil
			},
		},
	})
	t.Cleanup(func() { agentwrapper.SetClients(nil) })

	runDir := filepath.Join(tmpDir, "rd")
	require.NoError(t, os.MkdirAll(runDir, 0o755))

	runner := NewAgentRunner(nil, nil)
	runner.(interface{ SetAgents([]*agentspec.Agent) }).SetAgents([]*agentspec.Agent{
		{
			Config: agentspec.AgentConfig{
				ID:      "qa",
				Name:    "Quota Agent",
				CLI:     []agentspec.CLITarget{{CLI: "agy", Model: "m1"}},
				RunDirs: []string{runDir},
			},
		},
	})

	nctx := &NodeContext{
		SessionID: "qsess",
		RunID:     "qrun",
		RunDir:    runDir,
		TmpDir:    t.TempDir(),
		Input:     "",
		Node:      &workflowspec.NodeSpec{ID: "n1", Type: workflowspec.NodeTypeAgent, AgentID: "qa", Entry: true},
		Values:    &RunValues{},
		Headless:  true,
		SuspendQuota: func(prompt string, options []string) (string, error) {
			t.Fatal("headless runs must not suspend for quota decisions")
			return "", nil
		},
	}

	res, err := runner.Run(t.Context(), nctx)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, workflowspec.StatusFailed, res.Status)
	require.Error(t, res.Error)
	assert.Contains(t, res.Error.Error(), "no CLI target with more than 10% quota")
}

func TestQuotaMessageID_Determinism(t *testing.T) {
	assert.Equal(t, "wf-run1-node-quota", QuotaMessageID("run1", "node", 1, 1))
	assert.Equal(t, "wf-run1-node-quota", QuotaMessageID("run1", "node", 1, 1), "stable across calls")
	assert.Equal(t, "wf-run1-node-quota-2", QuotaMessageID("run1", "node", 1, 2))
	assert.Equal(t, "wf-run1-node-quota-4", QuotaMessageID("run1", "node", 4, 1))
	// Distinct from human node message IDs of the same node.
	assert.NotEqual(t, HumanMessageID("run1", "node", 1), QuotaMessageID("run1", "node", 1, 1))
}

// TestRunQuotaSuspension_ConsumesPreSuppliedReplyOnce guards the restart path:
// a re-driven run applies the persisted user reply once; if quota is still
// exhausted afterwards, the next suspension must wait for a fresh decision
// instead of replaying the old reply in an infinite loop.
func TestRunQuotaSuspension_ConsumesPreSuppliedReplyOnce(t *testing.T) {
	engine := NewEngine(NewNodeRunnerRegistry())
	node := &workflowspec.NodeSpec{ID: "n", Type: workflowspec.NodeTypeAgent, AgentID: "a"}
	rc := RunContext{
		RunID:        "r",
		SessionID:    "s",
		HumanReplies: map[string]string{"n": "Wait for quota recovery, then continue"},
	}
	snap := func() snapshotCapture { return snapshotCapture{} }

	reply, err := engine.runQuotaSuspension(context.Background(), rc, node, 1, 1, nil, "", snap, func() {}, func(WorkflowEvent) {}, "prompt", nil)
	require.NoError(t, err)
	assert.Equal(t, "Wait for quota recovery, then continue", reply)

	// Second suspension: no pre-supplied reply remains, so a real waiter must
	// be registered and block until a live reply is delivered.
	engine.SetHumanSuspender(func(req SuspendRequest) error {
		go func() {
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if engine.DeliverResumeByMessageID(req.MessageID, "Use agy q-model") {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			t.Errorf("reply delivery timed out for message %s", req.MessageID)
		}()
		return nil
	})

	reply2, err := engine.runQuotaSuspension(context.Background(), rc, node, 1, 2, nil, "", snap, func() {}, func(WorkflowEvent) {}, "prompt", nil)
	require.NoError(t, err)
	assert.Equal(t, "Use agy q-model", reply2)
}

func TestBuildQuotaPromptAndOptions(t *testing.T) {
	agent := &agentspec.Agent{Config: agentspec.AgentConfig{ID: "commit-agent", Name: "Commit Agent"}}
	nq := &run.NoQuotaError{
		AgentID:      "commit-agent",
		MinThreshold: 0.10,
		Targets: []run.QuotaTargetStatus{
			{CLI: "agy", Model: "gemini-3.7-flash-low", Remaining: 0.02, Enabled: true},
			{CLI: "opencode", Model: "zai-coding-plan/glm-5.3-flash/high", Remaining: 0.0, Enabled: true},
			{CLI: "simplest", Model: "x", Remaining: 0.5, Enabled: false},
		},
	}

	prompt := buildQuotaPrompt(nq, agent)
	assert.Contains(t, prompt, `Agent "Commit Agent" (commit-agent) cannot start`)
	assert.Contains(t, prompt, "agy gemini-3.7-flash-low: 2% remaining")
	assert.Contains(t, prompt, "opencode zai-coding-plan/glm-5.3-flash/high: 0% remaining")
	assert.Contains(t, prompt, "simplest x: provider disabled")

	opts := quotaOptions(nq)
	assert.Equal(t, []string{
		"Wait for quota recovery, then continue",
		"Use agy gemini-3.7-flash-low",
		"Cancel run",
	}, opts)
	for _, opt := range opts {
		assert.NotContains(t, opt, " / ", "option labels must not contain the ' / ' separator")
	}

	// Explicit model variant mentions the selected model.
	nqExplicit := &run.NoQuotaError{
		AgentID:       "commit-agent",
		ExplicitModel: "gemini-3.7-flash-low",
		MinThreshold:  0.10,
		Targets:       nq.Targets,
	}
	assert.Contains(t, buildQuotaPrompt(nqExplicit, agent), "selected model gemini-3.7-flash-low is out of quota")
}

func TestClassifyQuotaReply(t *testing.T) {
	targets := []agentspec.CLITarget{
		{CLI: "agy", Model: "gemini-3.7-flash-low"},
		{CLI: "opencode", Model: "zai-coding-plan/glm-5.3-flash/high"},
	}

	tests := []struct {
		name         string
		reply        string
		wantDecision quotaDecision
		wantModel    string
	}{
		{name: "exact wait label continues", reply: "Wait for quota recovery, then continue", wantDecision: quotaDecisionContinue},
		{name: "exact cancel label cancels", reply: "Cancel run", wantDecision: quotaDecisionCancel},
		{name: "free text cancel cancels", reply: "please cancel this", wantDecision: quotaDecisionCancel},
		{name: "abort cancels", reply: "abort", wantDecision: quotaDecisionCancel},
		{name: "exact target label forces model", reply: "Use agy gemini-3.7-flash-low", wantDecision: quotaDecisionTarget, wantModel: "gemini-3.7-flash-low"},
		{name: "target label with slashes forces model", reply: "Use opencode zai-coding-plan/glm-5.3-flash/high", wantDecision: quotaDecisionTarget, wantModel: "zai-coding-plan/glm-5.3-flash/high"},
		{name: "free text naming cli and model forces model", reply: "just use opencode with zai-coding-plan/glm-5.3-flash/high please", wantDecision: quotaDecisionTarget, wantModel: "zai-coding-plan/glm-5.3-flash/high"},
		{name: "unrecognized free text continues", reply: "whatever, go on", wantDecision: quotaDecisionContinue},
		{name: "empty reply continues", reply: "   ", wantDecision: quotaDecisionContinue},
		{name: "cli name alone does not force", reply: "use agy maybe", wantDecision: quotaDecisionContinue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, model := classifyQuotaReply(tt.reply, targets)
			assert.Equal(t, tt.wantDecision, decision)
			assert.Equal(t, tt.wantModel, model)
		})
	}
}
