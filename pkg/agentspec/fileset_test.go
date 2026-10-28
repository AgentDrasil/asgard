package agentspec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// useMockCLIs fakes the installed CLIs; it is process-global state, so callers
// must not be parallel at the top level.
func useMockCLIs(t *testing.T) {
	t.Helper()
	agentwrapper.SetClients(map[string]types.CLIClient{
		"opencode": &mockClient{models: []string{"deepseek-chat"}},
	})
	t.Cleanup(func() { agentwrapper.SetClients(nil) })
}

func agentCfg(id, extra string) []byte {
	return []byte("id: " + id + "\nname: " + id + "\ndescription: d\nmain_agent: false\ncli:\n  - cli: opencode\n    model: deepseek-chat\n" + extra)
}

func TestParseFiles(t *testing.T) {
	useMockCLIs(t)

	t.Run("global and runner agents", func(t *testing.T) {
		t.Parallel()

		set, err := ParseFiles(map[string][]byte{
			TeamsKey:                           []byte("teams:\n  - core\n"),
			"agents/a/config.yaml":             agentCfg("a", "team: core\n"),
			"agents/a/AGENTS.md":               []byte("hello"),
			"agents/a/skills/x/SKILL.md":       []byte("skill"),
			"agents/wf/config.yaml":            agentCfg("wf", "type: workflow\n"),
			"agents/wf/workflow.yaml":          []byte("name: wf\n"),
			"agents/nocfg/AGENTS.md":           []byte("ignored"),
			"runners/n1/agents/b/config.yaml":  agentCfg("b", ""),
			"runners/n1/agents/b/AGENTS.md":    []byte("b"),
			"runners/n1/other/c/config.yaml":   agentCfg("c", ""),
			"asgard/config.yaml":               []byte("ignored"),
			"runners/n2/agents/b/config.yaml":  agentCfg("b", ""),
			"runners/n2/agents/b/workflow.txt": []byte("x"),
		})
		require.NoError(t, err)

		assert.Equal(t, []string{"core"}, set.Teams)
		assert.Len(t, set.Global, 2)
		assert.Equal(t, []byte("skill"), set.Global["a"].Files["skills/x/SKILL.md"])
		assert.Equal(t, "core", set.Global["a"].Config.Team)
		assert.Len(t, set.Runners["n1"], 1)
		assert.Len(t, set.Runners["n2"], 1)
		assert.Equal(t, "n1", set.Runners["n1"]["b"].NodeID)
		assert.Empty(t, set.Collisions())
	})

	tests := []struct {
		name    string
		files   map[string][]byte
		wantErr string
	}{
		{"id mismatch", map[string][]byte{"agents/a/config.yaml": agentCfg("z", "")}, "does not match directory name"},
		{"unknown team", map[string][]byte{"agents/a/config.yaml": agentCfg("a", "team: nope\n")}, "is not defined in teams.yaml"},
		{"unsupported cli", map[string][]byte{"agents/a/config.yaml": []byte("id: a\ncli:\n  - cli: nosuch\n    model: m\n")}, "invalid config for agent a"},
		{"workflow without workflow.yaml", map[string][]byte{"agents/a/config.yaml": agentCfg("a", "type: workflow\n")}, "workflow.yaml is missing"},
		{"bad teams.yaml", map[string][]byte{TeamsKey: []byte(": : :\n\t")}, "teams.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseFiles(tt.files)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSet_EffectiveAndCollisions(t *testing.T) {
	useMockCLIs(t)

	set, err := ParseFiles(map[string][]byte{
		"agents/g/config.yaml":              agentCfg("g", ""),
		"agents/dup/config.yaml":            agentCfg("dup", ""),
		"runners/n1/agents/r/config.yaml":   agentCfg("r", ""),
		"runners/n2/agents/dup/config.yaml": agentCfg("dup", ""),
	})
	require.NoError(t, err)

	eff, err := set.Effective("n1")
	require.NoError(t, err)
	var ids []string
	for _, d := range eff {
		ids = append(ids, d.Config.ID)
	}
	assert.Equal(t, []string{"dup", "g", "r"}, ids)

	eff, err = set.Effective("unknown-node")
	require.NoError(t, err)
	assert.Len(t, eff, 2)

	_, err = set.Effective("n2")
	require.Error(t, err)
	assert.ErrorContains(t, err, `agent id "dup" is defined both in agents/dup and in runners/n2/agents/dup`)

	cols := set.Collisions()
	require.Len(t, cols, 1)
	assert.ErrorContains(t, cols[0], "runners/n2/agents/dup")
}

func TestDefinition_Materialize(t *testing.T) {
	useMockCLIs(t)

	set, err := ParseFiles(map[string][]byte{
		"agents/wf/config.yaml":       agentCfg("wf", "type: workflow\n"),
		"agents/wf/workflow.yaml":     []byte("name: wf\n"),
		"agents/wf/AGENTS.md":         []byte("rules"),
		"agents/wf/bin/run.sh":        []byte("#!/bin/sh\necho hi\n"),
		"agents/wf/skills/s/SKILL.md": []byte("skill"),
	})
	require.NoError(t, err)
	def := set.Global["wf"]

	dir := filepath.Join(t.TempDir(), "cache", "wf@7")
	agent, err := def.Materialize(dir)
	require.NoError(t, err)

	assert.Equal(t, dir, agent.Path)
	assert.Equal(t, filepath.Join(dir, "workflow.yaml"), agent.WorkflowPath)
	assert.Equal(t, "wf", agent.Config.ID)

	got, err := os.ReadFile(filepath.Join(dir, "skills", "s", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "skill", string(got))
	info, err := os.Stat(filepath.Join(dir, "bin", "run.sh"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0o100)

	// Re-materializing the same revision reuses the directory and leaves no
	// staging leftovers.
	_, err = def.Materialize(dir)
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Dir(dir))
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	t.Run("unsafe path rejected", func(t *testing.T) {
		t.Parallel()

		bad := &Definition{Config: AgentConfig{ID: "x"}, Files: map[string][]byte{"../escape": []byte("x")}}
		_, err := bad.Materialize(filepath.Join(t.TempDir(), "x@1"))
		require.Error(t, err)
		assert.ErrorContains(t, err, "unsafe file path")
	})
}

func TestDefinition_Hash(t *testing.T) {
	t.Parallel()

	a := &Definition{Files: map[string][]byte{"a": []byte("1"), "b": []byte("2")}}
	b := &Definition{Files: map[string][]byte{"b": []byte("2"), "a": []byte("1")}}
	c := &Definition{Files: map[string][]byte{"a": []byte("1"), "b": []byte("3")}}
	assert.Equal(t, a.Hash(), b.Hash())
	assert.NotEqual(t, a.Hash(), c.Hash())
}

func TestValidateNodeID(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"runner-a", "n_1", "node2"} {
		assert.NoError(t, ValidateNodeID(id), id)
	}
	for _, id := range []string{"", "..", ".", "a/b", "../x", "Bad", "a b"} {
		assert.ErrorContains(t, ValidateNodeID(id), "invalid runner node id", id)
	}
}

func TestParseFiles_RejectsBadNodeID(t *testing.T) {
	useMockCLIs(t)

	_, err := ParseFiles(map[string][]byte{"runners/../agents/a/config.yaml": agentCfg("a", "")})
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid runner node id")
}

func TestParseFiles_RunnerErrorsAreScoped(t *testing.T) {
	useMockCLIs(t)

	set, err := ParseFiles(map[string][]byte{
		"agents/g/config.yaml":                 agentCfg("g", ""),
		"runners/n1/agents/Bad_Id/config.yaml": agentCfg("Bad_Id", ""),
		"runners/n1/agents/ok/config.yaml":     agentCfg("ok", ""),
		"runners/n2/agents/r/config.yaml":      agentCfg("r", ""),
	})
	require.NoError(t, err)
	require.Len(t, set.RunnerErrors(), 1)
	assert.ErrorContains(t, set.RunnerErrors()[0], "runner n1:")

	_, err = set.Effective("n1")
	require.Error(t, err)
	eff, err := set.Effective("n2")
	require.NoError(t, err)
	assert.Len(t, eff, 2)

	// Global errors still fail the whole set.
	_, err = ParseFiles(map[string][]byte{"agents/Bad_Id/config.yaml": agentCfg("Bad_Id", "")})
	require.Error(t, err)
}

func TestParseFiles_UnknownTeamInRunnerIsScoped(t *testing.T) {
	useMockCLIs(t)

	set, err := ParseFiles(map[string][]byte{"runners/n1/agents/a/config.yaml": agentCfg("a", "team: nope\n")})
	require.NoError(t, err)
	require.Len(t, set.RunnerErrors(), 1)
	assert.ErrorContains(t, set.RunnerErrors()[0], "runner n1")
}
