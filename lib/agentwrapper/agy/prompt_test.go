package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

func TestEnsureWorkspaceTrusted(t *testing.T) {
	tempHome := t.TempDir()

	oldHome := os.Getenv("HOME")
	err := os.Setenv("HOME", tempHome)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Setenv("HOME", oldHome)
	})

	cliDir := filepath.Join(tempHome, ".gemini", "antigravity-cli")
	err = os.MkdirAll(cliDir, 0755)
	require.NoError(t, err)

	settingsPath := filepath.Join(cliDir, "settings.json")

	// 1. Write an initial settings.json with a trusted workspace
	initialSettings := `{
  "model": "test-model",
  "trustedWorkspaces": [
    "/some/trusted/path"
  ]
}`
	err = os.WriteFile(settingsPath, []byte(initialSettings), 0644)
	require.NoError(t, err)

	// 2. Checking the already trusted path should succeed without modifying the file
	err = ensureWorkspaceTrusted("/some/trusted/path")
	require.NoError(t, err)

	// 3. Checking an untrusted path should add it to settings.json and succeed
	untrustedPath, err := filepath.Abs(".")
	require.NoError(t, err)
	untrustedPath = filepath.Clean(untrustedPath)

	err = ensureWorkspaceTrusted(untrustedPath)
	require.NoError(t, err)

	// 4. Verify settings.json was updated and contains the new path while preserving other keys
	updatedData, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var config struct {
		Model             string   `json:"model"`
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	err = json.Unmarshal(updatedData, &config)
	require.NoError(t, err)

	assert.Equal(t, "test-model", config.Model)
	assert.Contains(t, config.TrustedWorkspaces, "/some/trusted/path")
	assert.Contains(t, config.TrustedWorkspaces, untrustedPath)
}

func TestSplitModelVariant(t *testing.T) {
	tests := []struct {
		input     string
		wantModel string
		wantVar   string
	}{
		{"gemini-3.7-flash-low", "gemini-3.7-flash", "low"},
		{"gemini-3.7-flash-medium", "gemini-3.7-flash", "medium"},
		{"gemini-3.7-flash-high", "gemini-3.7-flash", "high"},
		{"gemini-3.7-flash/low", "gemini-3.7-flash", "low"},
		{"gemini-3.7-flash", "gemini-3.7-flash", ""},
		{"claude-3-7-sonnet-high", "claude-3-7-sonnet", "high"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			base, variant := SplitModelVariant(tt.input)
			assert.Equal(t, tt.wantModel, base)
			assert.Equal(t, tt.wantVar, variant)
		})
	}
}

func TestBuildPromptArgv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		runDir   string
		prompt   string
		opts     types.PromptOptions
		wantArgv []string
	}{
		{
			name:   "basic options without AddTmpToDir",
			runDir: "/workspace",
			prompt: "hello world",
			opts: types.PromptOptions{
				SessionID: "sess-123",
				Model:     "gemini-3.7-flash",
			},
			wantArgv: []string{
				"agy", "--dangerously-skip-permissions", "--output-format", "stream-json",
				"--add-dir", "/workspace",
				"--conversation=sess-123",
				"--model", "gemini-3.7-flash",
				"--print", "hello world",
			},
		},
		{
			name:   "with AddTmpToDir enabled",
			runDir: "/workspace",
			prompt: "hello world",
			opts: types.PromptOptions{
				SessionID:   "sess-123",
				Model:       "gemini-3.7-flash-high",
				AddTmpToDir: true,
			},
			wantArgv: []string{
				"agy", "--dangerously-skip-permissions", "--output-format", "stream-json",
				"--add-dir", "/workspace",
				"--add-dir", "/tmp",
				"--conversation=sess-123",
				"--model", "gemini-3.7-flash",
				"--effort", "high",
				"--print", "hello world",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			argv := buildPromptArgv(tt.runDir, tt.prompt, tt.opts)
			assert.Equal(t, tt.wantArgv, argv)
		})
	}
}
