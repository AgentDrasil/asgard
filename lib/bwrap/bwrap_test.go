package bwrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/agents"
)

func TestBuildSystemPrompt(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	agentsMDPath := filepath.Join(tmpDir, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsMDPath, []byte("# Custom Instructions\n\nDo stuff."), 0644))

	tests := []struct {
		name         string
		cli          string
		agentsMDPath string
		wantContains []string
	}{
		{
			name:         "agy with AGENTS.md",
			cli:          "agy",
			agentsMDPath: agentsMDPath,
			wantContains: []string{
				"Forget the `ask_question` tool",
				"# Custom Instructions",
				"Do stuff.",
			},
		},
		{
			name:         "agy without AGENTS.md",
			cli:          "agy",
			agentsMDPath: "",
			wantContains: []string{
				"Forget the `ask_question` tool",
			},
		},
		{
			name:         "opencode with AGENTS.md",
			cli:          "opencode",
			agentsMDPath: agentsMDPath,
			wantContains: []string{
				"Forget the `question` tool",
				"# Custom Instructions",
				"Do stuff.",
			},
		},
		{
			name:         "opencode without AGENTS.md",
			cli:          "opencode",
			agentsMDPath: "",
			wantContains: []string{
				"Forget the `question` tool",
			},
		},
		{
			name:         "unknown CLI without AGENTS.md returns empty",
			cli:          "unknown",
			agentsMDPath: "",
			wantContains: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildSystemPrompt(tt.cli, tt.agentsMDPath)
			require.NoError(t, err)

			for _, want := range tt.wantContains {
				assert.Contains(t, got, want)
			}
			if tt.wantContains == nil {
				assert.Empty(t, got)
			}
		})
	}
}

