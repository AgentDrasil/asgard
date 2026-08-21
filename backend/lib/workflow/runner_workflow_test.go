package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestEngineWithWorkflowRunner(resolve ResolveDefnFunc) (*Engine, *SubWorkflowRunner) {
	registry := NewNodeRunnerRegistry()
	cmdRunner := NewCommandRunner(false)
	registry.Register(cmdRunner)

	wfRunner := NewSubWorkflowRunner(resolve)
	registry.Register(wfRunner)

	engine := NewEngine(registry)
	wfRunner.SetEngine(engine)

	return engine, wfRunner
}

func TestSubWorkflowRunner_SingleRun_Success(t *testing.T) {
	t.Parallel()

	childYAML := `
name: child-wf
nodes:
  - id: step1
    type: command
    command: "echo 'hello from child'"`

	childDefn, err := ParseDefinition([]byte(childYAML))
	require.NoError(t, err)

	engine, _ := setupTestEngineWithWorkflowRunner(func(name string) (*WorkflowDefinition, error) {
		if name == "child-wf" {
			return childDefn, nil
		}
		return nil, fmt.Errorf("unknown workflow: %s", name)
	})

	parentYAML := `
name: parent-wf
nodes:
  - id: sub
    type: workflow
    workflow: child-wf`

	parentDefn, err := ParseDefinition([]byte(parentYAML))
	require.NoError(t, err)

	tmpDir := t.TempDir()
	runDir := t.TempDir()

	res, err := engine.Execute(t.Context(), parentDefn, RunContext{
		RunDir: runDir,
		TmpDir: tmpDir,
	})
	require.NoError(t, err)
	require.Equal(t, RunStatusCompleted, res.Status)

	subRes := res.Nodes["sub"]
	require.NotNil(t, subRes)
	assert.Equal(t, StatusSucceeded, subRes.Status)
	assert.Contains(t, subRes.Output, "hello from child")
}

func TestSubWorkflowRunner_Fanout_EmptyItems(t *testing.T) {
	t.Parallel()

	childYAML := `
name: child-wf
nodes:
  - id: step1
    type: command
    command: "echo 'process: ${input}'"`

	childDefn, err := ParseDefinition([]byte(childYAML))
	require.NoError(t, err)

	engine, _ := setupTestEngineWithWorkflowRunner(func(name string) (*WorkflowDefinition, error) {
		if name == "child-wf" {
			return childDefn, nil
		}
		return nil, fmt.Errorf("unknown workflow: %s", name)
	})

	t.Run("non-existent items file creates 0 byte output file and succeeds", func(t *testing.T) {
		tmpDir := t.TempDir()
		runDir := t.TempDir()
		outputFilePath := filepath.Join(tmpDir, "out.jsonl")

		parentYAML := fmt.Sprintf(`
name: parent-wf
nodes:
  - id: fanout-node
    type: workflow
    workflow: child-wf
    fanout:
      items_file: %s/missing.txt
      output_file: %s`, tmpDir, outputFilePath)

		parentDefn, err := ParseDefinition([]byte(parentYAML))
		require.NoError(t, err)

		var events []WorkflowEvent
		res, err := engine.Execute(t.Context(), parentDefn, RunContext{
			RunDir: runDir,
			TmpDir: tmpDir,
			EmitEvent: func(ev WorkflowEvent) {
				events = append(events, ev)
			},
		})
		require.NoError(t, err)
		assert.Equal(t, RunStatusCompleted, res.Status)

		subRes := res.Nodes["fanout-node"]
		require.NotNil(t, subRes)
		assert.Equal(t, StatusSucceeded, subRes.Status)
		assert.Equal(t, "", subRes.Output)

		// Check output file created and has 0 bytes
		info, err := os.Stat(outputFilePath)
		require.NoError(t, err)
		assert.Equal(t, int64(0), info.Size())

		// Verify event metadata
		var found bool
		for _, ev := range events {
			if ev.NodeID == "fanout-node" && ev.Metadata != nil {
				if ic, ok := ev.Metadata["item_count"]; ok && ic == 0 {
					assert.Equal(t, true, ev.Metadata["items_file_missing"])
					found = true
				}
			}
		}
		assert.True(t, found, "expected metadata event with item_count: 0 and items_file_missing: true")
	})

	t.Run("empty items file creates 0 byte output file", func(t *testing.T) {
		tmpDir := t.TempDir()
		runDir := t.TempDir()
		emptyItemsFile := filepath.Join(tmpDir, "empty.txt")
		require.NoError(t, os.WriteFile(emptyItemsFile, []byte("   \n\n  \t\n"), 0644))
		outputFilePath := filepath.Join(tmpDir, "out_empty.jsonl")

		parentYAML := fmt.Sprintf(`
name: parent-wf
nodes:
  - id: fanout-node
    type: workflow
    workflow: child-wf
    fanout:
      items_file: %s
      output_file: %s`, emptyItemsFile, outputFilePath)

		parentDefn, err := ParseDefinition([]byte(parentYAML))
		require.NoError(t, err)

		res, err := engine.Execute(t.Context(), parentDefn, RunContext{
			RunDir: runDir,
			TmpDir: tmpDir,
		})
		require.NoError(t, err)
		assert.Equal(t, RunStatusCompleted, res.Status)

		info, err := os.Stat(outputFilePath)
		require.NoError(t, err)
		assert.Equal(t, int64(0), info.Size())
	})
}

