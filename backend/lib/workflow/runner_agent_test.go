package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

func TestResolveWorkflowDirs_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		rc            RunContext
		defn          *workflowspec.WorkflowDefinition
		wantRunDirs   []string
		wantMountDirs workflowspec.MountDirsConfig
	}{
		{
			name: "rc overrides definition completely",
			rc: RunContext{
				RunDir:          "/rc/run",
				WorkflowRunDirs: []string{"/rc/allowed"},
				WorkflowMountDirs: workflowspec.MountDirsConfig{
					ReadOnly:  []string{"/rc/ro"},
					ReadWrite: []string{"/rc/rw"},
				},
			},
			defn: &workflowspec.WorkflowDefinition{
				RunDirs: []string{"/defn/allowed"},
				MountDirs: workflowspec.MountDirsConfig{
					ReadOnly:  []string{"/defn/ro"},
					ReadWrite: []string{"/defn/rw"},
				},
			},
			wantRunDirs: []string{"/rc/allowed"},
			wantMountDirs: workflowspec.MountDirsConfig{
				ReadOnly:  []string{"/rc/ro"},
				ReadWrite: []string{"/rc/rw"},
			},
		},
		{
			name: "fallback to definition RunDirs and MountDirs when rc has none",
			rc: RunContext{
				RunDir: "/fallback/dir",
			},
			defn: &workflowspec.WorkflowDefinition{
				RunDirs: []string{"/defn/allowed"},
				MountDirs: workflowspec.MountDirsConfig{
					ReadOnly:  []string{"/defn/ro"},
					ReadWrite: []string{"/defn/rw"},
				},
			},
			wantRunDirs: []string{"/defn/allowed"},
			wantMountDirs: workflowspec.MountDirsConfig{
				ReadOnly:  []string{"/defn/ro"},
				ReadWrite: []string{"/defn/rw"},
			},
		},
		{
			name: "fallback to rc.RunDir when both rc.WorkflowRunDirs and defn.RunDirs are empty",
			rc: RunContext{
				RunDir: "/rc/rundir/only",
			},
			defn:        &workflowspec.WorkflowDefinition{},
			wantRunDirs: []string{"/rc/rundir/only"},
			wantMountDirs: workflowspec.MountDirsConfig{
				ReadOnly:  nil,
				ReadWrite: nil,
			},
		},
		{
			name: "independent fallback for ReadOnly and ReadWrite mounts",
			rc: RunContext{
				WorkflowMountDirs: workflowspec.MountDirsConfig{
					ReadOnly: []string{"/rc/ro/only"},
					// ReadWrite is empty in rc -> should fallback to defn.ReadWrite
				},
			},
			defn: &workflowspec.WorkflowDefinition{
				MountDirs: workflowspec.MountDirsConfig{
					ReadOnly:  []string{"/defn/ro"},
					ReadWrite: []string{"/defn/rw/fallback"},
				},
			},
			wantRunDirs: nil,
			wantMountDirs: workflowspec.MountDirsConfig{
				ReadOnly:  []string{"/rc/ro/only"},
				ReadWrite: []string{"/defn/rw/fallback"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotRunDirs, gotMountDirs := resolveWorkflowDirs(tt.rc, tt.defn)
			assert.Equal(t, tt.wantRunDirs, gotRunDirs)
			assert.Equal(t, tt.wantMountDirs, gotMountDirs)
		})
	}
}