func TestBuildArgs(t *testing.T) {
	tmpDir := t.TempDir()

	runDir := filepath.Join(tmpDir, "rundir")
	require.NoError(t, os.MkdirAll(runDir, 0755))

	roDir := filepath.Join(tmpDir, "rodir")
	require.NoError(t, os.MkdirAll(roDir, 0755))

	rwDir := filepath.Join(tmpDir, "rwdir")
	require.NoError(t, os.MkdirAll(rwDir, 0755))

	cfg := &agents.AgentConfig{
		ID:          "test-agent",
		Name:        "Test Agent",
		Description: "A test agent",
		RunDirs:     []string{runDir},
		MountDirs: agents.MountConfig{
			ReadOnly:  []string{roDir},
			ReadWrite: []string{rwDir},
		},
	}

	t.Setenv("HOME", tmpDir)
	home := tmpDir

	// Create directories that buildArgsForAgent expects to exist under HOME
	for _, subDir := range []string{".gemini", ".cache", ".config", ".local", ".ssh"} {
		require.NoError(t, os.MkdirAll(filepath.Join(home, subDir), 0755))
	}

	// Create simulated agent configuration directory with AGENTS.md and skills
	agentPath := filepath.Join(tmpDir, "test-agent-dir")
	require.NoError(t, os.MkdirAll(filepath.Join(agentPath, "skills"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(agentPath, "AGENTS.md"), []byte("agents instructions"), 0644))

	// Test case 1: agy CLITarget with session
	targetAgy := agents.CLITarget{
		CLI:   "agy",
		Model: "some-model",
	}

	args, err := buildArgsForAgent(cfg, agentPath, targetAgy, "some prompt", optional.Some("my-session-id"), runDir, "test-sock-dir", "test-chat")
	require.NoError(t, err)

	argStr := strings.Join(args, " ")

	sshDir := filepath.Join(home, ".ssh")
	assert.Contains(t, argStr, "--tmpfs "+sshDir)

	// Verify required bwrap components
	expectedTmpDir := filepath.Join(home, "tmp", "test-chat")
	assert.Contains(t, argStr, "--bind "+expectedTmpDir+" /tmp")
	assert.Contains(t, argStr, "--setenv HOME "+home)
	assert.Contains(t, argStr, "--bind "+runDir+" "+runDir)
	assert.Contains(t, argStr, "--chdir "+runDir)
	assert.Contains(t, argStr, "--ro-bind "+roDir+" "+roDir)
	assert.Contains(t, argStr, "--bind "+rwDir+" "+rwDir)

	// Verify agy specific mounts
	geminiDir := filepath.Join(home, ".gemini")
	assert.Contains(t, argStr, "--bind "+geminiDir+" "+geminiDir)

	// Verify agy system prompt: a generated file is mounted at GEMINI.md (not the raw AGENTS.md)
	expectedAgyDest := filepath.Join(home, ".gemini", "GEMINI.md")
	expectedAgySkills := filepath.Join(home, ".gemini", "antigravity-cli", "skills")
	assert.Contains(t, argStr, "--ro-bind "+expectedTmpDir+"/.asgard_system_prompt "+expectedAgyDest)
	assert.Contains(t, argStr, "--ro-bind "+filepath.Join(agentPath, "skills")+" "+expectedAgySkills)

	// Verify the generated prompt file contains our instructions and the AGENTS.md content
	promptContent, readErr := os.ReadFile(filepath.Join(expectedTmpDir, ".asgard_system_prompt"))
	require.NoError(t, readErr)
	assert.Contains(t, string(promptContent), "Forget the `ask_question` tool")
	assert.Contains(t, string(promptContent), "agents instructions")

	// Verify ending command structure with --session and --prompt
	expectedEnd := "-- aw agy --model some-model --session my-session-id --prompt some prompt"
	assert.True(t, strings.HasSuffix(argStr, expectedEnd), "expected suffix %q, got: %s", expectedEnd, argStr)

	// Test case 2: opencode CLITarget without session (None)
	targetOpencode := agents.CLITarget{
		CLI:   "opencode",
		Model: "another-model",
	}

	argsOpencode, err := buildArgsForAgent(cfg, agentPath, targetOpencode, "run", optional.None[string](), runDir, "test-sock-dir", "")
	require.NoError(t, err)

	argStrOpencode := strings.Join(argsOpencode, " ")

	expectedDefaultTmpDir := filepath.Join(home, "tmp", "default")
	assert.Contains(t, argStrOpencode, "--bind "+expectedDefaultTmpDir+" /tmp")

	// Verify opencode specific mounts
	cacheDir := filepath.Join(home, ".cache")
	configDir := filepath.Join(home, ".config")
	localDir := filepath.Join(home, ".local")
	assert.Contains(t, argStrOpencode, "--bind "+cacheDir+" "+cacheDir)
	assert.Contains(t, argStrOpencode, "--bind "+configDir+" "+configDir)
	assert.Contains(t, argStrOpencode, "--bind "+localDir+" "+localDir)
	assert.Contains(t, argStrOpencode, "--chdir "+runDir)

	// Verify opencode system prompt: a generated file is mounted at AGENTS.md (not the raw AGENTS.md)
	expectedOpencodeDest := filepath.Join(home, ".config", "opencode", "AGENTS.md")
	expectedOpencodeSkills := filepath.Join(home, ".config", "opencode", "skills")
	assert.Contains(t, argStrOpencode, "--ro-bind "+expectedDefaultTmpDir+"/.asgard_system_prompt "+expectedOpencodeDest)
	assert.Contains(t, argStrOpencode, "--ro-bind "+filepath.Join(agentPath, "skills")+" "+expectedOpencodeSkills)

	// Verify the generated prompt file contains our instructions and the AGENTS.md content
	opencodePromptContent, readErr := os.ReadFile(filepath.Join(expectedDefaultTmpDir, ".asgard_system_prompt"))
	require.NoError(t, readErr)
	assert.Contains(t, string(opencodePromptContent), "Forget the `question` tool")
	assert.Contains(t, string(opencodePromptContent), "agents instructions")

	expectedEndOpencode := "-- aw opencode --model another-model --prompt run"
	assert.True(t, strings.HasSuffix(argStrOpencode, expectedEndOpencode), "expected suffix %q, got: %s", expectedEndOpencode, argStrOpencode)
}

func TestCommandForCommandExec(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Create simulated auth dirs to make sure they get masked
	agyAuthDir := filepath.Join(tmpDir, ".gemini")
	if err := os.MkdirAll(agyAuthDir, 0755); err != nil {
		t.Fatalf("failed to create agy auth dir: %v", err)
	}
	opencodeAuthDir := filepath.Join(tmpDir, ".local", "share", "opencode")
	if err := os.MkdirAll(opencodeAuthDir, 0755); err != nil {
		t.Fatalf("failed to create opencode auth dir: %v", err)
	}
	sshDir := filepath.Join(tmpDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0755); err != nil {
		t.Fatalf("failed to create ssh dir: %v", err)
	}

	runDir := filepath.Join(tmpDir, "rundir")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("failed to create rundir: %v", err)
	}

	cmd, err := CommandForCommandExec(runDir, "test-sock-dir", "test-chat")
	if err != nil {
		t.Fatalf("CommandForCommandExec error: %v", err)
	}

	argStr := strings.Join(cmd.Args, " ")

	expectedTmpDir := filepath.Join(tmpDir, "tmp", "test-chat")
	if !strings.Contains(argStr, "--bind "+expectedTmpDir+" /tmp") {
		t.Errorf("expected '--bind %s /tmp' in args, got: %s", expectedTmpDir, argStr)
	}
	if !strings.Contains(argStr, "--bind "+tmpDir+" "+tmpDir) {
		t.Errorf("expected home bind mount, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--tmpfs "+agyAuthDir) {
		t.Errorf("expected agy auth dir masking, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--tmpfs "+opencodeAuthDir) {
		t.Errorf("expected opencode auth dir masking, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--tmpfs "+sshDir) {
		t.Errorf("expected ssh dir masking, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--bind "+runDir+" "+runDir) {
		t.Errorf("expected runDir bind mount, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--chdir "+runDir) {
		t.Errorf("expected '--chdir %s' in args, got: %s", runDir, argStr)
	}

	expectedEnd := "-- /bin/fakebashd"
	if !strings.HasSuffix(argStr, expectedEnd) {
		t.Errorf("expected suffix %q, got: %s", expectedEnd, argStr)
	}
}