func TestSubWorkflowRunner_Fanout_ConcurrencyAndIsolation(t *testing.T) {
	t.Parallel()

	var runningCount int32
	var maxObservedRunning int32
	var capturedValues []*RunValues
	var valuesMu sync.Mutex

	childDefn := &WorkflowDefinition{
		Name: "child-wf",
		Nodes: []*NodeSpec{
			{
				ID:       "cnode",
				Type:     NodeTypeFunction,
				Function: "cfunc",
			},
		},
	}

	fnReg := NewFunctionRegistry()
	fnReg.Register("cfunc", func(ctx context.Context, nctx *NodeContext) (string, error) {
		cur := atomic.AddInt32(&runningCount, 1)
		for {
			old := atomic.LoadInt32(&maxObservedRunning)
			if cur <= old || atomic.CompareAndSwapInt32(&maxObservedRunning, old, cur) {
				break
			}
		}

		// Verify session isolation: write unique marker to RunValues
		nctx.Values.Set("marker", nctx.Input)
		valuesMu.Lock()
		capturedValues = append(capturedValues, nctx.Values)
		valuesMu.Unlock()

		time.Sleep(50 * time.Millisecond)

		// Assert this sub-run's RunValues only has its own marker
		val, ok := nctx.Values.Get("marker")
		if !ok || val != nctx.Input {
			return "", fmt.Errorf("session isolation violation: expected %s, got %v", nctx.Input, val)
		}

		atomic.AddInt32(&runningCount, -1)
		return nctx.Input, nil
	})

	registry := NewNodeRunnerRegistry()
	wfRunner := NewSubWorkflowRunner(func(name string) (*WorkflowDefinition, error) {
		return childDefn, nil
	})
	registry.Register(wfRunner)
	registry.Register(NewFunctionRunner(fnReg))

	engine := NewEngine(registry)
	wfRunner.SetEngine(engine)

	tmpDir := t.TempDir()
	itemsFile := filepath.Join(tmpDir, "items.txt")
	var items []string
	for i := 1; i <= 6; i++ {
		items = append(items, fmt.Sprintf("item-%d", i))
	}
	require.NoError(t, os.WriteFile(itemsFile, []byte(strings.Join(items, "\n")), 0644))

	parentDefn := &WorkflowDefinition{
		Name: "parent-wf",
		Nodes: []*NodeSpec{
			{
				ID:       "fanout",
				Type:     NodeTypeWorkflow,
				Workflow: "child-wf",
				Fanout: &FanoutSpec{
					ItemsFile:   itemsFile,
					MaxParallel: func(i int) *int { return &i }(2),
				},
			},
		},
	}

	res, err := engine.Execute(t.Context(), parentDefn, RunContext{
		RunDir: tmpDir,
		TmpDir: tmpDir,
	})
	require.NoError(t, err)
	assert.Equal(t, RunStatusCompleted, res.Status)
	assert.Equal(t, int32(2), atomic.LoadInt32(&maxObservedRunning), "concurrency should be capped at max_parallel=2")

	// Ensure all captured RunValues instances are distinct pointers (isolated sessions)
	valuesMu.Lock()
	assert.Len(t, capturedValues, 6)
	for i := 0; i < len(capturedValues); i++ {
		for j := i + 1; j < len(capturedValues); j++ {
			assert.NotSame(t, capturedValues[i], capturedValues[j], "each sub-workflow invocation must have an isolated RunValues instance")
		}
	}
	valuesMu.Unlock()
}

