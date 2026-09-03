package bwrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func TestBuildSystemPrompt(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	agentsMDPath := filepath.Join(tmpDir, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsMDPath, []byte("# Custom Instructions\n\nDo stuff."), 0644))

	tests := []struct {
		name           string
		cli            string
		agentsMDPath   string
		hasTeam        bool
		langRules      string
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:         "agy with AGENTS.md, team and langRules",
			cli:          "agy",
			agentsMDPath: agentsMDPath,
			hasTeam:      true,
			langRules:    "## Language Preferences\n\n- Responses/Conversations: Chinese (Simplified)",
			wantContains: []string{
				"## Language Preferences",
				"Responses/Conversations: Chinese (Simplified)",
				"/bin/ask-user <question>",
				"call-peer",
				"# Custom Instructions",
				"Do stuff.",
			},
		},
		{
			name:         "agy with AGENTS.md and team",
			cli:          "agy",
			agentsMDPath: agentsMDPath,
			hasTeam:      true,
			langRules:    "",
			wantContains: []string{
				"/bin/ask-user <question>",
				"call-peer",
				"# Custom Instructions",
				"Do stuff.",
			},
		},
		{
			name:         "agy with AGENTS.md and no team",
			cli:          "agy",
			agentsMDPath: agentsMDPath,
			hasTeam:      false,
			langRules:    "",
			wantContains: []string{
				"/bin/ask-user <question>",
				"# Custom Instructions",
				"Do stuff.",
			},
			wantNotContain: []string{
				"call-peer",
			},
		},
		{
			name:         "agy without AGENTS.md with team",
			cli:          "agy",
			agentsMDPath: "",
			hasTeam:      true,
			langRules:    "",
			wantContains: []string{
				"/bin/ask-user <question>",
				"call-peer",
			},
		},
		{
			name:         "agy without AGENTS.md without team",
			cli:          "agy",
			agentsMDPath: "",
			hasTeam:      false,
			langRules:    "",
			wantContains: []string{
				"/bin/ask-user <question>",
			},
			wantNotContain: []string{
				"call-peer",
			},
		},
		{
			name:         "opencode with AGENTS.md with team and langRules",
			cli:          "opencode",
			agentsMDPath: agentsMDPath,
			hasTeam:      true,
			langRules:    "## Language Preferences\n\n- Responses/Conversations: English (US)",
			wantContains: []string{
				"## Language Preferences",
				"Responses/Conversations: English (US)",
				"/bin/ask-user <question>",
				"call-peer",
				"# Custom Instructions",
				"Do stuff.",
			},
		},
		{
			name:         "opencode with AGENTS.md with team",
			cli:          "opencode",
			agentsMDPath: agentsMDPath,
			hasTeam:      true,
			langRules:    "",
			wantContains: []string{
				"/bin/ask-user <question>",
				"call-peer",
				"# Custom Instructions",
				"Do stuff.",
			},
		},
		{
			name:         "opencode with AGENTS.md without team",
			cli:          "opencode",
			agentsMDPath: agentsMDPath,
			hasTeam:      false,
			langRules:    "",
			wantContains: []string{
				"/bin/ask-user <question>",
				"# Custom Instructions",
				"Do stuff.",
			},
			wantNotContain: []string{
				"call-peer",
			},
		},
		{
			name:         "opencode without AGENTS.md without team",
			cli:          "opencode",
			agentsMDPath: "",
			hasTeam:      false,
			langRules:    "",
			wantContains: []string{
				"/bin/ask-user <question>",
			},
			wantNotContain: []string{
				"call-peer",
			},
		},
		{
			name:         "unknown CLI without AGENTS.md returns empty",
			cli:          "unknown",
			agentsMDPath: "",
			hasTeam:      true,
			langRules:    "",
			wantContains: nil,
		},
		{
			name:         "unknown CLI with langRules returns langRules",
			cli:          "unknown",
			agentsMDPath: "",
			hasTeam:      true,
			langRules:    "## Language Preferences\n\n- Responses/Conversations: English (US)",
			wantContains: []string{
				"## Language Preferences",
				"Responses/Conversations: English (US)",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildSystemPrompt(tt.cli, tt.agentsMDPath, tt.hasTeam, tt.langRules)
			require.NoError(t, err)

			for _, want := range tt.wantContains {
				assert.Contains(t, got, want)
			}
			for _, dontWant := range tt.wantNotContain {
				assert.NotContains(t, got, dontWant)
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

	cfg := &agentspec.AgentConfig{
		ID:          "test-agent",
		Name:        "Test Agent",
		Description: "A test agent",
		Team:        "test-team",
		RunDirs:     []string{runDir},
		MountDirs: agentspec.MountConfig{
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

	// Test case 1: agy CLITarget with session, team and langRules
	targetAgy := agentspec.CLITarget{
		CLI:   "agy",
		Model: "some-model",
	}

	langRules := "## Language Preferences\n\n- Responses/Conversations: Chinese (Simplified)"
	configPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("api_key: secret"), 0644))

	args, err := buildArgsForAgent(cfg, agentPath, targetAgy, "some prompt", optional.Some("my-session-id"), runDir, "test-sock-dir", "test-chat", langRules, configPath)
	require.NoError(t, err)

	argStr := strings.Join(args, " ")

	assert.Contains(t, argStr, "--ro-bind /dev/null "+configPath)

	sshDir := filepath.Join(home, ".ssh")
	assert.Contains(t, argStr, "--tmpfs "+sshDir)

	// Verify required bwrap components
	expectedTmpDir := filepath.Join(home, "tmp", "test-chat")
	assert.Contains(t, argStr, "--bind "+expectedTmpDir+" /tmp")
	expectedSessionDir := filepath.Join(home, "data", "test-chat")
	assert.Contains(t, argStr, "--bind "+expectedSessionDir+" /session")
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

	// Verify the generated prompt file contains language rules, tool instructions and the AGENTS.md content
	promptContent, readErr := os.ReadFile(filepath.Join(expectedTmpDir, ".asgard_system_prompt"))
	require.NoError(t, readErr)
	assert.Contains(t, string(promptContent), "## Language Preferences")
	assert.Contains(t, string(promptContent), "Chinese (Simplified)")
	assert.Contains(t, string(promptContent), "/bin/ask-user <question>")
	assert.Contains(t, string(promptContent), "/bin/call-peer <agent-id> <message>")
	assert.Contains(t, string(promptContent), "agents instructions")

	// Verify ending command structure with --session and --prompt
	expectedEnd := "-- aw agy --model some-model --add-tmp-to-dir --session my-session-id --prompt some prompt"
	assert.True(t, strings.HasSuffix(argStr, expectedEnd), "expected suffix %q, got: %s", expectedEnd, argStr)

	// Test case 2: opencode CLITarget without team
	cfgNoTeam := &agentspec.AgentConfig{
		ID:          "test-agent-no-team",
		Name:        "Test Agent No Team",
		Description: "A test agent without team",
		RunDirs:     []string{runDir},
	}

	targetOpencode := agentspec.CLITarget{
		CLI:   "opencode",
		Model: "another-model",
	}

	argsOpencode, err := buildArgsForAgent(cfgNoTeam, agentPath, targetOpencode, "run", optional.None[string](), runDir, "test-sock-dir", "", "", "")
	require.NoError(t, err)

	argStrOpencode := strings.Join(argsOpencode, " ")

	expectedDefaultTmpDir := filepath.Join(home, "tmp", "default")
	assert.Contains(t, argStrOpencode, "--bind "+expectedDefaultTmpDir+" /tmp")
	expectedDefaultSessionDir := filepath.Join(home, "data", "default")
	assert.Contains(t, argStrOpencode, "--bind "+expectedDefaultSessionDir+" /session")

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

	// Verify the generated prompt file contains ask-user instructions but NOT call-peer
	opencodePromptContent, readErr := os.ReadFile(filepath.Join(expectedDefaultTmpDir, ".asgard_system_prompt"))
	require.NoError(t, readErr)
	assert.Contains(t, string(opencodePromptContent), "/bin/ask-user <question>")
	assert.NotContains(t, string(opencodePromptContent), "call-peer")
	assert.Contains(t, string(opencodePromptContent), "agents instructions")

	expectedEndOpencode := "-- aw opencode --model another-model --prompt run"
	assert.True(t, strings.HasSuffix(argStrOpencode, expectedEndOpencode), "expected suffix %q, got: %s", expectedEndOpencode, argStrOpencode)
}

func TestTimezoneInheritance(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("TZ", "Asia/Tokyo")

	args, err := appendBaseSandboxArgs([]string{}, tmpDir, "test-tz")
	require.NoError(t, err)

	argStr := strings.Join(args, " ")
	assert.Contains(t, argStr, "--setenv TZ Asia/Tokyo")

	if _, err := os.Stat("/usr/share/zoneinfo"); err == nil {
		assert.Contains(t, argStr, "--ro-bind /usr/share/zoneinfo /usr/share/zoneinfo")
	}
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

	runcfgRunDir := filepath.Join(tmpDir, "rundir")
	if err := os.MkdirAll(runcfgRunDir, 0755); err != nil {
		t.Fatalf("failed to create rundir: %v", err)
	}

	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("api_key: secret"), 0644); err != nil {
		t.Fatalf("failed to create config.yaml: %v", err)
	}

	cmd, err := CommandForCommandExec(runcfgRunDir, "test-sock-dir", "test-chat", configPath)
	if err != nil {
		t.Fatalf("CommandForCommandExec error: %v", err)
	}

	argStr := strings.Join(cmd.Args, " ")

	expectedTmpDir := filepath.Join(tmpDir, "tmp", "test-chat")
	if !strings.Contains(argStr, "--die-with-parent") {
		t.Errorf("expected '--die-with-parent' in args, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--bind "+expectedTmpDir+" /tmp") {
		t.Errorf("expected '--bind %s /tmp' in args, got: %s", expectedTmpDir, argStr)
	}
	expectedSessionDir := filepath.Join(tmpDir, "data", "test-chat")
	if !strings.Contains(argStr, "--bind "+expectedSessionDir+" /session") {
		t.Errorf("expected '--bind %s /session' in args, got: %s", expectedSessionDir, argStr)
	}
	if !strings.Contains(argStr, "--bind "+tmpDir+" "+tmpDir) {
		t.Errorf("expected home bind mount, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--ro-bind /dev/null "+configPath) {
		t.Errorf("expected config masking ro-bind /dev/null, got: %s", argStr)
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
	// runDir is inside home, so it must NOT be bind-mounted separately (a
	// nested bind would give it a different st_dev and break hard links).
	if strings.Contains(argStr, "--bind "+runcfgRunDir+" "+runcfgRunDir) {
		t.Errorf("expected no separate runDir bind mount (inside home), got: %s", argStr)
	}
	if !strings.Contains(argStr, "--chdir "+runcfgRunDir) {
		t.Errorf("expected '--chdir %s' in args, got: %s", runcfgRunDir, argStr)
	}

	expectedEnd := "-- /bin/fakebashd"
	if !strings.HasSuffix(argStr, expectedEnd) {
		t.Errorf("expected suffix %q, got: %s", expectedEnd, argStr)
	}
}
