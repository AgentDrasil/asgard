package workflow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

func TestEngine_IsSessionExecuting(t *testing.T) {
	t.Run("regular workflow execution and finished timing window", func(t *testing.T) {
		slowRunner := &slowNodeRunner{delay: 100 * time.Millisecond}
		registry := NewNodeRunnerRegistry()
		registry.Register(slowRunner)
		engine := NewEngine(registry)

		yamlDefn := `
name: regular-slow-wf
nodes:
  - id: slow_node
    type: command
    command: "echo slow"
`
		defn, err := workflowspec.ParseDefinition([]byte(yamlDefn))
		require.NoError(t, err)

		sessionID := "sess-regular"
		var finishedWasExecuting bool
		var finishedChecked bool
		var mu sync.Mutex

		go func() {
			_, _ = engine.Execute(context.Background(), defn, RunContext{
				SessionID: sessionID,
				RunID:     "run-regular",
				RunDir:    t.TempDir(),
				EmitEvent: func(ev WorkflowEvent) {
					if ev.Type == EventWorkflowFinished {
						mu.Lock()
						finishedChecked = true
						finishedWasExecuting = engine.IsSessionExecuting(sessionID)
						mu.Unlock()
					}
				},
			})
		}()

		// While executing, IsSessionExecuting should be true
		waitFor(t, func() bool {
			return engine.IsSessionExecuting(sessionID)
		}, "session should be actively executing")

		// Wait for execution to finish
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return finishedChecked
		}, "EventWorkflowFinished should be emitted")

		mu.Lock()
		assert.False(t, finishedWasExecuting, "IsSessionExecuting must already be false when EventWorkflowFinished is emitted")
		mu.Unlock()

		assert.False(t, engine.IsSessionExecuting(sessionID))
	})

	t.Run("human node suspension and resume lifecycle", func(t *testing.T) {
		slowRunner := &slowNodeRunner{delay: 100 * time.Millisecond}
		registry := NewNodeRunnerRegistry()
		registry.Register(slowRunner)
		store := newMemStore()
		rec := &suspendRecorder{}
		engine := NewEngine(registry)
		engine.SetRunStore(store)
		engine.SetHumanSuspender(func(req SuspendRequest) error {
			rec.record(req)
			return nil
		})

		yamlDefn := `
name: human-lifecycle-wf
nodes:
  - id: human_ask
    type: human
    prompt: "confirm?"
    options: ["ok"]
  - id: downstream_slow
    type: command
    depends:
      - node: human_ask
    command: "echo slow"
`
		defn, err := workflowspec.ParseDefinition([]byte(yamlDefn))
		require.NoError(t, err)

		sessionID := "sess-human-life"
		go func() {
			_, _ = engine.Execute(context.Background(), defn, RunContext{
				SessionID: sessionID,
				RunID:     "run-human-life",
				RunDir:    t.TempDir(),
			})
		}()

		// Wait for human node to suspend
		waitFor(t, func() bool {
			return len(rec.all()) > 0
		}, "human node suspended")

		// When waiting for human, IsSessionExecuting should be false
		assert.False(t, engine.IsSessionExecuting(sessionID), "IsSessionExecuting should be false when suspended on human node")

		// Resume human node
		msgID := rec.all()[0].MessageID
		outcome, _, err := engine.ResumeByMessageID(context.Background(), msgID, "ok", nil)
		require.NoError(t, err)
		assert.Equal(t, ResumeDeliveredLive, outcome)

		// After resume, during downstream execution, IsSessionExecuting should be true
		waitFor(t, func() bool {
			return engine.IsSessionExecuting(sessionID)
		}, "IsSessionExecuting should be true after resume while downstream runs")

		// Once completed, IsSessionExecuting becomes false
		waitFor(t, func() bool {
			return !engine.IsSessionExecuting(sessionID) && store.get("run-human-life") != nil && store.get("run-human-life").Status == PersistStatusCompleted
		}, "workflow completed and IsSessionExecuting is false")
	})

	t.Run("fork parallel with human and fast command", func(t *testing.T) {
		engine, _, rec := newTestEngine(t)
		yamlDefn := `
name: fork-parallel-wf
nodes:
  - id: human_branch
    type: human
    prompt: "fork confirm"
    options: ["ok"]
  - id: fast_cmd
    type: command
    command: "echo fast"
`
		defn, err := workflowspec.ParseDefinition([]byte(yamlDefn))
		require.NoError(t, err)

		sessionID := "sess-fork"
		go func() {
			_, _ = engine.Execute(context.Background(), defn, RunContext{
				SessionID: sessionID,
				RunID:     "run-fork",
				RunDir:    t.TempDir(),
			})
		}()

		waitFor(t, func() bool {
			return len(rec.all()) > 0
		}, "human branch suspended")

		// Fast command finished, human is waiting: IsSessionExecuting should be false
		assert.False(t, engine.IsSessionExecuting(sessionID))

		msgID := rec.all()[0].MessageID
		_, _, err = engine.ResumeByMessageID(context.Background(), msgID, "ok", nil)
		require.NoError(t, err)
	})
}

