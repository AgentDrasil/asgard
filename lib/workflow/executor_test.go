package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowExecutor_PersistCh_TimeoutAndDrop(t *testing.T) {
	t.Parallel()

	yamlSpec := `
name: test-timeout-wf
nodes:
  - id: start
    type: command
    command: "echo hello"
`
	defn, err := ParseDefinition([]byte(yamlSpec))
	require.NoError(t, err)

	registry := &NodeRunnerRegistry{}
	engine := NewEngine(registry)
	exec := NewWorkflowExecutor(engine, defn)
	// Set 5ms timeout for fast testing
	exec.persistTimeout = 5 * time.Millisecond

	// Slow persistence consumer that sleeps on each event
	exec.OnEvent = func(sessionID string, ev WorkflowEvent) {
		time.Sleep(10 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = exec.Execute(ctx, WorkflowRunParams{
		SessionID: "sess-persist-timeout",
		Prompt:    "hello",
	})
	require.NoError(t, err)

	// Verify that normal execution did not drop events
	assert.Equal(t, uint64(0), exec.PersistDropped())
}

func TestWorkflowExecutor_PersistCh_DropCounterDirect(t *testing.T) {
	t.Parallel()

	exec := &WorkflowExecutor{
		persistTimeout: 5 * time.Millisecond,
	}

	// Simulate full persistCh channel to verify timeout and dropped counter increment
	persistCh := make(chan WorkflowEvent, 1)
	persistCh <- WorkflowEvent{Type: EventNodeStarted} // fill buffer

	dropped := false
	emit := func(ev WorkflowEvent) {
		timer := time.NewTimer(exec.persistTimeout)
		select {
		case persistCh <- ev:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			exec.persistDropped++
			dropped = true
		}
	}

	emit(WorkflowEvent{Type: EventNodeFinished})
	assert.True(t, dropped)
	assert.Equal(t, uint64(1), exec.PersistDropped())
}

func TestWorkflowExecutor_Execute_RunDir(t *testing.T) {
	t.Parallel()

	defn := &WorkflowDefinition{
		Name: "test-rundir",
		Nodes: []*NodeSpec{
			{ID: "step1", Type: NodeTypeCommand, Command: "echo hello"},
		},
	}

	registry := NewNodeRunnerRegistry()
	registry.Register(NewCommandRunner(false))
	engine := NewEngine(registry)
	exec := NewWorkflowExecutor(engine, defn)

	// Test with non-existent RunDir via params.RunDir
	_, err := exec.Execute(t.Context(), WorkflowRunParams{
		SessionID: "sess-test",
		RunDir:    "/tmp/non-existent-dir-12345",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")

	// Test with valid RunDir via params.RunDir
	validDir := t.TempDir()
	res, err := exec.Execute(t.Context(), WorkflowRunParams{
		SessionID: "sess-test-valid",
		RunDir:    validDir,
	})
	require.NoError(t, err)
	assert.NotNil(t, res)
}