func TestResolveEffectiveAgent_TableDriven(t *testing.T) {
	t.Parallel()

	nctx := &NodeContext{
		WorkflowRunDirs: []string{"/wf/rundir"},
		WorkflowMountDirs: workflowspec.MountDirsConfig{
			ReadOnly:  []string{"/wf/ro"},
			ReadWrite: []string{"/wf/rw"},
		},
	}

	tests := []struct {
		name          string
		inputAgent    *agentspec.Agent
		wantRunDirs   []string
		wantMountDirs agentspec.MountConfig
	}{
		{
			name: "agent with empty RunDirs and MountDirs inherits all from workflow",
			inputAgent: &agentspec.Agent{
				Config: agentspec.AgentConfig{
					ID:   "child-empty",
					Name: "Child Empty",
				},
			},
			wantRunDirs: []string{"/wf/rundir"},
			wantMountDirs: agentspec.MountConfig{
				ReadOnly:  []string{"/wf/ro"},
				ReadWrite: []string{"/wf/rw"},
			},
		},
		{
			name: "agent with explicit RunDirs retains own RunDirs but inherits MountDirs",
			inputAgent: &agentspec.Agent{
				Config: agentspec.AgentConfig{
					ID:      "child-with-rundir",
					Name:    "Child with RunDir",
					RunDirs: []string{"/agent/own/run"},
				},
			},
			wantRunDirs: []string{"/agent/own/run"},
			wantMountDirs: agentspec.MountConfig{
				ReadOnly:  []string{"/wf/ro"},
				ReadWrite: []string{"/wf/rw"},
			},
		},
		{
			name: "agent with explicit ReadOnly retains own RO but inherits RW",
			inputAgent: &agentspec.Agent{
				Config: agentspec.AgentConfig{
					ID:   "child-custom-ro",
					Name: "Child Custom RO",
					MountDirs: agentspec.MountConfig{
						ReadOnly: []string{"/agent/own/ro"},
					},
				},
			},
			wantRunDirs: []string{"/wf/rundir"},
			wantMountDirs: agentspec.MountConfig{
				ReadOnly:  []string{"/agent/own/ro"},
				ReadWrite: []string{"/wf/rw"},
			},
		},
		{
			name: "agent with explicit ReadWrite retains own RW but inherits RO",
			inputAgent: &agentspec.Agent{
				Config: agentspec.AgentConfig{
					ID:   "child-custom-rw",
					Name: "Child Custom RW",
					MountDirs: agentspec.MountConfig{
						ReadWrite: []string{"/agent/own/rw"},
					},
				},
			},
			wantRunDirs: []string{"/wf/rundir"},
			wantMountDirs: agentspec.MountConfig{
				ReadOnly:  []string{"/wf/ro"},
				ReadWrite: []string{"/agent/own/rw"},
			},
		},
		{
			name: "agent with full custom config retains all custom settings",
			inputAgent: &agentspec.Agent{
				Config: agentspec.AgentConfig{
					ID:      "child-full-custom",
					Name:    "Child Full Custom",
					RunDirs: []string{"/custom/run"},
					MountDirs: agentspec.MountConfig{
						ReadOnly:  []string{"/custom/ro"},
						ReadWrite: []string{"/custom/rw"},
					},
				},
			},
			wantRunDirs: []string{"/custom/run"},
			wantMountDirs: agentspec.MountConfig{
				ReadOnly:  []string{"/custom/ro"},
				ReadWrite: []string{"/custom/rw"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Snapshot original fields before invocation to verify immutability
			origRunDirs := append([]string(nil), tt.inputAgent.Config.RunDirs...)
			origRO := append([]string(nil), tt.inputAgent.Config.MountDirs.ReadOnly...)
			origRW := append([]string(nil), tt.inputAgent.Config.MountDirs.ReadWrite...)

			effective := resolveEffectiveAgent(tt.inputAgent, nctx)
			require.NotNil(t, effective)
			assert.Equal(t, tt.wantRunDirs, effective.Config.RunDirs)
			assert.Equal(t, tt.wantMountDirs.ReadOnly, effective.Config.MountDirs.ReadOnly)
			assert.Equal(t, tt.wantMountDirs.ReadWrite, effective.Config.MountDirs.ReadWrite)

			// Original agent must remain unmodified (immutability)
			assert.Equal(t, origRunDirs, tt.inputAgent.Config.RunDirs)
			assert.Equal(t, origRO, tt.inputAgent.Config.MountDirs.ReadOnly)
			assert.Equal(t, origRW, tt.inputAgent.Config.MountDirs.ReadWrite)
			assert.NotSame(t, tt.inputAgent, effective)
		})
	}
}

func TestResolveEffectiveAgent_NilHandling(t *testing.T) {
	t.Parallel()

	assert.Nil(t, resolveEffectiveAgent(nil, &NodeContext{}))
}

func TestEntryValidation(t *testing.T) {
	t.Parallel()

	const noAgentWorkflow = `
name: w
nodes:
  - id: classify
    type: llm
    model: m
    prompt: classify ${input}
`

	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "entry on agent node is valid",
			yaml: `
name: w
nodes:
  - id: a1
    type: agent
    agent_id: agy
    entry: true
`,
		},
		{
			name: "multiple entry agents are valid",
			yaml: `
name: w
nodes:
  - id: a1
    type: agent
    agent_id: agy
    entry: true
  - id: a2
    type: agent
    agent_id: agy
    entry: true
`,
		},
		{
			name: "agent workflow without entry is rejected",
			yaml: `
name: w
nodes:
  - id: a1
    type: agent
    agent_id: agy
`,
			wantErr: "entry: true",
		},
		{
			name: "entry on llm node is rejected",
			yaml: `
name: w
nodes:
  - id: a1
    type: agent
    agent_id: agy
    entry: true
  - id: l1
    type: llm
    model: m
    entry: true
    prompt: summarize
`,
			wantErr: "entry is only allowed on agent nodes",
		},
		{
			name: "workflow without agent nodes needs no entry",
			yaml: noAgentWorkflow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := workflowspec.ParseDefinition([]byte(tt.yaml))
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestAgentRunner_SetAgents(t *testing.T) {
	t.Parallel()

	runner := NewAgentRunnerWithListener(nil, nil, nil)
	agentRunnerInstance, ok := runner.(*agentRunner)
	require.True(t, ok)

	testAgents := []*agentspec.Agent{
		{
			Config: agentspec.AgentConfig{
				ID:   "test-agent-1",
				Name: "Test Agent 1",
			},
		},
		{
			Config: agentspec.AgentConfig{
				ID:   "test-agent-2",
				Name: "Test Agent 2",
			},
		},
	}

	agentRunnerInstance.SetAgents(testAgents)

	a1, err := agentRunnerInstance.lookup("test-agent-1")
	require.NoError(t, err)
	assert.Equal(t, "test-agent-1", a1.Config.ID)

	a2, err := agentRunnerInstance.lookup("test-agent-2")
	require.NoError(t, err)
	assert.Equal(t, "test-agent-2", a2.Config.ID)

	_, err = agentRunnerInstance.lookup("non-existent")
	assert.ErrorContains(t, err, "agent \"non-existent\" not found")

	// Test Engine.SetAgents
	reg := NewNodeRunnerRegistry()
	reg.Register(runner)
	eng := NewEngine(reg)
	eng.SetAgents([]*agentspec.Agent{
		{
			Config: agentspec.AgentConfig{
				ID:   "updated-agent",
				Name: "Updated",
			},
		},
	})

	updated, err := agentRunnerInstance.lookup("updated-agent")
	require.NoError(t, err)
	assert.Equal(t, "Updated", updated.Config.Name)
}

func TestCheckRequiredOutputs_TableDriven(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	validFile := filepath.Join(tempDir, "output.md")
	require.NoError(t, os.WriteFile(validFile, []byte("# Valid Output"), 0644))

	emptyFile := filepath.Join(tempDir, "empty.md")
	require.NoError(t, os.WriteFile(emptyFile, []byte(""), 0644))

	nctx := &NodeContext{
		RunDir:    tempDir,
		TmpDir:    tempDir,
		SessionID: "sess-123",
	}

	tests := []struct {
		name            string
		requiredOutputs []string
		wantMissing     []string
	}{
		{
			name:            "empty required outputs returns nil",
			requiredOutputs: nil,
			wantMissing:     nil,
		},
		{
			name:            "all files exist and non-empty",
			requiredOutputs: []string{"${tmp_dir}/output.md"},
			wantMissing:     nil,
		},
		{
			name:            "file does not exist",
			requiredOutputs: []string{"${tmp_dir}/missing.md"},
			wantMissing:     []string{filepath.Join(tempDir, "missing.md")},
		},
		{
			name:            "file exists but is empty",
			requiredOutputs: []string{"${tmp_dir}/empty.md"},
			wantMissing:     []string{filepath.Join(tempDir, "empty.md")},
		},
		{
			name:            "mixed existing, empty and missing files",
			requiredOutputs: []string{"${tmp_dir}/output.md", "${tmp_dir}/empty.md", "${tmp_dir}/nonexistent.md"},
			wantMissing:     []string{filepath.Join(tempDir, "empty.md"), filepath.Join(tempDir, "nonexistent.md")},
		},
		{
			name:            "empty or unresolved variable interpolation entry is caught as missing",
			requiredOutputs: []string{"${unknown_variable}"},
			wantMissing:     []string{"${unknown_variable}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			missing := checkRequiredOutputs(tt.requiredOutputs, nctx)
			assert.Equal(t, tt.wantMissing, missing)
		})
	}
}

func TestAgentRunner_Headless_EntryEmptyInputFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		nctx       *NodeContext
		node       *workflowspec.NodeSpec
		resuming   bool
		wantPrompt string
		wantErr    bool
		errContain string
	}{
		{
			name:       "resuming session uses follow-up prompt",
			nctx:       &NodeContext{Input: "some input", Headless: false},
			node:       &workflowspec.NodeSpec{ID: "agent-1", Entry: true},
			resuming:   true,
			wantPrompt: agentFollowUpPrompt,
		},
		{
			name:       "entry node with non-empty input",
			nctx:       &NodeContext{Input: "hello world", Headless: false},
			node:       &workflowspec.NodeSpec{ID: "agent-1", Entry: true},
			resuming:   false,
			wantPrompt: "hello world",
		},
		{
			name:       "entry node with empty input in non-headless mode returns error",
			nctx:       &NodeContext{Input: "   ", Headless: false},
			node:       &workflowspec.NodeSpec{ID: "agent-1", Entry: true},
			resuming:   false,
			wantErr:    true,
			errContain: "the workflow input is empty",
		},
		{
			name:       "entry node with empty input in headless mode falls back to agentStartPrompt",
			nctx:       &NodeContext{Input: "", Headless: true},
			node:       &workflowspec.NodeSpec{ID: "agent-1", Entry: true},
			resuming:   false,
			wantPrompt: agentStartPrompt,
		},
		{
			name:       "non-entry fresh node uses agentStartPrompt",
			nctx:       &NodeContext{Input: "", Headless: false},
			node:       &workflowspec.NodeSpec{ID: "agent-2", Entry: false},
			resuming:   false,
			wantPrompt: agentStartPrompt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotPrompt, err := resolveAgentPrompt(tt.nctx, tt.node, tt.resuming)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContain)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantPrompt, gotPrompt)
			}
		})
	}

	// Also verify full runner.Run error on non-headless entry with empty input
	runner := NewAgentRunnerWithListener(nil, nil, nil)
	agentRunnerInstance, ok := runner.(*agentRunner)
	require.True(t, ok)

	agentRunnerInstance.SetAgents([]*agentspec.Agent{
		{
			Config: agentspec.AgentConfig{
				ID:   "test-agent",
				Name: "Test Agent",
				CLI: []agentspec.CLITarget{
					{CLI: "echo", Model: "dummy"},
				},
			},
		},
	})

	node := &workflowspec.NodeSpec{
		ID:      "agent-1",
		Type:    workflowspec.NodeTypeAgent,
		AgentID: "test-agent",
		Entry:   true,
	}

	nctxNonHeadless := &NodeContext{
		SessionID: "sess-1",
		RunDir:    t.TempDir(),
		TmpDir:    t.TempDir(),
		Input:     "",
		Node:      node,
		Headless:  false,
		Values:    &RunValues{},
	}

	_, err := runner.Run(t.Context(), nctxNonHeadless)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the workflow input is empty")
}
