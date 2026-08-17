package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/agents"
)

func TestResolveWorkflowDirs_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		rc            RunContext
		defn          *WorkflowDefinition
		wantRunDirs   []string
		wantMountDirs MountDirsConfig
	}{
		{
			name: "rc overrides definition completely",
			rc: RunContext{
				RunDir:          "/rc/run",
				WorkflowRunDirs: []string{"/rc/allowed"},
				WorkflowMountDirs: MountDirsConfig{
					ReadOnly:  []string{"/rc/ro"},
					ReadWrite: []string{"/rc/rw"},
				},
			},
			defn: &WorkflowDefinition{
				RunDirs: []string{"/defn/allowed"},
				MountDirs: MountDirsConfig{
					ReadOnly:  []string{"/defn/ro"},
					ReadWrite: []string{"/defn/rw"},
				},
			},
			wantRunDirs: []string{"/rc/allowed"},
			wantMountDirs: MountDirsConfig{
				ReadOnly:  []string{"/rc/ro"},
				ReadWrite: []string{"/rc/rw"},
			},
		},
		{
			name: "fallback to definition RunDirs and MountDirs when rc has none",
			rc: RunContext{
				RunDir: "/fallback/dir",
			},
			defn: &WorkflowDefinition{
				RunDirs: []string{"/defn/allowed"},
				MountDirs: MountDirsConfig{
					ReadOnly:  []string{"/defn/ro"},
					ReadWrite: []string{"/defn/rw"},
				},
			},
			wantRunDirs: []string{"/defn/allowed"},
			wantMountDirs: MountDirsConfig{
				ReadOnly:  []string{"/defn/ro"},
				ReadWrite: []string{"/defn/rw"},
			},
		},
		{
			name: "fallback to rc.RunDir when both rc.WorkflowRunDirs and defn.RunDirs are empty",
			rc: RunContext{
				RunDir: "/rc/rundir/only",
			},
			defn:        &WorkflowDefinition{},
			wantRunDirs: []string{"/rc/rundir/only"},
			wantMountDirs: MountDirsConfig{
				ReadOnly:  nil,
				ReadWrite: nil,
			},
		},
		{
			name: "independent fallback for ReadOnly and ReadWrite mounts",
			rc: RunContext{
				WorkflowMountDirs: MountDirsConfig{
					ReadOnly: []string{"/rc/ro/only"},
					// ReadWrite is empty in rc -> should fallback to defn.ReadWrite
				},
			},
			defn: &WorkflowDefinition{
				MountDirs: MountDirsConfig{
					ReadOnly:  []string{"/defn/ro"},
					ReadWrite: []string{"/defn/rw/fallback"},
				},
			},
			wantRunDirs: nil,
			wantMountDirs: MountDirsConfig{
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
		WorkflowMountDirs: MountDirsConfig{
			ReadOnly:  []string{"/wf/ro"},
			ReadWrite: []string{"/wf/rw"},
		},
	}

	tests := []struct {
		name          string
		inputAgent    *agents.Agent
		wantRunDirs   []string
		wantMountDirs agents.MountConfig
	}{
		{
			name: "agent with empty RunDirs and MountDirs inherits all from workflow",
			inputAgent: &agents.Agent{
				Config: agents.AgentConfig{
					ID:   "child-empty",
					Name: "Child Empty",
				},
			},
			wantRunDirs: []string{"/wf/rundir"},
			wantMountDirs: agents.MountConfig{
				ReadOnly:  []string{"/wf/ro"},
				ReadWrite: []string{"/wf/rw"},
			},
		},
		{
			name: "agent with explicit RunDirs retains own RunDirs but inherits MountDirs",
			inputAgent: &agents.Agent{
				Config: agents.AgentConfig{
					ID:      "child-with-rundir",
					Name:    "Child with RunDir",
					RunDirs: []string{"/agent/own/run"},
				},
			},
			wantRunDirs: []string{"/agent/own/run"},
			wantMountDirs: agents.MountConfig{
				ReadOnly:  []string{"/wf/ro"},
				ReadWrite: []string{"/wf/rw"},
			},
		},
		{
			name: "agent with explicit ReadOnly retains own RO but inherits RW",
			inputAgent: &agents.Agent{
				Config: agents.AgentConfig{
					ID:   "child-custom-ro",
					Name: "Child Custom RO",
					MountDirs: agents.MountConfig{
						ReadOnly: []string{"/agent/own/ro"},
					},
				},
			},
			wantRunDirs: []string{"/wf/rundir"},
			wantMountDirs: agents.MountConfig{
				ReadOnly:  []string{"/agent/own/ro"},
				ReadWrite: []string{"/wf/rw"},
			},
		},
		{
			name: "agent with explicit ReadWrite retains own RW but inherits RO",
			inputAgent: &agents.Agent{
				Config: agents.AgentConfig{
					ID:   "child-custom-rw",
					Name: "Child Custom RW",
					MountDirs: agents.MountConfig{
						ReadWrite: []string{"/agent/own/rw"},
					},
				},
			},
			wantRunDirs: []string{"/wf/rundir"},
			wantMountDirs: agents.MountConfig{
				ReadOnly:  []string{"/wf/ro"},
				ReadWrite: []string{"/agent/own/rw"},
			},
		},
		{
			name: "agent with full custom config retains all custom settings",
			inputAgent: &agents.Agent{
				Config: agents.AgentConfig{
					ID:      "child-full-custom",
					Name:    "Child Full Custom",
					RunDirs: []string{"/custom/run"},
					MountDirs: agents.MountConfig{
						ReadOnly:  []string{"/custom/ro"},
						ReadWrite: []string{"/custom/rw"},
					},
				},
			},
			wantRunDirs: []string{"/custom/run"},
			wantMountDirs: agents.MountConfig{
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

			_, err := ParseDefinition([]byte(tt.yaml))
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}