func TestSubWorkflowRunner_Fanout_TmpDirInheritance(t *testing.T) {
	t.Parallel()

	childYAML := `
name: child-wf
nodes:
  - id: check-tmp
    type: command
    command: "echo tmp=${tmp_dir}"`

	childDefn, err := ParseDefinition([]byte(childYAML))
	require.NoError(t, err)

	engine, _ := setupTestEngineWithWorkflowRunner(func(name string) (*WorkflowDefinition, error) {
		return childDefn, nil
	})

	tmpDir := t.TempDir()
	itemsFile := filepath.Join(tmpDir, "items.txt")
	require.NoError(t, os.WriteFile(itemsFile, []byte("item1"), 0644))

	parentDefn := &WorkflowDefinition{
		Name: "parent-wf",
		Nodes: []*NodeSpec{
			{
				ID:       "fanout",
				Type:     NodeTypeWorkflow,
				Workflow: "child-wf",
				Fanout: &FanoutSpec{
					ItemsFile: itemsFile,
				},
			},
		},
	}

	res, err := engine.Execute(t.Context(), parentDefn, RunContext{
		RunDir: tmpDir,
		TmpDir: tmpDir,
	})
	require.NoError(t, err)
	assert.Equal(t, RunStatusCompleted, res.Status)

	subRes := res.Nodes["fanout"]
	assert.Contains(t, subRes.Output, fmt.Sprintf("tmp=%s", tmpDir))
}

func TestSubWorkflowRunner_Fanout_MountDirsInheritance(t *testing.T) {
	t.Parallel()

	childDefn := &WorkflowDefinition{
		Name: "child-wf",
		Nodes: []*NodeSpec{
			{
				ID:       "check-mounts",
				Type:     NodeTypeFunction,
				Function: "check-mounts-fn",
			},
		},
	}

	var capturedRunDirs []string
	var capturedMountDirs MountDirsConfig

	fnReg := NewFunctionRegistry()
	fnReg.Register("check-mounts-fn", func(ctx context.Context, nctx *NodeContext) (string, error) {
		capturedRunDirs = nctx.WorkflowRunDirs
		capturedMountDirs = nctx.WorkflowMountDirs
		return "ok", nil
	})

	registry := NewNodeRunnerRegistry()
	wfRunner := NewSubWorkflowRunner(func(name string) (*WorkflowDefinition, error) {
		return childDefn, nil
	})
	registry.Register(wfRunner)
	registry.Register(NewFunctionRunner(fnReg))

	engine := NewEngine(registry)
	wfRunner.SetEngine(engine)

	tmpDir := t.TempDir()
	itemsFile := filepath.Join(tmpDir, "items.txt")
	require.NoError(t, os.WriteFile(itemsFile, []byte("item1"), 0644))

	parentDefn := &WorkflowDefinition{
		Name: "parent-wf",
		Nodes: []*NodeSpec{
			{
				ID:       "fanout",
				Type:     NodeTypeWorkflow,
				Workflow: "child-wf",
				Fanout: &FanoutSpec{
					ItemsFile: itemsFile,
				},
			},
		},
	}

	rc := RunContext{
		RunDir:          tmpDir,
		TmpDir:          tmpDir,
		WorkflowRunDirs: []string{"/custom/rundir"},
		WorkflowMountDirs: MountDirsConfig{
			ReadOnly:  []string{"/custom/ro"},
			ReadWrite: []string{"/custom/rw"},
		},
	}

	res, err := engine.Execute(t.Context(), parentDefn, rc)
	require.NoError(t, err)
	assert.Equal(t, RunStatusCompleted, res.Status)

	assert.Equal(t, []string{"/custom/rundir"}, capturedRunDirs)
	assert.Equal(t, []string{"/custom/ro"}, capturedMountDirs.ReadOnly)
	assert.Equal(t, []string{"/custom/rw"}, capturedMountDirs.ReadWrite)
}

