package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToAnySlice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []string
		expected []any
	}{
		{
			name:     "nil slice",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty slice",
			input:    []string{},
			expected: nil,
		},
		{
			name:     "strings slice",
			input:    []string{"a", "b", "c"},
			expected: []any{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res := ToAnySlice(tt.input)
			if tt.expected == nil {
				assert.Nil(t, res)
			} else {
				require.NotNil(t, res)
				assert.Equal(t, tt.expected, res)
			}
		})
	}
}

func TestSanitizeMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    map[string]any
		expected map[string]any
	}{
		{
			name:     "nil map",
			input:    nil,
			expected: nil,
		},
		{
			name: "typed string slice converted to any slice",
			input: map[string]any{
				"artifact_files": []string{"/tmp/plan.md", "/tmp/feedback.md"},
				"node_id":        "intend_agent",
				"step_index":     5,
				"is_done":        true,
			},
			expected: map[string]any{
				"artifact_files": []any{"/tmp/plan.md", "/tmp/feedback.md"},
				"node_id":        "intend_agent",
				"step_index":     5,
				"is_done":        true,
			},
		},
		{
			name: "nested map and slices",
			input: map[string]any{
				"nested": map[string]any{
					"tags": []string{"alpha", "beta"},
				},
				"string_map": map[string]string{
					"key1": "val1",
				},
			},
			expected: map[string]any{
				"nested": map[string]any{
					"tags": []any{"alpha", "beta"},
				},
				"string_map": map[string]any{
					"key1": "val1",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res := SanitizeMetadata(tt.input)
			if tt.expected == nil {
				assert.Nil(t, res)
			} else {
				require.NotNil(t, res)
				assert.Equal(t, tt.expected, res)
			}
		})
	}
}
