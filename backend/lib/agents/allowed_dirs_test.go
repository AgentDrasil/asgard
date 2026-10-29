package agents

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func TestAllowedDirsIncludesWorkflowDirs(t *testing.T) {
	wf := filepath.Join(t.TempDir(), "workflow.yaml")
	require.NoError(t, os.WriteFile(wf, []byte(`name: wf
run_dirs: ["/wf/run"]
mount_dirs:
  readonly: ["/wf/ro"]
  readwrite: ["/wf/rw"]
nodes:
  - id: start
    type: command
    command: "true"
`), 0o644))
	list := []*agentspec.Agent{
		nil,
		{Config: agentspec.AgentConfig{
			RunDirs:   []string{"/agent/run"},
			MountDirs: agentspec.MountConfig{ReadOnly: []string{"/agent/ro"}, ReadWrite: []string{"/agent/rw"}},
		}},
		{WorkflowPath: wf},
		{WorkflowPath: filepath.Join(t.TempDir(), "missing.yaml")},
	}
	assert.ElementsMatch(t, []string{"/agent/run", "/agent/rw", "/wf/run", "/wf/rw"}, AllowedDirs(list, true))
	assert.ElementsMatch(t, []string{"/agent/run", "/agent/rw", "/agent/ro", "/wf/run", "/wf/rw", "/wf/ro"}, AllowedDirs(list, false))
}

func TestReadonlyDirsFailsClosedOnMalformedEntries(t *testing.T) {
	dirs, err := ReadonlyDirs([]*agentspec.Agent{
		nil,
		{Config: agentspec.AgentConfig{MountDirs: agentspec.MountConfig{ReadOnly: []string{"/ok/ro", "rel/ro"}}}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"rel/ro"`)
	assert.Equal(t, []string{"/ok/ro"}, dirs, "the absolute entries are still returned")

	dirs, err = ReadonlyDirs([]*agentspec.Agent{{Config: agentspec.AgentConfig{MountDirs: agentspec.MountConfig{ReadOnly: []string{"/a", "/b"}}}}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/a", "/b"}, dirs)

	// A workflow whose definition does not load (relative mount entry) leaves
	// its readonly mounts unknown: an error, not a silent skip.
	wf := filepath.Join(t.TempDir(), "workflow.yaml")
	require.NoError(t, os.WriteFile(wf, []byte("name: wf\nmount_dirs:\n  readonly: [\"rel/ro\"]\nnodes:\n  - id: start\n    type: command\n    command: \"true\"\n"), 0o644))
	_, err = ReadonlyDirs([]*agentspec.Agent{{WorkflowPath: wf}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rel/ro")
	// A missing workflow file is not an error.
	_, err = ReadonlyDirs([]*agentspec.Agent{{WorkflowPath: filepath.Join(t.TempDir(), "none.yaml")}})
	assert.NoError(t, err)
}