func TestEngine_ResumeOutcome(t *testing.T) {
	t.Run("live waiter delivery returns ResumeDeliveredLive", func(t *testing.T) {
		engine, _, rec := newTestEngine(t)
		yamlDefn := `
name: test-live-wf
nodes:
  - id: human_node
    type: human
    prompt: "approve"
    options: ["ok"]
`
		defn, err := workflowspec.ParseDefinition([]byte(yamlDefn))
		require.NoError(t, err)

		go func() {
			_, _ = engine.Execute(context.Background(), defn, RunContext{
				SessionID: "sess-live",
				RunID:     "run-live",
				RunDir:    t.TempDir(),
			})
		}()

		waitFor(t, func() bool {
			return len(rec.all()) > 0
		}, "suspended")

		msgID := rec.all()[0].MessageID
		outcome, res, err := engine.ResumeByMessageID(context.Background(), msgID, "ok", nil)
		require.NoError(t, err)
		assert.Equal(t, ResumeDeliveredLive, outcome)
		assert.Nil(t, res)
	})

	t.Run("re-drive from snapshot returns ResumeReDriven", func(t *testing.T) {
		defn, err := workflowspec.ParseDefinition([]byte(humanLoopYAML))
		require.NoError(t, err)

		engine1, store, rec := newTestEngine(t)
		ctx1, cancel1 := context.WithCancel(context.Background())
		go func() {
			_, _ = engine1.Execute(ctx1, defn, RunContext{
				SessionID: "sess-redrive",
				RunID:     "run-redrive",
				RunDir:    t.TempDir(),
			})
		}()

		waitFor(t, func() bool {
			return len(rec.all()) > 0
		}, "suspended on engine1")

		snap := store.get("run-redrive")
		msgID := rec.all()[0].MessageID

		// Simulate crash / restart
		cancel1()
		waitFor(t, func() bool {
			return store.get("run-redrive").Status == PersistStatusCancelled
		}, "cancelled")
		require.NoError(t, store.MarkWaitingHuman(snap))

		engine2, _, _ := newTestEngine(t)
		engine2.SetRunStore(store)

		outcome, res, err := engine2.ResumeByMessageID(context.Background(), msgID, "Approve", nil)
		require.NoError(t, err)
		assert.Equal(t, ResumeReDriven, outcome)
		require.NotNil(t, res)
		assert.Equal(t, RunStatusCompleted, res.Status)
		assert.Equal(t, PersistStatusCompleted, store.get("run-redrive").Status)
	})

	t.Run("duplicate reply during active execution returns ResumeIgnored", func(t *testing.T) {
		engine, store, rec := newTestEngine(t)
		yamlDefn := `
name: test-ignored-wf
nodes:
  - id: human_step
    type: human
    prompt: "approve"
    options: ["ok"]
`
		defn, err := workflowspec.ParseDefinition([]byte(yamlDefn))
		require.NoError(t, err)

		go func() {
			_, _ = engine.Execute(context.Background(), defn, RunContext{
				SessionID: "sess-ignored-test",
				RunID:     "run-ignored-test",
				RunDir:    t.TempDir(),
			})
		}()

		waitFor(t, func() bool {
			return len(rec.all()) > 0
		}, "human suspended")

		msgID := rec.all()[0].MessageID

		// Temporarily clear in-memory waiter from waitingByMsg to test snapshot-load + executing guard (Guard 2)
		engine.waitMu.Lock()
		waiter := engine.waitingByMsg[msgID]
		delete(engine.waitingByMsg, msgID)
		engine.waitMu.Unlock()

		// Attempt resume while executing is true and snap in WAITING_HUMAN
		outcome, res, err := engine.ResumeByMessageID(context.Background(), msgID, "duplicate", nil)
		require.NoError(t, err)
		assert.Equal(t, ResumeIgnored, outcome)
		assert.Nil(t, res)

		// Restore in-memory waiter and complete run cleanly
		engine.waitMu.Lock()
		engine.waitingByMsg[msgID] = waiter
		engine.waitMu.Unlock()

		outcomeLive, _, errLive := engine.ResumeByMessageID(context.Background(), msgID, "ok", nil)
		require.NoError(t, errLive)
		assert.Equal(t, ResumeDeliveredLive, outcomeLive)

		waitFor(t, func() bool {
			return store.get("run-ignored-test") != nil && store.get("run-ignored-test").Status == PersistStatusCompleted
		}, "workflow completed")
	})
}

