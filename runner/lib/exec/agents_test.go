package exec

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// fakeAgents is a mutable AgentSource.
type fakeAgents struct {
	mu     sync.Mutex
	agents []*agentspec.Agent
	err    error
}

func (f *fakeAgents) Agents() ([]*agentspec.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.agents, f.err
}

func (f *fakeAgents) set(a ...*agentspec.Agent) {
	f.mu.Lock()
	f.agents = a
	f.mu.Unlock()
}

func testAgent(id, hash, node string) *agentspec.Agent {
	return &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:      id,
			Name:    id,
			CLI:     []agentspec.CLITarget{{CLI: "opencode", Model: "m1"}},
			RunDirs: []string{"/cfg-run"},
		},
		Hash:   hash,
		NodeID: node,
	}
}

func TestResolveAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		agents  []*agentspec.Agent
		err     error
		sr      *runnerpb.StartRun
		wantErr string
	}{
		{
			name:   "global match",
			agents: []*agentspec.Agent{testAgent("a", "h1", "")},
			sr:     &runnerpb.StartRun{AgentId: "a", AgentHash: "h1"},
		},
		{
			name:   "runner source match",
			agents: []*agentspec.Agent{testAgent("a", "h1", "node")},
			sr:     &runnerpb.StartRun{AgentId: "a", AgentHash: "h1", AgentSource: runnerpb.AgentSource_AGENT_SOURCE_RUNNER},
		},
		{
			name:    "hash never arrives",
			agents:  []*agentspec.Agent{testAgent("a", "old", "")},
			sr:      &runnerpb.StartRun{AgentId: "a", AgentHash: "new"},
			wantErr: "runner has hash old",
		},
		{
			name:    "wrong source",
			agents:  []*agentspec.Agent{testAgent("a", "h1", "")},
			sr:      &runnerpb.StartRun{AgentId: "a", AgentHash: "h1", AgentSource: runnerpb.AgentSource_AGENT_SOURCE_RUNNER},
			wantErr: "different source",
		},
		{
			name:    "unknown agent",
			agents:  []*agentspec.Agent{testAgent("a", "h1", "")},
			sr:      &runnerpb.StartRun{AgentId: "b", AgentHash: "h1"},
			wantErr: "no such agent",
		},
		{
			name:    "definitions unusable",
			err:     errors.New("boom"),
			sr:      &runnerpb.StartRun{AgentId: "a", AgentHash: "h1"},
			wantErr: "boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := &fakeAgents{agents: tt.agents, err: tt.err}
			got, err := resolveAgent(context.Background(), src, tt.sr, 50*time.Millisecond, 5*time.Millisecond)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr)
				assert.ErrorContains(t, err, "not available on this runner")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.sr.GetAgentHash(), got.Hash)
		})
	}
}

func TestResolveAgentWaitsForHash(t *testing.T) {
	t.Parallel()

	src := &fakeAgents{agents: []*agentspec.Agent{testAgent("a", "old", "")}}
	go func() {
		time.Sleep(60 * time.Millisecond)
		src.set(testAgent("a", "new", ""))
	}()
	got, err := resolveAgent(context.Background(), src, &runnerpb.StartRun{AgentId: "a", AgentHash: "new"}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "new", got.Hash)
}

func TestResolveAgentContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := resolveAgent(ctx, &fakeAgents{}, &runnerpb.StartRun{AgentId: "a"}, time.Minute, time.Millisecond)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestApplyOverlay(t *testing.T) {
	t.Parallel()

	base := testAgent("a", "h", "")
	base.Config.MountDirs.ReadOnly = []string{"/ro"}
	base.Config.MountDirs.ReadWrite = []string{"/rw"}

	t.Run("empty keeps agent config", func(t *testing.T) {
		t.Parallel()
		got := applyOverlay(base, &runnerpb.StartRun{})
		assert.Equal(t, []string{"/cfg-run"}, got.Config.RunDirs)
		assert.Equal(t, []string{"/ro"}, got.Config.MountDirs.ReadOnly)
		assert.Equal(t, []string{"/rw"}, got.Config.MountDirs.ReadWrite)
	})
	t.Run("fields override independently", func(t *testing.T) {
		t.Parallel()
		got := applyOverlay(base, &runnerpb.StartRun{RunDirs: []string{"/wf-run"}, MountDirsReadwrite: []string{"/wf-rw"}})
		assert.Equal(t, []string{"/wf-run"}, got.Config.RunDirs)
		assert.Equal(t, []string{"/ro"}, got.Config.MountDirs.ReadOnly)
		assert.Equal(t, []string{"/wf-rw"}, got.Config.MountDirs.ReadWrite)
	})
	t.Run("does not mutate the shared agent", func(t *testing.T) {
		t.Parallel()
		_ = applyOverlay(base, &runnerpb.StartRun{RunDirs: []string{"/x"}})
		assert.Equal(t, []string{"/cfg-run"}, base.Config.RunDirs)
	})
	t.Run("deep copies mask rules and readwrite entries when not overridden", func(t *testing.T) {
		t.Parallel()
		agent := testAgent("a", "h", "")
		agent.Config.MountDirs.ReadWrite = []string{"/rw"}
		agent.Config.MountDirs.ReadWriteEntries = []agentspec.MountPathEntry{
			{
				Path: "/rw",
				MaskRules: &agentspec.SubdirMaskRule{
					Base:   "/rw",
					Mode:   agentspec.MaskOthers,
					KeepRW: []string{".pnpm-store"},
				},
			},
		}
		agent.Config.MountDirs.MaskRules = map[string]*agentspec.SubdirMaskRule{
			"/rw": {
				Base:   "/rw",
				Mode:   agentspec.MaskOthers,
				KeepRW: []string{".pnpm-store"},
			},
		}

		got := applyOverlay(agent, &runnerpb.StartRun{})
		require.NotNil(t, got.Config.MountDirs.MaskRules)
		require.NotNil(t, got.Config.MountDirs.MaskRules["/rw"])
		assert.Equal(t, []string{".pnpm-store"}, got.Config.MountDirs.MaskRules["/rw"].KeepRW)

		got.Config.MountDirs.MaskRules["/rw"].KeepRW[0] = "mutated"
		got.Config.MountDirs.ReadWriteEntries[0].Path = "/mutated"

		assert.Equal(t, []string{".pnpm-store"}, agent.Config.MountDirs.MaskRules["/rw"].KeepRW)
		assert.Equal(t, "/rw", agent.Config.MountDirs.ReadWriteEntries[0].Path)
	})
	t.Run("preserves mask rules and readwrite entries when sr.MountDirsReadwrite contains the same path", func(t *testing.T) {
		t.Parallel()
		agent := testAgent("a", "h", "")
		agent.Config.MountDirs.ReadWrite = []string{"/rw"}
		agent.Config.MountDirs.ReadWriteEntries = []agentspec.MountPathEntry{
			{
				Path: "/rw",
				MaskRules: &agentspec.SubdirMaskRule{
					Base:   "/rw",
					Mode:   agentspec.MaskOthers,
					KeepRW: []string{".pnpm-store"},
				},
			},
		}
		agent.Config.MountDirs.MaskRules = map[string]*agentspec.SubdirMaskRule{
			"/rw": {
				Base:   "/rw",
				Mode:   agentspec.MaskOthers,
				KeepRW: []string{".pnpm-store"},
			},
		}

		got := applyOverlay(agent, &runnerpb.StartRun{MountDirsReadwrite: []string{"/rw"}})
		require.NotNil(t, got.Config.MountDirs.MaskRules)
		require.NotNil(t, got.Config.MountDirs.MaskRules["/rw"])
		assert.Equal(t, []string{".pnpm-store"}, got.Config.MountDirs.MaskRules["/rw"].KeepRW)
		require.Len(t, got.Config.MountDirs.ReadWriteEntries, 1)
		assert.Equal(t, "/rw", got.Config.MountDirs.ReadWriteEntries[0].Path)
	})
	t.Run("drops mask rules and readwrite entries when sr.MountDirsReadwrite overrides with different path", func(t *testing.T) {
		t.Parallel()
		agent := testAgent("a", "h", "")
		agent.Config.MountDirs.ReadWrite = []string{"/rw"}
		agent.Config.MountDirs.ReadWriteEntries = []agentspec.MountPathEntry{
			{
				Path: "/rw",
				MaskRules: &agentspec.SubdirMaskRule{
					Base: "/rw",
					Mode: agentspec.MaskOthers,
				},
			},
		}
		agent.Config.MountDirs.MaskRules = map[string]*agentspec.SubdirMaskRule{
			"/rw": {
				Base: "/rw",
				Mode: agentspec.MaskOthers,
			},
		}

		got := applyOverlay(agent, &runnerpb.StartRun{MountDirsReadwrite: []string{"/other-rw"}})
		assert.Equal(t, []string{"/other-rw"}, got.Config.MountDirs.ReadWrite)
		assert.Empty(t, got.Config.MountDirs.ReadWriteEntries)
		assert.Nil(t, got.Config.MountDirs.MaskRules)
		assert.NotNil(t, agent.Config.MountDirs.MaskRules["/rw"])
	})
	t.Run("ignores sr.MountDirsReadwrite when agent has LockModeReadOnly", func(t *testing.T) {
		t.Parallel()
		agent := testAgent("a", "h", "")
		agent.Config.LockMode = agentspec.LockModeReadOnly
		agent.Config.MountDirs.ReadOnly = []string{"/ro"}

		got := applyOverlay(agent, &runnerpb.StartRun{MountDirsReadwrite: []string{"/attempted-rw"}})
		assert.True(t, got.Config.IsReadOnly())
		assert.Empty(t, got.Config.MountDirs.ReadWrite)
		assert.Empty(t, got.Config.MountDirs.ReadWriteEntries)
		assert.Nil(t, got.Config.MountDirs.MaskRules)
		assert.Equal(t, []string{"/ro"}, got.Config.MountDirs.ReadOnly)
	})
}
