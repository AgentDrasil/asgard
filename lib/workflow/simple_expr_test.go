package workflow

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

func TestInterpolate(t *testing.T) {
	vars := map[string]string{
		"session_id":              "sess-1",
		"artifacts_dir":           "/tmp/artifacts",
		"nodes.build.output_file": "build.log",
	}
	resolve := func(key string) (string, bool) {
		v, ok := vars[key]
		return v, ok
	}

	assert.Equal(t,
		"see /tmp/artifacts/build.log in sess-1",
		Interpolate("see ${artifacts_dir}/${nodes.build.output_file} in ${session_id}", resolve),
	)
	// Unknown keys pass through verbatim.
	assert.Equal(t,
		"keep ${HOME} intact",
		Interpolate("keep ${HOME} intact", resolve),
	)
	// Unterminated placeholder passes through.
	assert.Equal(t, "broken ${session_id", Interpolate("broken ${session_id", resolve))
}
