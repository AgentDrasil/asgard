package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/elmhuangyu/configs/pkg/configlib"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func agentSourceYAML(id string) []byte {
	return []byte("id: " + id + "\nname: " + id + "\ndescription: d\nrun_dirs: [/tmp]\ncli:\n  - cli: agy\n    model: Gemini 3.5 Flash (Low)\n")
}

func TestServer_ReloadsAgentsFromSourceOnChange(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	agentwrapper.SetClients(map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"Gemini 3.5 Flash (Low)"}},
	})
	t.Cleanup(func() { agentwrapper.SetClients(nil) })

	src := configlib.NewMemSource(configlib.FlattenKVs{
		"agents/agent_father/config.yaml": agentSourceYAML("agent_father"),
	})
	store := config.NewAgentStore(config.PrefixedSource{Source: src})
	require.NoError(t, store.Start(context.Background()))
	t.Cleanup(store.Close)

	srv, err := New(&config.Config{AgentDir: t.TempDir()}, nil, WithAgentSource(store))
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	agentIDs := func() []string {
		srv.mu.RLock()
		defer srv.mu.RUnlock()
		ids := make([]string, 0, len(srv.agents))
		for _, a := range srv.agents {
			ids = append(ids, a.Config.ID)
		}
		return ids
	}
	assert.Equal(t, []string{"agent_father"}, agentIDs())

	store.OnChange(func() { _ = srv.ReloadAgents() })
	src.Set(configlib.FlattenKVs{
		"agents/agent_father/config.yaml": agentSourceYAML("agent_father"),
		"agents/helper/config.yaml":       agentSourceYAML("helper"),
	})
	require.Eventually(t, func() bool { return len(agentIDs()) == 2 }, 5*time.Second, 20*time.Millisecond)

	// Dropping agent_father makes the reload fail; the previous list is kept
	// and the failure is reported.
	src.Set(configlib.FlattenKVs{"agents/helper/config.yaml": agentSourceYAML("helper")})
	require.Eventually(t, func() bool {
		return len(srv.diagnostics.Snapshot().Errors) > 0
	}, 5*time.Second, 20*time.Millisecond)
	assert.Len(t, agentIDs(), 2)
}

// acceptRecorder is a Lister that records the lists passed to Accept.
type acceptRecorder struct {
	list  []*agentspec.Agent
	onAcc func()
}

func (a *acceptRecorder) LoadAll() ([]*agentspec.Agent, error) { return a.list, nil }
func (a *acceptRecorder) Accept([]*agentspec.Agent) {
	if a.onAcc != nil {
		a.onAcc()
	}
}

func TestServer_AcceptsAfterSwappingAgents(t *testing.T) {
	rec := &acceptRecorder{list: []*agentspec.Agent{{Config: agentspec.AgentConfig{ID: agentspec.AgentFatherID}}}}
	srv, err := New(&config.Config{AgentDir: t.TempDir()}, nil, WithAgentSource(rec))
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	var servedAtAccept int
	rec.onAcc = func() {
		srv.mu.RLock()
		defer srv.mu.RUnlock()
		servedAtAccept = len(srv.agents)
	}
	rec.list = append(rec.list, &agentspec.Agent{Config: agentspec.AgentConfig{ID: "second"}})
	require.NoError(t, srv.reload())
	assert.Equal(t, 2, servedAtAccept, "Accept runs after the server switched lists")
}
