package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNodeContextInterpolateLoopIteration(t *testing.T) {
	nctx := &NodeContext{
		SessionID: "sess-1",
		Node:      &NodeSpec{ID: "fixer"},
		Defn:      &WorkflowDefinition{Name: "t"},
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