func TestSubWorkflowRunner_Fanout_PartialFailure(t *testing.T) {
	t.Parallel()

	childYAML := `
name: child-wf
nodes:
  - id: check-fail
    type: command
    command: "if [ \"${input}\" = \"fail\" ]; then exit 1; else echo \"ok: ${input}\"; fi"`

	childDefn, err := ParseDefinition([]byte(childYAML))
	require.NoError(t, err)

	engine, _ := setupTestEngineWithWorkflowRunner(func(name string) (*WorkflowDefinition, error) {
		return childDefn, nil
	})

	tmpDir := t.TempDir()
	itemsFile := filepath.Join(tmpDir, "items.txt")
	require.NoError(t, os.WriteFile(itemsFile, []byte("item1\nfail\nitem2"), 0644))

	parentDefn := &WorkflowDefinition{
		Name: "parent-wf",
		Nodes: []*NodeSpec{
			{
				ID:       "fanout",
				Type:     NodeTypeWorkflow,
				Workflow: "child-wf",
				Fanout: &FanoutSpec{
					ItemsFile: itemsFile,
				},
			},
		},
	}

	res, err := engine.Execute(t.Context(), parentDefn, RunContext{
		RunDir: tmpDir,
		TmpDir: tmpDir,
	})
	require.NoError(t, err)
	assert.Equal(t, RunStatusFailed, res.Status)

	subRes := res.Nodes["fanout"]
	require.NotNil(t, subRes)
	assert.Equal(t, StatusFailed, subRes.Status)
	assert.Contains(t, subRes.Output, "ok: item1")
	assert.Contains(t, subRes.Output, "ok: item2")
	assert.Contains(t, subRes.Output, `"status":"FAILED"`)
}

func TestSubWorkflowRunner_Fanout_OrderedOutput(t *testing.T) {
	t.Parallel()

	childDefn := &WorkflowDefinition{
		Name: "child-wf",
		Nodes: []*NodeSpec{
			{
				ID:       "sleeper",
				Type:     NodeTypeFunction,
				Function: "sleep-fn",
			},
		},
	}

	fnReg := NewFunctionRegistry()
	fnReg.Register("sleep-fn", func(ctx context.Context, nctx *NodeContext) (string, error) {
		// Invert sleep times: item1 sleeps longest, item3 sleeps shortest
		switch nctx.Input {
		case "item1":
			time.Sleep(60 * time.Millisecond)
		case "item2":
			time.Sleep(30 * time.Millisecond)
		default:
			time.Sleep(5 * time.Millisecond)
		}
		return "done-" + nctx.Input, nil
	})

	registry := NewNodeRunnerRegistry()
	wfRunner := NewSubWorkflowRunner(func(name string) (*WorkflowDefinition, error) {
		return childDefn, nil
	})
	registry.Register(wfRunner)
	registry.Register(NewFunctionRunner(fnReg))

	engine := NewEngine(registry)
	wfRunner.SetEngine(engine)

	tmpDir := t.TempDir()
	itemsFile := filepath.Join(tmpDir, "items.txt")
	outputFile := filepath.Join(tmpDir, "output.jsonl")
	require.NoError(t, os.WriteFile(itemsFile, []byte("item1\nitem2\nitem3"), 0644))

	parentDefn := &WorkflowDefinition{
		Name: "parent-wf",
		Nodes: []*NodeSpec{
			{
				ID:       "fanout",
				Type:     NodeTypeWorkflow,
				Workflow: "child-wf",
				Fanout: &FanoutSpec{
					ItemsFile:  itemsFile,
					OutputFile: outputFile,
				},
			},
		},
	}

	res, err := engine.Execute(t.Context(), parentDefn, RunContext{
		RunDir: tmpDir,
		TmpDir: tmpDir,
	})
	require.NoError(t, err)
	assert.Equal(t, RunStatusCompleted, res.Status)

	subRes := res.Nodes["fanout"]
	require.NotNil(t, subRes)
	assert.Equal(t, StatusSucceeded, subRes.Status)

	lines := strings.Split(strings.TrimSpace(subRes.Output), "\n")
	require.Len(t, lines, 3)

	type entry struct {
		ItemIndex int    `json:"item_index"`
		Item      string `json:"item"`
		Status    string `json:"status"`
		Output    string `json:"output"`
	}

	for i, l := range lines {
		var e entry
		require.NoError(t, json.Unmarshal([]byte(l), &e))
		assert.Equal(t, i+1, e.ItemIndex)
		assert.Equal(t, fmt.Sprintf("item%d", i+1), e.Item)
		assert.Equal(t, fmt.Sprintf("done-item%d", i+1), e.Output)
		assert.Equal(t, "SUCCEEDED", e.Status)
	}

	fileContent, err := os.ReadFile(outputFile)
	require.NoError(t, err)
	assert.Equal(t, subRes.Output, string(fileContent))
}

