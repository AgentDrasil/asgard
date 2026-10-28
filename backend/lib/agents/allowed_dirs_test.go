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
