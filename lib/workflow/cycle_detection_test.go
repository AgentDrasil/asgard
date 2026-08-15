package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCycleDetection(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "two node cycle",
			yaml: `
name: cyclic
nodes:
  - id: a
    type: command
    command: "true"
    depends:
      - node: b
  - id: b
    type: command
    command: "true"
    depends:
      - node: a
`,
			wantErr: "circular dependency detected",
		},
		{
			name: "three node cycle with path",
			yaml: `
name: cyclic3
nodes:
  - id: a
    type: command
    command: "true"
    depends:
      - node: c
  - id: b
    type: command
    command: "true"
    depends:
      - node: a
  - id: c
    type: command
    command: "true"
    depends:
      - node: b
`,
			wantErr: "circular dependency detected",
		},
		{
			name: "self dependency",
			yaml: `
name: selfdep
nodes:
  - id: a
    type: command
    command: "true"
    depends:
      - node: a
`,
			wantErr: "circular dependency detected: a -> a",
		},
		{
			name: "valid dag passes",
			yaml: `
name: valid
nodes:
  - id: a
    type: command
    command: "true"
  - id: b
    type: command
    command: "true"
    depends:
      - node: a
  - id: c
    type: command
    command: "true"
    depends:
      - node: a
      - node: b
`,
		},
		{
			name: "unknown dependency",
			yaml: `
name: unknowndep
nodes:
  - id: a
    type: command
    command: "true"
    depends:
      - node: ghost
`,
			wantErr: "depends on unknown node",
		},
		{
			name: "duplicate node id",
			yaml: `
name: dup
nodes:
  - id: a
    type: command
    command: "true"
  - id: a
    type: command
    command: "true"
`,
			wantErr: "duplicate node id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseDefinition([]byte(tt.yaml))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestCycleErrorMessageContainsPath(t *testing.T) {
	yaml := `
name: cyclic3
nodes:
  - id: x
    type: command
    command: "true"
    depends:
      - node: z
  - id: y
    type: command
    command: "true"
    depends:
      - node: x
  - id: z
    type: command
    command: "true"
    depends:
      - node: y
`
	_, err := ParseDefinition([]byte(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency detected: ")
	// The message must contain an explicit cycle path ending where it started.
	assert.Contains(t, err.Error(), "->")
}