func TestSubWorkflowRunner_RecursionDepthAndCycleDetection(t *testing.T) {
	t.Parallel()

	defns := map[string]*WorkflowDefinition{
		"A": {
			Name: "A",
			Nodes: []*NodeSpec{
				{ID: "n", Type: NodeTypeWorkflow, Workflow: "A"},
			},
		},
		"B": {
			Name: "B",
			Nodes: []*NodeSpec{
				{ID: "n", Type: NodeTypeWorkflow, Workflow: "C"},
			},
		},
		"C": {
			Name: "C",
			Nodes: []*NodeSpec{
				{ID: "n", Type: NodeTypeWorkflow, Workflow: "B"},
			},
		},
		"D1": {
			Name: "D1",
			Nodes: []*NodeSpec{
				{ID: "n", Type: NodeTypeWorkflow, Workflow: "D2"},
			},
		},
		"D2": {
			Name: "D2",
			Nodes: []*NodeSpec{
				{ID: "n", Type: NodeTypeWorkflow, Workflow: "D3"},
			},
		},
		"D3": {
			Name: "D3",
			Nodes: []*NodeSpec{
				{ID: "n", Type: NodeTypeWorkflow, Workflow: "D4"},
			},
		},
		"D4": {
			Name: "D4",
			Nodes: []*NodeSpec{
				{ID: "n", Type: NodeTypeWorkflow, Workflow: "D5"},
			},
		},
		"D5": {
			Name: "D5",
			Nodes: []*NodeSpec{
				{ID: "n", Type: NodeTypeCommand, Command: "echo done"},
			},
		},
	}

	engine, _ := setupTestEngineWithWorkflowRunner(func(name string) (*WorkflowDefinition, error) {
		if d, ok := defns[name]; ok {
			return d, nil
		}
		return nil, fmt.Errorf("unknown wf: %s", name)
	})

	t.Run("A->A self cycle detection", func(t *testing.T) {
		ctx := context.WithValue(t.Context(), wfCallChainKey{}, []string{"A"})
		res, err := engine.Execute(ctx, defns["A"], RunContext{TmpDir: t.TempDir(), RunDir: t.TempDir()})
		require.NoError(t, err)
		assert.Equal(t, RunStatusFailed, res.Status)
		assert.ErrorContains(t, res.Nodes["n"].Error, "workflow cycle detected: A already in call chain")
	})

	t.Run("B->C->B indirect cycle detection", func(t *testing.T) {
		ctx := context.WithValue(t.Context(), wfCallChainKey{}, []string{"B"})
		res, err := engine.Execute(ctx, defns["B"], RunContext{TmpDir: t.TempDir(), RunDir: t.TempDir()})
		require.NoError(t, err)
		assert.Equal(t, RunStatusFailed, res.Status)
		assert.ErrorContains(t, res.Nodes["n"].Error, "workflow cycle detected: B already in call chain")
	})

	t.Run("recursion depth 4 succeeded and depth 5 rejected", func(t *testing.T) {
		// Depth 4 success: root chain has ["root"] -> calls D2 (chain ["root", "D2"]) -> calls D3 (chain ["root", "D2", "D3"]) -> calls D4 (chain ["root", "D2", "D3", "D4"]) -> calls D5 command.
		// Total workflow definitions in chain when D5 runs is 4 workflows ("root", "D2", "D3", "D4").
		ctxSuccess := context.WithValue(t.Context(), wfCallChainKey{}, []string{"root"})
		resSuccess, errSuccess := engine.Execute(ctxSuccess, defns["D2"], RunContext{TmpDir: t.TempDir(), RunDir: t.TempDir()})
		require.NoError(t, errSuccess)
		assert.Equal(t, RunStatusCompleted, resSuccess.Status)
		require.NotNil(t, resSuccess.Nodes["n"])
		assert.Equal(t, StatusSucceeded, resSuccess.Nodes["n"].Status)

		// Depth 5 rejected: root chain has ["root"] -> D1 -> D2 -> D3 -> D4 (length 4) -> calling D5 exceeds max depth 4.
		ctxReject := context.WithValue(t.Context(), wfCallChainKey{}, []string{"root"})
		resReject, errReject := engine.Execute(ctxReject, defns["D1"], RunContext{TmpDir: t.TempDir(), RunDir: t.TempDir()})
		require.NoError(t, errReject)
		assert.Equal(t, RunStatusFailed, resReject.Status)
		assert.ErrorContains(t, resReject.Nodes["n"].Error, "workflow recursion depth limit exceeded (max 4)")
	})
}

