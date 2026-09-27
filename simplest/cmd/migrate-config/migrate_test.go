package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestMigrateConfigBytes_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		inputYAML     string
		expectChanged bool
		validate      func(t *testing.T, outputYAML []byte)
	}{
		{
			name: "legacy config missing type, reasoningEffort migration, and image limits",
			inputYAML: `# Configuration file comment
providers:
  openai:
    api: openai-compat
models:
  # Model comment
  - id: gpt-4o
    provider: openai
    reasoningEffort:
      - low
      - high
    input:
      - text
      - image
`,
			expectChanged: true,
			validate: func(t *testing.T, outputYAML []byte) {
				t.Helper()
				outStr := string(outputYAML)
				assert.Contains(t, outStr, "# Configuration file comment")
				assert.Contains(t, outStr, "# Model comment")

				var parsed struct {
					Models []struct {
						ID               string             `yaml:"id"`
						Type             string             `yaml:"type"`
						ThinkingLevelMap map[string]*string `yaml:"thinkingLevelMap"`
						InputLimits      struct {
							Images struct {
								Resize struct {
									MaxWidth  int   `yaml:"maxWidth"`
									MaxHeight int   `yaml:"maxHeight"`
									MaxBytes  int64 `yaml:"maxBytes"`
								} `yaml:"resize"`
							} `yaml:"images"`
						} `yaml:"inputLimits"`
					} `yaml:"models"`
				}
				err := yaml.Unmarshal(outputYAML, &parsed)
				require.NoError(t, err)
				require.Len(t, parsed.Models, 1)
				m := parsed.Models[0]
				assert.Equal(t, "chat", m.Type)
				require.NotNil(t, m.ThinkingLevelMap)
				// low and high should have non-nil values
				require.NotNil(t, m.ThinkingLevelMap["low"])
				assert.Equal(t, "low", *m.ThinkingLevelMap["low"])
				require.NotNil(t, m.ThinkingLevelMap["high"])
				assert.Equal(t, "high", *m.ThinkingLevelMap["high"])
				// minimal, medium, xhigh, max should be nil (explicit null)
				assert.Nil(t, m.ThinkingLevelMap["minimal"])
				assert.Nil(t, m.ThinkingLevelMap["medium"])
				assert.Nil(t, m.ThinkingLevelMap["xhigh"])
				assert.Nil(t, m.ThinkingLevelMap["max"])

				assert.Equal(t, 2000, m.InputLimits.Images.Resize.MaxWidth)
				assert.Equal(t, 2000, m.InputLimits.Images.Resize.MaxHeight)
				assert.Equal(t, int64(4718592), m.InputLimits.Images.Resize.MaxBytes)
			},
		},
		{
			name: "already migrated config is idempotent",
			inputYAML: `providers:
  openai:
    api: openai-compat
models:
  - id: gpt-4o
    type: chat
    provider: openai
    thinkingLevelMap:
      low: low
      high: high
    input:
      - text
      - image
    inputLimits:
      images:
        resize:
          maxWidth: 2000
          maxHeight: 2000
          maxBytes: 4718592
`,
			expectChanged: false,
			validate: func(t *testing.T, outputYAML []byte) {
				t.Helper()
				assert.NotEmpty(t, outputYAML)
			},
		},
		{
			name: "snake_case reasoning_effort migration",
			inputYAML: `models:
  - id: custom-model
    provider: test
    reasoning_effort:
      - medium
`,
			expectChanged: true,
			validate: func(t *testing.T, outputYAML []byte) {
				t.Helper()
				var parsed struct {
					Models []struct {
						Type             string             `yaml:"type"`
						ThinkingLevelMap map[string]*string `yaml:"thinkingLevelMap"`
					} `yaml:"models"`
				}
				err := yaml.Unmarshal(outputYAML, &parsed)
				require.NoError(t, err)
				require.Len(t, parsed.Models, 1)
				assert.Equal(t, "chat", parsed.Models[0].Type)
				require.NotNil(t, parsed.Models[0].ThinkingLevelMap["medium"])
				assert.Equal(t, "medium", *parsed.Models[0].ThinkingLevelMap["medium"])
				assert.Nil(t, parsed.Models[0].ThinkingLevelMap["low"])
			},
		},
		{
			name: "no models section produces no changes",
			inputYAML: `providers:
  openai:
    api: openai-compat
`,
			expectChanged: false,
			validate: func(t *testing.T, outputYAML []byte) {
				t.Helper()
				assert.Equal(t, "providers:\n  openai:\n    api: openai-compat\n", string(outputYAML))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, changed, err := MigrateConfigBytes([]byte(tt.inputYAML))
			require.NoError(t, err)
			assert.Equal(t, tt.expectChanged, changed)
			if tt.validate != nil {
				tt.validate(t, res)
			}
		})
	}
}

func TestMigrateConfigFile_DryRun(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	originalContent := `models:
  - id: model-a
    provider: openai
`
	err := os.WriteFile(configPath, []byte(originalContent), 0o600)
	require.NoError(t, err)

	changed, err := MigrateConfigFile(configPath, true)
	require.NoError(t, err)
	assert.True(t, changed)

	// File on disk must remain strictly unchanged in dry-run mode
	diskData, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, originalContent, string(diskData))
}

func TestMigrateConfigFile_AtomicWriteSuccess(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	originalContent := `models:
  - id: model-a
    provider: openai
`
	err := os.WriteFile(configPath, []byte(originalContent), 0o600)
	require.NoError(t, err)

	changed, err := MigrateConfigFile(configPath, false)
	require.NoError(t, err)
	assert.True(t, changed)

	diskData, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(diskData), "type: chat")

	// Ensure no temp files remained in tempDir
	entries, err := os.ReadDir(tempDir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".tmp-config-"), "residual temp file found: %s", entry.Name())
	}
}

func TestMigrateConfigFile_AtomicFailurePreservesOriginalAndCleansUp(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	subDir := filepath.Join(tempDir, "readonly_dir")
	err := os.Mkdir(subDir, 0o700)
	require.NoError(t, err)

	configPath := filepath.Join(subDir, "config.yaml")
	originalContent := `models:
  - id: model-a
    provider: openai
`
	err = os.WriteFile(configPath, []byte(originalContent), 0o600)
	require.NoError(t, err)

	// Make directory read-only to cause os.CreateTemp to fail when attempting to write
	err = os.Chmod(subDir, 0o500)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Chmod(subDir, 0o700)
	})

	changed, err := MigrateConfigFile(configPath, false)
	require.Error(t, err)
	assert.False(t, changed)

	// Restore permission to read directory and verify
	err = os.Chmod(subDir, 0o700)
	require.NoError(t, err)

	// Original file must remain intact
	diskData, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, originalContent, string(diskData))

	// No leftover temp files
	entries, err := os.ReadDir(subDir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".tmp-config-"), "residual temp file found: %s", entry.Name())
	}
}
