package workflowspec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateSimpleExpr(t *testing.T) {
	defn := &WorkflowDefinition{Name: "t", Nodes: []*NodeSpec{
		{ID: "build_cmd", Type: NodeTypeCommand, Command: "true"},
	}}
	upstreams := map[string]*NodeResult{
		"build_cmd": {Status: StatusFailed, ExitCode: 2, Output: "boom"},
	}

	tests := []struct {
		name    string
		expr    string
		want    bool
		wantErr bool
	}{
		{"exit code non-zero", "nodes.build_cmd.exit_code != 0", true, false},
		{"exit code zero", "nodes.build_cmd.exit_code == 0", false, false},
		{"exit code numeric compare", "nodes.build_cmd.exit_code > 1", true, false},
		{"exit code gte", "nodes.build_cmd.exit_code >= 2", true, false},
		{"exit code lt", "nodes.build_cmd.exit_code < 5", true, false},
		{"status single quoted", "nodes.build_cmd.status == 'FAILED'", true, false},
		{"status double quoted", `nodes.build_cmd.status == "FAILED"`, true, false},
		{"status mismatch", "nodes.build_cmd.status == 'SUCCEEDED'", false, false},
		{"output equality", "nodes.build_cmd.output == boom", true, false},
		{"unknown field", "nodes.build_cmd.nope == 1", false, true},
		{"unknown node", "nodes.other.exit_code == 0", false, true},
		{"bad operator", "nodes.build_cmd.exit_code = 0", false, true},
		{"missing literal", "nodes.build_cmd.exit_code ==", false, true},
		{"non-node path", "foo.bar == 1", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EvaluateSimpleExpr(tt.expr, upstreams, defn)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEvaluateSimpleExprSyntaxOnly(t *testing.T) {
	// Syntax-check mode (nil upstreams) must accept valid expressions without
	// resolving node references.
	ok, err := EvaluateSimpleExpr("nodes.anything.exit_code != 0", nil, nil)
	require.NoError(t, err)
	assert.True(t, ok)

	_, err = EvaluateSimpleExpr("garbage", nil, nil)
	assert.Error(t, err)
}

func TestEvaluateSimpleExprLoopIteration(t *testing.T) {
	upstreams := map[string]*NodeResult{
		"fixer": {
			Status:         StatusSucceeded,
			ExitCode:       0,
			LoopIterations: map[string]int{"fix_loop": 2, "step_loop": 4},
		},
		"root": {Status: StatusSucceeded, ExitCode: 0},
	}

	tests := []struct {
		name    string
		expr    string
		want    bool
		wantErr bool
	}{
		{"loop iteration equality", "nodes.fixer.loop_iteration.fix_loop == 2", true, false},
		{"loop iteration mismatch", "nodes.fixer.loop_iteration.fix_loop == 1", false, false},
		{"loop iteration numeric compare", "nodes.fixer.loop_iteration.fix_loop >= 2", true, false},
		{"second loop id", "nodes.fixer.loop_iteration.step_loop < 5", true, false},
		{"unknown loop id", "nodes.fixer.loop_iteration.other_loop == 1", false, true},
		{"node without loop snapshot", "nodes.root.loop_iteration.fix_loop == 1", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EvaluateSimpleExpr(tt.expr, upstreams, nil)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInterpolate(t *testing.T) {
	vars := map[string]string{
		"session_id":              "sess-1",
		"tmp_dir":                 "/tmp/work",
		"nodes.build.output_file": "build.log",
	}
	resolve := func(key string) (string, bool) {
		v, ok := vars[key]
		return v, ok
	}

	assert.Equal(t,
		"see /tmp/work/build.log in sess-1",
		Interpolate("see ${tmp_dir}/${nodes.build.output_file} in ${session_id}", resolve),
	)
	// Unknown keys pass through verbatim.
	assert.Equal(t,
		"keep ${HOME} intact",
		Interpolate("keep ${HOME} intact", resolve),
	)
	// Unterminated placeholder passes through.
	assert.Equal(t, "broken ${session_id", Interpolate("broken ${session_id", resolve))
}