func TestSubWorkflowRunner_RejectsHumanNodeInSubWorkflow(t *testing.T) {
	t.Parallel()

	childYAML := `
name: child-human
nodes:
  - id: ask
    type: human
    prompt: "Are you there?"`

	childDefn, err := ParseDefinition([]byte(childYAML))
	require.NoError(t, err)

	engine, _ := setupTestEngineWithWorkflowRunner(func(name string) (*WorkflowDefinition, error) {
		return childDefn, nil
	})

	parentYAML := `
name: parent-wf
nodes:
  - id: sub
    type: workflow
    workflow: child-human`

	parentDefn, err := ParseDefinition([]byte(parentYAML))
	require.NoError(t, err)

	res, err := engine.Execute(t.Context(), parentDefn, RunContext{
		RunDir: t.TempDir(),
		TmpDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, RunStatusFailed, res.Status)
	assert.ErrorContains(t, res.Nodes["sub"].Error, "contains human node \"ask\" (not allowed in sub-workflows)")
}

func TestSubWorkflowRunner_HeadlessDefense_BlocksHumanNode(t *testing.T) {
	t.Parallel()

	humanYAML := `
name: test-human-wf
nodes:
  - id: ask
    type: human
    prompt: "Prompt"`

	defn, err := ParseDefinition([]byte(humanYAML))
	require.NoError(t, err)

	engine, _ := setupTestEngineWithWorkflowRunner(nil)

	res, err := engine.Execute(t.Context(), defn, RunContext{
		RunDir:   t.TempDir(),
		TmpDir:   t.TempDir(),
		Headless: true,
	})
	require.NoError(t, err)
	assert.Equal(t, RunStatusFailed, res.Status)
	assert.ErrorContains(t, res.Nodes["ask"].Error, "headless execution: human nodes not supported")
}

func TestSubWorkflowRunner_NodeTimeout(t *testing.T) {
	t.Parallel()

	childYAML := `
name: child-slow
nodes:
  - id: sleep-node
    type: command
    command: "sleep 2"`

	childDefn, err := ParseDefinition([]byte(childYAML))
	require.NoError(t, err)

	engine, _ := setupTestEngineWithWorkflowRunner(func(name string) (*WorkflowDefinition, error) {
		return childDefn, nil
	})

	parentYAML := `
name: parent-timeout
nodes:
  - id: sub
    type: workflow
    workflow: child-slow
    timeout: 50ms`

	parentDefn, err := ParseDefinition([]byte(parentYAML))
	require.NoError(t, err)

	res, err := engine.Execute(t.Context(), parentDefn, RunContext{
		RunDir: t.TempDir(),
		TmpDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, RunStatusFailed, res.Status)
	assert.Equal(t, StatusFailed, res.Nodes["sub"].Status)
}