func TestEngine_ForkDualHuman_ResumeRouting(t *testing.T) {
	yamlDefn := `
name: fork-dual-human-wf
nodes:
  - id: human_a
    type: human
    prompt: "Human A prompt"
    options: ["ok"]
  - id: human_b
    type: human
    prompt: "Human B prompt"
    options: ["ok"]
  - id: final_join
    type: command
    command: "echo done"
    depends:
      - node: human_a
      - node: human_b
`
	defn, err := workflowspec.ParseDefinition([]byte(yamlDefn))
	require.NoError(t, err)

	store := newMemStore()
	registry := NewNodeRunnerRegistry()
	registry.Register(NewCommandRunner(false))
	rec := &suspendRecorder{}
	engine := NewEngine(registry)
	engine.SetRunStore(store)
	engine.SetHumanSuspender(func(req SuspendRequest) error {
		rec.record(req)
		return nil
	})

	runDir := t.TempDir()
	outCh := make(chan *WorkflowRunResult, 1)
	go func() {
		res, err := engine.Execute(context.Background(), defn, RunContext{
			SessionID: "chat-dual-route",
			RunID:     "run-dual-route",
			RunDir:    runDir,
		})
		require.NoError(t, err)
		outCh <- res
	}()

	// Wait for both human_a and human_b to suspend
	waitFor(t, func() bool {
		snap := store.get("run-dual-route")
		return snap != nil && snap.Status == PersistStatusWaitingHuman && len(snap.SuspendedNodes) == 2
	}, "both human nodes suspended")

	snap := store.get("run-dual-route")
	msgA := snap.SuspendedNodes["human_a"].MessageID
	msgB := snap.SuspendedNodes["human_b"].MessageID
	require.NotEmpty(t, msgA)
	require.NotEmpty(t, msgB)

	// Resume human_a: should deliver to live waiter, human_a completes, but run stays WAITING_HUMAN in store
	outcomeA, resA, err := engine.ResumeByMessageID(context.Background(), msgA, "ok", nil)
	require.NoError(t, err)
	assert.Equal(t, ResumeDeliveredLive, outcomeA)
	assert.Nil(t, resA)

	// Verify store snapshot still has status WAITING_HUMAN with only human_b suspended
	waitFor(t, func() bool {
		s := store.get("run-dual-route")
		return s != nil && s.Status == PersistStatusWaitingHuman && len(s.SuspendedNodes) == 1 &&
			s.SuspendedNodes["human_b"].MessageID == msgB &&
			s.NodeStates["human_a"].Status == string(workflowspec.StatusSucceeded)
	}, "run remains WAITING_HUMAN in store with human_a completed and human_b waiting")

	// Resume human_b
	outcomeB, resB, err := engine.ResumeByMessageID(context.Background(), msgB, "ok", nil)
	require.NoError(t, err)
	assert.Equal(t, ResumeDeliveredLive, outcomeB)
	assert.Nil(t, resB)

	// Entire workflow finishes
	select {
	case res := <-outCh:
		require.NotNil(t, res)
		assert.Equal(t, RunStatusCompleted, res.Status)
		assert.Equal(t, workflowspec.StatusSucceeded, res.Nodes["human_a"].Status)
		assert.Equal(t, workflowspec.StatusSucceeded, res.Nodes["human_b"].Status)
		assert.Equal(t, workflowspec.StatusSucceeded, res.Nodes["final_join"].Status)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for workflow completion")
	}

	assert.Equal(t, PersistStatusCompleted, store.get("run-dual-route").Status)
}
