package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

func TestNodeContextInterpolateLoopIteration(t *testing.T) {
	nctx := &NodeContext{
		SessionID: "sess-1",
		Node:      &workflowspec.NodeSpec{ID: "fixer"},
		Defn:      &workflowspec.WorkflowDefinition{Name: "t"},
		LoopIterations: map[string]int{
			"fix_loop":  2,
			"step_loop": 4,
		},
	}

	assert.Equal(t,
		"iteration is 2 for fix_loop and 4 for step_loop",
		nctx.Interpolate("iteration is ${loops.fix_loop.iteration} for fix_loop and ${loops.step_loop.iteration} for step_loop"),
	)
	assert.Equal(t,
		"unknown ${loops.missing.iteration} stays verbatim",
		nctx.Interpolate("unknown ${loops.missing.iteration} stays verbatim"),
	)
}

func TestNodeContextInterpolateSessionDir(t *testing.T) {
	nctx := &NodeContext{
		SessionID:  "sess-1",
		RunDir:     "/home/user/project",
		TmpDir:     "/home/user/tmp/sess-1",
		SessionDir: "/home/user/session/sess-1",
		Input:      "hello",
		Node:       &workflowspec.NodeSpec{ID: "n1"},
		Defn:       &workflowspec.WorkflowDefinition{Name: "t"},
	}
	assert.Equal(t, "/home/user/session/sess-1/report.md", nctx.Interpolate("${session_dir}/report.md"))
	assert.Equal(t, "/home/user/tmp/sess-1/report.md", nctx.Interpolate("${tmp_dir}/report.md"))
	assert.Equal(t, "sess-1:/home/user/session/sess-1", nctx.Interpolate("${session_id}:${session_dir}"))
}
