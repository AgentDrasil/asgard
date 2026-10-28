package bwrap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

func TestBuildContractBody(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	agentsMDPath := filepath.Join(tmpDir, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsMDPath, []byte("# Custom Instructions\n\nDo stuff."), 0644))

	tests := []struct {
		name           string
		agentsMDPath   string
		langRules      string
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:         "AGENTS.md with langRules",
			agentsMDPath: agentsMDPath,
			langRules:    "## Language Preferences\n\n- Responses/Conversations: Chinese (Simplified)",
			wantContains: []string{
				"## Language Preferences",
				"Responses/Conversations: Chinese (Simplified)",
				"# Custom Instructions",
				"Do stuff.",
			},
			// CLI-specific protocol headers are composed sandbox-side, never in the contract body.
			wantNotContain: []string{
				"/bin/ask-user",
				"call-peer",
			},
		},
		{
			name:         "AGENTS.md without langRules",
			agentsMDPath: agentsMDPath,
			langRules:    "",
			wantContains: []string{
				"# Custom Instructions",
				"Do stuff.",
			},
		},
		{
			name:         "langRules only",
			agentsMDPath: "",
			langRules:    "## Language Preferences\n\n- Responses/Conversations: English (US)",
			wantContains: []string{
				"## Language Preferences",
				"Responses/Conversations: English (US)",
			},
		},
		{
			name:           "missing AGENTS.md is tolerated",
			agentsMDPath:   filepath.Join(tmpDir, "absent.md"),
			langRules:      "",
			wantContains:   nil,
			wantNotContain: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildContractBody(tt.agentsMDPath, tt.langRules)
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

func TestWriteContractFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	agentsMDPath := filepath.Join(tmpDir, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsMDPath, []byte("agents instructions"), 0644))

	cfg := &agentspec.AgentConfig{
		ID:   "test-agent",
		Name: "Test Agent",
		Team: "test-team",
	}

	path, err := writeContractFile(tmpDir, cfg, agentsMDPath, "## Language Preferences")
	require.NoError(t, err)
	require.NotEmpty(t, path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "agent_id: test-agent")
	// YAML quotes scalar values containing spaces.
	assert.Contains(t, content, `agent_name: "Test Agent"`)
	assert.Contains(t, content, "team: test-team")
	assert.Contains(t, content, "## Language Preferences")
	assert.Contains(t, content, "agents instructions")

	// Empty contract renders nothing.
	empty, err := writeContractFile(tmpDir, nil, "", "")
	require.NoError(t, err)
	assert.Empty(t, empty)
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
	for _, subDir := range []string{".gemini", ".cache", ".config", ".local", ".ssh", "asgard/config"} {
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
	asgardDir := filepath.Join(home, "asgard", "config")
	assert.Contains(t, argStr, "--tmpfs "+asgardDir)

	// Verify required bwrap components
	expectedTmpDir := filepath.Join(home, "asgard", "data", "tmp", "test-chat")
	assert.Contains(t, argStr, "--bind "+expectedTmpDir+" /tmp")
	expectedSessionDir := filepath.Join(home, "asgard", "data", "sessions", "test-chat")
	assert.Contains(t, argStr, "--bind "+expectedSessionDir+" /session")
	assert.Contains(t, argStr, "--setenv HOME "+home)
	assert.Contains(t, argStr, "--bind "+runDir+" "+runDir)
	assert.Contains(t, argStr, "--chdir "+runDir)
	assert.Contains(t, argStr, "--ro-bind "+roDir+" "+roDir)
	assert.Contains(t, argStr, "--bind "+rwDir+" "+rwDir)

	// Verify agy specific mounts
	geminiDir := filepath.Join(home, ".gemini")
	assert.Contains(t, argStr, "--bind "+geminiDir+" "+geminiDir)

	// Verify the unified AW_AGENTS.md contract mount: one canonical sandbox
	// path for every CLI, plus the env pointer.
	expectedAgySkills := filepath.Join(home, ".gemini", "antigravity-cli", "skills")
	assert.Contains(t, argStr, "--setenv AW_AGENTS_PATH /session/AW_AGENTS.md")
	assert.Contains(t, argStr, "--ro-bind "+expectedTmpDir+"/.aw_agents.md /session/AW_AGENTS.md")
	assert.Contains(t, argStr, "--ro-bind "+filepath.Join(agentPath, "skills")+" "+expectedAgySkills)

	// Verify the rendered contract carries metadata frontmatter plus the raw
	// body (CLI protocol headers are composed sandbox-side, never here).
	promptContent, readErr := os.ReadFile(filepath.Join(expectedTmpDir, ".aw_agents.md"))
	require.NoError(t, readErr)
	assert.Contains(t, string(promptContent), "agent_id: test-agent")
	assert.Contains(t, string(promptContent), "team: test-team")
	assert.Contains(t, string(promptContent), "## Language Preferences")
	assert.Contains(t, string(promptContent), "Chinese (Simplified)")
	assert.NotContains(t, string(promptContent), "/bin/ask-user")
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

	expectedDefaultTmpDir := filepath.Join(home, "asgard", "data", "tmp", "default")
	assert.Contains(t, argStrOpencode, "--bind "+expectedDefaultTmpDir+" /tmp")
	expectedDefaultSessionDir := filepath.Join(home, "asgard", "data", "sessions", "default")
	assert.Contains(t, argStrOpencode, "--bind "+expectedDefaultSessionDir+" /session")

	// Verify opencode specific mounts
	cacheDir := filepath.Join(home, ".cache")
	configDir := filepath.Join(home, ".config")
	localDir := filepath.Join(home, ".local")
	assert.Contains(t, argStrOpencode, "--bind "+cacheDir+" "+cacheDir)
	assert.Contains(t, argStrOpencode, "--bind "+configDir+" "+configDir)
	assert.Contains(t, argStrOpencode, "--bind "+localDir+" "+localDir)
	assert.Contains(t, argStrOpencode, "--chdir "+runDir)

	// Verify opencode contract mount lands at the canonical path (no more
	// CLI-specific AGENTS.md mount) and skills mounting is unchanged.
	expectedOpencodeSkills := filepath.Join(home, ".config", "opencode", "skills")
	assert.Contains(t, argStrOpencode, "--setenv AW_AGENTS_PATH /session/AW_AGENTS.md")
	assert.Contains(t, argStrOpencode, "--ro-bind "+expectedDefaultTmpDir+"/.aw_agents.md /session/AW_AGENTS.md")
	assert.Contains(t, argStrOpencode, "--ro-bind "+filepath.Join(agentPath, "skills")+" "+expectedOpencodeSkills)

	// Verify the generated contract carries the agent metadata frontmatter
	// plus the body, without CLI protocol headers.
	opencodePromptContent, readErr := os.ReadFile(filepath.Join(expectedDefaultTmpDir, ".aw_agents.md"))
	require.NoError(t, readErr)
	assert.Contains(t, string(opencodePromptContent), "agent_id: test-agent-no-team")
	assert.NotContains(t, string(opencodePromptContent), "/bin/ask-user")
	assert.NotContains(t, string(opencodePromptContent), "call-peer")
	assert.Contains(t, string(opencodePromptContent), "agents instructions")

	expectedEndOpencode := "-- aw opencode --model another-model --prompt run"
	assert.True(t, strings.HasSuffix(argStrOpencode, expectedEndOpencode), "expected suffix %q, got: %s", expectedEndOpencode, argStrOpencode)

	// Test case 3: simplest CLITarget
	cfgFull := &agentspec.AgentConfig{
		ID:          "test-agent",
		Name:        "Test Agent",
		Description: "A test agent",
		RunDirs:     []string{runDir},
		CLI: []agentspec.CLITarget{{
			CLI:   "simplest",
			Model: "simple-model",
		}},
	}

	targetSimplest := agentspec.CLITarget{
		CLI:   "simplest",
		Model: "simple-model",
	}

	argsSimplest, err := buildArgsForAgent(cfgFull, agentPath, targetSimplest, "run", optional.None[string](), runDir, "test-sock-dir", "", "", "")
	require.NoError(t, err)

	argStrSimplest := strings.Join(argsSimplest, " ")

	expectedSimplestSkills := filepath.Join(home, ".config", "simplest", "skills")
	assert.Contains(t, argStrSimplest, "--setenv AW_AGENTS_PATH /session/AW_AGENTS.md")
	assert.Contains(t, argStrSimplest, "--ro-bind "+filepath.Join(home, "asgard", "data", "tmp", "default", ".aw_agents.md")+" /session/AW_AGENTS.md")
	assert.Contains(t, argStrSimplest, "--ro-bind "+filepath.Join(agentPath, "skills")+" "+expectedSimplestSkills)
	expectedEndSimplest := "-- aw simplest --model simple-model --prompt run"
	assert.True(t, strings.HasSuffix(argStrSimplest, expectedEndSimplest), "expected suffix %q, got: %s", expectedEndSimplest, argStrSimplest)
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

// TestCompilerToolchainMounts verifies that the C compiler backend directory
// (/usr/libexec, holding cc1/collect2) and the libc headers (/usr/include) are
// bind-mounted read-only so cgo-based builds such as `go test -race` work
// inside the sandbox.
func TestCompilerToolchainMounts(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	args, err := appendBaseSandboxArgs([]string{}, tmpDir, "test-toolchain")
	require.NoError(t, err)

	argStr := strings.Join(args, " ")
	for _, p := range []string{"/usr/libexec", "/usr/include"} {
		if _, err := os.Stat(p); err == nil {
			assert.Contains(t, argStr, "--ro-bind "+p+" "+p)
		}
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

	asgardDir := filepath.Join(tmpDir, "asgard", "config")
	if err := os.MkdirAll(asgardDir, 0755); err != nil {
		t.Fatalf("failed to create asgard config dir: %v", err)
	}

	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("api_key: secret"), 0644); err != nil {
		t.Fatalf("failed to create config.yaml: %v", err)
	}

	simplestDir := filepath.Join(tmpDir, ".config", "simplest")
	if err := os.MkdirAll(simplestDir, 0755); err != nil {
		t.Fatalf("failed to create simplest dir: %v", err)
	}
	simplestKeyFile := filepath.Join(simplestDir, "key.yaml")
	if err := os.WriteFile(simplestKeyFile, []byte("providers: {}"), 0600); err != nil {
		t.Fatalf("failed to create simplest key.yaml: %v", err)
	}
	simplestModelsFile := filepath.Join(simplestDir, "models.yaml")
	if err := os.WriteFile(simplestModelsFile, []byte("models: []"), 0644); err != nil {
		t.Fatalf("failed to create simplest models.yaml: %v", err)
	}

	cmd, err := CommandForCommandExec(runcfgRunDir, "test-sock-dir", "test-chat", configPath, true)
	if err != nil {
		t.Fatalf("CommandForCommandExec error: %v", err)
	}

	argStr := strings.Join(cmd.Args, " ")

	expectedTmpDir := filepath.Join(tmpDir, "asgard", "data", "tmp", "test-chat")
	if !strings.Contains(argStr, "--die-with-parent") {
		t.Errorf("expected '--die-with-parent' in args, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--bind "+expectedTmpDir+" /tmp") {
		t.Errorf("expected '--bind %s /tmp' in args, got: %s", expectedTmpDir, argStr)
	}
	expectedSessionDir := filepath.Join(tmpDir, "asgard", "data", "sessions", "test-chat")
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
	if !strings.Contains(argStr, "--ro-bind /dev/null "+simplestKeyFile) {
		t.Errorf("expected simplest key file masking ro-bind /dev/null, got: %s", argStr)
	}
	if strings.Contains(argStr, "--tmpfs "+simplestDir) {
		t.Errorf("simplest dir must NOT be masked with tmpfs so models.yaml remains readable, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--tmpfs "+sshDir) {
		t.Errorf("expected ssh dir masking, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--tmpfs "+asgardDir) {
		t.Errorf("expected asgard dir masking, got: %s", argStr)
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

func TestCommandForCommandExec_CrossSessionMasking(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	hostData := filepath.Join(tmpDir, "asgard", "data")

	// 1. allowCrossSession = false (default)
	cmd, err := CommandForCommandExec(tmpDir, "test-sock", "chat-123", "", false)
	require.NoError(t, err)

	argStr := strings.Join(cmd.Args, " ")

	// Must mask the whole host runtime-data root (sessions, tmp and db)
	assert.Contains(t, argStr, "--tmpfs "+hostData)

	// 2. allowCrossSession = true
	cmdAllow, err := CommandForCommandExec(tmpDir, "test-sock", "chat-123", "", true)
	require.NoError(t, err)

	argStrAllow := strings.Join(cmdAllow.Args, " ")

	assert.NotContains(t, argStrAllow, "--tmpfs "+hostData)
}

func TestCommandForCommandExec_WithProxyAndMasking(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	caCertPath := filepath.Join(tmpDir, "ca.crt")
	caKeyPath := filepath.Join(tmpDir, "ca.key")
	proxyConfigPath := filepath.Join(tmpDir, "proxy.yaml")

	require.NoError(t, os.WriteFile(caCertPath, []byte("dummy-cert"), 0644))
	require.NoError(t, os.WriteFile(caKeyPath, []byte("dummy-key"), 0600))
	require.NoError(t, os.WriteFile(proxyConfigPath, []byte("dummy-proxy-conf"), 0644))

	proxyOpt := ProxySandboxConfig{
		Enabled:         true,
		ProxyAddr:       "http://127.0.0.1:8082",
		CACert:          caCertPath,
		CAKey:           caKeyPath,
		ProxyConfigPath: proxyConfigPath,
		EnvVars: map[string]string{
			"OPENAI_API_KEY": "dummy-openai-key",
		},
	}

	cmd, err := CommandForCommandExec(tmpDir, "test-sock", "chat-123", "", false, proxyOpt)
	require.NoError(t, err)

	argStr := strings.Join(cmd.Args, " ")

	// 1. Verify masking of ca.key and proxy.yaml with /dev/null
	assert.Contains(t, argStr, "--ro-bind /dev/null "+caKeyPath)
	assert.Contains(t, argStr, "--ro-bind /dev/null "+proxyConfigPath)

	// 2. Verify CA bundle path is in isolated host path and mounted to sandbox CA bundle path
	expectedIsolatedBundle := filepath.Join(paths.CABundleDir("chat-123"), "merged-ca-certificates.crt")
	assert.Contains(t, argStr, "--ro-bind "+expectedIsolatedBundle+" /etc/ssl/certs/ca-certificates.crt")
	assert.NotContains(t, argStr, "--ro-bind "+caKeyPath+" /etc/ssl/certs/ca-certificates.crt")

	// 3. Verify uppercase and lowercase proxy env vars injection
	assert.Contains(t, argStr, "--setenv HTTP_PROXY http://127.0.0.1:8082")
	assert.Contains(t, argStr, "--setenv http_proxy http://127.0.0.1:8082")
	assert.Contains(t, argStr, "--setenv HTTPS_PROXY http://127.0.0.1:8082")
	assert.Contains(t, argStr, "--setenv https_proxy http://127.0.0.1:8082")
	assert.Contains(t, argStr, "--setenv ALL_PROXY http://127.0.0.1:8082")
	assert.Contains(t, argStr, "--setenv all_proxy http://127.0.0.1:8082")
	assert.Contains(t, argStr, "--setenv NO_PROXY localhost,127.0.0.1")
	assert.Contains(t, argStr, "--setenv no_proxy localhost,127.0.0.1")
	assert.Contains(t, argStr, "--setenv SSL_CERT_FILE /etc/ssl/certs/ca-certificates.crt")
	assert.Contains(t, argStr, "--setenv REQUESTS_CA_BUNDLE /etc/ssl/certs/ca-certificates.crt")
	assert.Contains(t, argStr, "--setenv NODE_EXTRA_CA_CERTS /etc/ssl/certs/ca-certificates.crt")
	assert.Contains(t, argStr, "--setenv CURL_CA_BUNDLE /etc/ssl/certs/ca-certificates.crt")

	// 4. Verify custom proxy env vars (dummy secret) injection
	assert.Contains(t, argStr, "--setenv OPENAI_API_KEY dummy-openai-key")
}

func TestCommandForAgent_NoProxyWithMasking(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	for _, subDir := range []string{".gemini", ".cache", ".config", ".local", ".ssh"} {
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, subDir), 0755))
	}

	caCertPath := filepath.Join(tmpDir, "ca.crt")
	caKeyPath := filepath.Join(tmpDir, "ca.key")
	proxyConfigPath := filepath.Join(tmpDir, "proxy.yaml")

	require.NoError(t, os.WriteFile(caCertPath, []byte("dummy-cert"), 0644))
	require.NoError(t, os.WriteFile(caKeyPath, []byte("dummy-key"), 0600))
	require.NoError(t, os.WriteFile(proxyConfigPath, []byte("dummy-proxy-conf"), 0644))

	proxyOpt := ProxySandboxConfig{
		Enabled:         true,
		ProxyAddr:       "http://127.0.0.1:8082",
		CACert:          caCertPath,
		CAKey:           caKeyPath,
		ProxyConfigPath: proxyConfigPath,
	}

	// Test with AgentConfig containing ancestor directory of proxyConfigPath
	ancestorDir := filepath.Join(tmpDir, "ancestor_dir")
	require.NoError(t, os.MkdirAll(ancestorDir, 0755))
	nestedProxyConfigPath := filepath.Join(ancestorDir, "nested_proxy.yaml")
	require.NoError(t, os.WriteFile(nestedProxyConfigPath, []byte("nested"), 0644))

	proxyOpt.ProxyConfigPath = nestedProxyConfigPath

	agentCfg := &agentspec.AgentConfig{
		MountDirs: agentspec.MountConfig{
			ReadWrite: []string{ancestorDir},
		},
	}

	target := agentspec.CLITarget{CLI: "agy", Model: "some-model"}
	cmd, err := CommandForAgent(agentCfg, "", target, "prompt", optional.None[string](), tmpDir, "sock", "chat-123", "", "", proxyOpt)
	require.NoError(t, err)

	argStr := strings.Join(cmd.Args, " ")

	// 1. Verify masking of ca.key and proxy.yaml with /dev/null
	assert.Contains(t, argStr, "--ro-bind /dev/null "+caKeyPath)
	assert.Contains(t, argStr, "--ro-bind /dev/null "+nestedProxyConfigPath)

	// Verify that the mask for nestedProxyConfigPath appears AFTER the ancestorDir bind
	ancestorBindIdx := -1
	proxyMaskIdx := -1
	for i := 0; i < len(cmd.Args)-2; i++ {
		if cmd.Args[i] == "--bind" && cmd.Args[i+1] == ancestorDir {
			ancestorBindIdx = i
		}
		if cmd.Args[i] == "--ro-bind" && cmd.Args[i+1] == "/dev/null" && cmd.Args[i+2] == nestedProxyConfigPath {
			proxyMaskIdx = i
		}
	}
	assert.Greater(t, ancestorBindIdx, -1, "expected ancestorDir --bind in args")
	assert.Greater(t, proxyMaskIdx, -1, "expected nestedProxyConfigPath --ro-bind in args")
	assert.Greater(t, proxyMaskIdx, ancestorBindIdx, "proxy masking must come AFTER ancestor bind to avoid being shadowed")

	// 2. Strict constraint: Agent sandbox MUST NOT have proxy env or CA overrides
	assert.NotContains(t, argStr, "HTTP_PROXY")
	assert.NotContains(t, argStr, "http_proxy")
	assert.NotContains(t, argStr, "HTTPS_PROXY")
	assert.NotContains(t, argStr, "https_proxy")
	assert.NotContains(t, argStr, "ALL_PROXY")
	assert.NotContains(t, argStr, "all_proxy")
	assert.NotContains(t, argStr, "SSL_CERT_FILE")
	assert.NotContains(t, argStr, "/etc/ssl/certs/ca-certificates.crt")
}

func TestCommandForAgent_APIKeyInjection(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	for _, subDir := range []string{".gemini", ".cache", ".config", ".local", ".ssh"} {
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, subDir), 0755))
	}

	proxyOpt := ProxySandboxConfig{
		TypesafeAPIKey: "my-typesafe-key",
		GeminiAPIKey:   "my-gemini-key",
		CompassModel:   "my-compass-model",
	}

	target := agentspec.CLITarget{CLI: "agy", Model: "some-model"}
	cmd, err := CommandForAgent(nil, "", target, "prompt", optional.None[string](), tmpDir, "sock", "chat-123", "", "", proxyOpt)
	require.NoError(t, err)

	argStr := strings.Join(cmd.Args, " ")
	assert.Contains(t, argStr, "--setenv TYPESAFE_API_KEY my-typesafe-key")
	assert.Contains(t, argStr, "--setenv GEMINI_API_KEY my-gemini-key")
	assert.Contains(t, argStr, "--setenv GEMINI_MODEL_FOR_COMMAND_RESULT_COMPASS my-compass-model")
}

// TestCommandForAgent_ClaudeEnv verifies the claude sandbox relocates its
// config into the bound ~/.claude directory instead of a single-file bind.
func TestCommandForAgent_ClaudeEnv(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	for _, subDir := range []string{".claude", ".cache", ".npm", ".ssh"} {
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, subDir), 0755))
	}

	target := agentspec.CLITarget{CLI: "claude", Model: "sonnet"}
	cmd, err := CommandForAgent(nil, "", target, "prompt", optional.None[string](), tmpDir, "sock", "chat-123", "", "")
	require.NoError(t, err)

	argStr := strings.Join(cmd.Args, " ")
	assert.Contains(t, argStr, "--bind "+filepath.Join(tmpDir, ".claude")+" "+filepath.Join(tmpDir, ".claude"))
	assert.Contains(t, argStr, "--setenv CLAUDE_CONFIG_DIR "+filepath.Join(tmpDir, ".claude"))
	assert.Contains(t, argStr, "--setenv DISABLE_AUTOUPDATER 1")
	assert.NotContains(t, argStr, "--bind "+filepath.Join(tmpDir, ".claude.json"))
}

// TestCommandForCommandExec_MasksClaudeConfigDir verifies the bash sandbox
// hides ~/.claude, which now also holds .claude.json (account and MCP data).
func TestCommandForCommandExec_MasksClaudeConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".ssh"), 0755))
	runDir := filepath.Join(tmpDir, "rundir")
	require.NoError(t, os.MkdirAll(runDir, 0755))

	cmd, err := CommandForCommandExec(runDir, "sock", "chat-1", "", false)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(cmd.Args, " "), "--tmpfs "+claudeDir)
}

func TestCommandForQuery_EphemeralSandboxRunsAw(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd, cleanup, err := CommandForQuery(context.Background(), "usage", "--cli", "claude")
	if err != nil {
		t.Fatalf("CommandForQuery: %v", err)
	}
	t.Cleanup(cleanup)

	tail := cmd.Args[len(cmd.Args)-4:]
	if strings.Join(tail, " ") != "aw usage --cli claude" {
		t.Fatalf("unexpected command tail: %v", tail)
	}
}

// TestBuildSandboxArgs_CredentialScope verifies the query sandbox mounts every
// registered CLI's auth, while an agent sandbox only sees its own CLI's.
func TestBuildSandboxArgs_CredentialScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runDir := filepath.Join(home, "run")
	for _, d := range []string{runDir, filepath.Join(home, ".gemini"), filepath.Join(home, ".claude")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	bound := func(args []string, dir string) bool {
		for i := 0; i+2 < len(args); i++ {
			if args[i] == "--bind" && args[i+1] == dir && args[i+2] == dir {
				return true
			}
		}
		return false
	}

	all, err := buildSandboxArgs(nil, "", agentspec.CLITarget{}, true, runDir, "", "q-all", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{".gemini", ".claude"} {
		if !bound(all, filepath.Join(home, d)) {
			t.Errorf("query sandbox should mount ~/%s", d)
		}
	}

	awConfig := filepath.Join(home, ".config", "aw", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(awConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(awConfig, []byte("agents: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	withCfg, err := buildSandboxArgs(nil, "", agentspec.CLITarget{}, true, runDir, "", "q-cfg", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(withCfg, " "), "--ro-bind "+awConfig+" "+queryConfigPath+" --setenv AW_CONFIG "+queryConfigPath) {
		t.Error("query sandbox should mount the aw config read-only and point AW_CONFIG at it")
	}

	// A symlinked config is mounted at its resolved path.
	realCfg := filepath.Join(home, "dotfiles", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(realCfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(awConfig, realCfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realCfg, awConfig); err != nil {
		t.Fatal(err)
	}
	linked, err := buildSandboxArgs(nil, "", agentspec.CLITarget{}, true, runDir, "", "q-link", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(linked, " "), "--ro-bind "+realCfg+" "+queryConfigPath) {
		t.Error("symlinked aw config should be mounted from its resolved path")
	}

	one, err := buildSandboxArgs(nil, "", agentspec.CLITarget{CLI: "claude"}, false, runDir, "", "q-one", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !bound(one, filepath.Join(home, ".claude")) {
		t.Error("claude sandbox should mount ~/.claude")
	}
	if bound(one, filepath.Join(home, ".gemini")) {
		t.Error("claude sandbox must not mount ~/.gemini")
	}
}

func indexOfSeq(args []string, seq ...string) int {
	for i := 0; i+len(seq) <= len(args); i++ {
		if strings.Join(args[i:i+len(seq)], "\x00") == strings.Join(seq, "\x00") {
			return i
		}
	}
	return -1
}

func TestGitSandboxArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	git := []string{"--git-dir=/repo/.git", "--work-tree=/repo", "status"}

	tests := []struct {
		name      string
		spec      GitSandboxSpec
		bind      string
		unshare   bool
		wantSSH   bool
		wantProxy bool
	}{
		{"read-only offline", GitSandboxSpec{ChatID: "chat1", WorkTreeSrc: "/proc/self/fd/3", Git: git}, "--ro-bind", true, false, false},
		{"pull writable with network", GitSandboxSpec{ChatID: "chat1", WorkTreeSrc: "/proc/self/fd/3", ReadWrite: true, Network: true, Git: git}, "--bind", false, true, false},
		{"push read-only with network", GitSandboxSpec{
			ChatID: "chat1", WorkTreeSrc: "/proc/self/fd/3", Network: true, Git: git,
			Proxy: ProxySandboxConfig{Enabled: false, ProxyAddr: "http://127.0.0.1:1"},
		}, "--ro-bind", false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := GitSandboxArgs(tt.spec)
			require.NoError(t, err)
			assert.Equal(t, 0, indexOfSeq(args, "--clearenv"))
			for _, flag := range []string{"--die-with-parent", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--new-session"} {
				assert.Contains(t, args, flag)
			}
			assert.Equal(t, tt.unshare, indexOfSeq(args, "--unshare-net") >= 0)
			assert.GreaterOrEqual(t, indexOfSeq(args, tt.bind, "/proc/self/fd/3", "/repo", "--chdir", "/repo"), 0)
			otherBind := map[string]string{"--bind": "--ro-bind", "--ro-bind": "--bind"}[tt.bind]
			assert.Less(t, indexOfSeq(args, otherBind, "/proc/self/fd/3", "/repo"), 0)
			// Only the repo, chat dirs and system paths are mounted: HOME is a tmpfs.
			assert.GreaterOrEqual(t, indexOfSeq(args, "--tmpfs", home), 0)
			assert.Less(t, indexOfSeq(args, "--bind", home, home), 0)
			assert.GreaterOrEqual(t, indexOfSeq(args, "--ro-bind", filepath.Join(home, ".gitconfig"), filepath.Join(home, ".gitconfig")), 0)
			assert.Equal(t, tt.wantSSH, indexOfSeq(args, "--tmpfs", filepath.Join(home, ".ssh")) >= 0, "ssh dir masked only with network")
			// The data root is masked like in agent command sandboxes.
			assert.GreaterOrEqual(t, indexOfSeq(args, "--tmpfs", paths.DataDir()), 0)
			n := len(args)
			assert.Equal(t, append([]string{"--", "git"}, git...), args[n-len(git)-2:])
		})
	}

	t.Run("env is passed explicitly", func(t *testing.T) {
		args, err := GitSandboxArgs(GitSandboxSpec{ChatID: "chat1", WorkTreeSrc: "/x", Env: []string{"GIT_TERMINAL_PROMPT=0", "bad"}, Git: git})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, indexOfSeq(args, "--setenv", "GIT_TERMINAL_PROMPT", "0"), 0)
		assert.Less(t, indexOfSeq(args, "--setenv", "bad"), 0)
	})
}

func TestGitSandboxArgsConfigMasks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte("secret: x"), 0o600))
	args, err := GitSandboxArgs(GitSandboxSpec{ChatID: "chat1", WorkTreeSrc: "/x", ConfigPath: cfg, Git: []string{"status"}})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, indexOfSeq(args, "--ro-bind", "/dev/null", cfg), 0)
}

// TestGitSandboxRuns needs a working bwrap (user namespaces); it is skipped
// where bubblewrap is missing or cannot create a sandbox.
func TestGitSandboxRuns(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap is not installed")
	}
	if err := exec.Command("bwrap", "--ro-bind", "/", "/", "true").Run(); err != nil {
		t.Skipf("bwrap cannot create a sandbox here: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=T", "-c", "user.email=t@e.x", "commit", "--allow-empty", "-m", "c"}} {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	dir, err := os.Open(repo)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dir.Close() })

	run := func(spec GitSandboxSpec) (string, error) {
		spec.ChatID = "chat1"
		cmd, err := CommandForGit(t.Context(), spec)
		require.NoError(t, err)
		cmd.ExtraFiles = []*os.File{dir}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run(GitSandboxSpec{Git: []string{"--git-dir=/repo/.git", "--work-tree=/repo", "log", "--format=%s"}})
	require.NoError(t, err, out)
	assert.Equal(t, "c", strings.TrimSpace(out))

	// Read-only: a write inside the repo fails.
	_, err = run(GitSandboxSpec{Git: []string{"--git-dir=/repo/.git", "--work-tree=/repo", "tag", "t1"}})
	assert.Error(t, err)
	_, err = run(GitSandboxSpec{ReadWrite: true, Git: []string{"--git-dir=/repo/.git", "--work-tree=/repo", "tag", "t1"}})
	assert.NoError(t, err)
}

func TestSandboxCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	chat := "chat-1"
	tmp, sess, data := paths.SessionTmpDir(chat), paths.SessionDir(chat), paths.DataDir()
	outside := t.TempDir()

	for _, tc := range []struct {
		name, dir, want string
		cross           bool
		wantErr         bool
	}{
		{name: "tmp root", dir: tmp, want: "/tmp"},
		{name: "tmp sub", dir: filepath.Join(tmp, "a", "b"), want: "/tmp/a/b"},
		{name: "session sub", dir: filepath.Join(sess, "sub"), want: "/session/sub"},
		{name: "outside root keeps its path", dir: outside, want: outside},
		{name: "another chat is masked", dir: paths.SessionTmpDir("chat-2"), wantErr: true},
		{name: "data root is masked", dir: filepath.Join(data, "shared"), wantErr: true},
		{name: "another chat with cross-session", dir: paths.SessionTmpDir("chat-2"), cross: true, want: paths.SessionTmpDir("chat-2")},
		{name: "own tmp sub-name prefix is not own tmp", dir: tmp + "x", wantErr: true},
	} {
		got, err := SandboxCwd(chat, tc.dir, tc.cross, nil)
		if tc.wantErr {
			assert.Error(t, err, tc.name)
			continue
		}
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}
}

func TestCommandForCommandExecAt_Chdir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	chat := "chat-1"
	hostDir := filepath.Join(paths.SessionTmpDir(chat), "sub")
	require.NoError(t, os.MkdirAll(hostDir, 0o755))

	cmd, err := CommandForCommandExecAt(hostDir, "/tmp/sub", "sock", chat, "", false, nil)
	require.NoError(t, err)
	chdir := indexOfSeq(cmd.Args, "--chdir", "/tmp/sub")
	require.GreaterOrEqual(t, chdir, 0, "the sandbox cwd is the in-sandbox path: %v", cmd.Args)
	assert.Less(t, indexOfSeq(cmd.Args, "--bind", paths.SessionTmpDir(chat), "/tmp"), chdir)
	assert.Equal(t, -1, indexOfSeq(cmd.Args, "--chdir", hostDir), "the host path does not exist in the sandbox")
	// The /tmp mount precedes the data-root mask, so it stays visible.
	mask := indexOfSeq(cmd.Args, "--tmpfs", paths.DataDir())
	require.GreaterOrEqual(t, mask, 0)
	assert.Less(t, indexOfSeq(cmd.Args, "--bind", paths.SessionTmpDir(chat), "/tmp"), mask)

	// A run dir outside HOME is bound at its own path, before the masks.
	outside := t.TempDir()
	cmd, err = CommandForCommandExecAt(outside, outside, "sock", chat, "", false, nil)
	require.NoError(t, err)
	bind := indexOfSeq(cmd.Args, "--bind", outside, outside)
	require.GreaterOrEqual(t, bind, 0)
	assert.Less(t, bind, indexOfSeq(cmd.Args, "--tmpfs", paths.DataDir()))
	assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--chdir", outside), 0)
}

func TestCommandForCommandExecAt_ReadonlyMounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	chat := "chat-1"
	homeBind := func(args []string) int { return indexOfSeq(args, "--bind", home, home) }

	// A readonly root under HOME, with the run dir inside it: ro-bound after
	// the whole-HOME rw bind, so it wins.
	roHome := filepath.Join(home, "docs")
	require.NoError(t, os.MkdirAll(filepath.Join(roHome, "sub"), 0o755))
	cmd, err := CommandForCommandExecAt(filepath.Join(roHome, "sub"), filepath.Join(roHome, "sub"), "sock", chat, "", false, []string{roHome})
	require.NoError(t, err)
	ro := indexOfSeq(cmd.Args, "--ro-bind", roHome, roHome)
	require.GreaterOrEqual(t, ro, 0, "%v", cmd.Args)
	assert.Greater(t, ro, homeBind(cmd.Args))
	assert.Equal(t, -1, indexOfSeq(cmd.Args, "--bind", filepath.Join(roHome, "sub"), filepath.Join(roHome, "sub")))

	// A readonly root outside HOME holding the run dir: never bound rw.
	roOut := t.TempDir()
	runDir := filepath.Join(roOut, "work")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	cmd, err = CommandForCommandExecAt(runDir, runDir, "sock", chat, "", false, []string{roOut})
	require.NoError(t, err)
	assert.Equal(t, -1, indexOfSeq(cmd.Args, "--bind", runDir, runDir))
	assert.Equal(t, -1, indexOfSeq(cmd.Args, "--bind", roOut, roOut))
	ro = indexOfSeq(cmd.Args, "--ro-bind", roOut, roOut)
	require.GreaterOrEqual(t, ro, 0)
	assert.Greater(t, ro, homeBind(cmd.Args))
	assert.Less(t, ro, indexOfSeq(cmd.Args, "--tmpfs", paths.DataDir()), "before the masks")
	assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--chdir", runDir), 0)

	// A writable run dir containing a readonly sub-root: rw bind first, the
	// ro-bind of the sub-root after it.
	rw := t.TempDir()
	sub := filepath.Join(rw, "ro")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	cmd, err = CommandForCommandExecAt(rw, rw, "sock", chat, "", false, []string{sub, filepath.Join(rw, "missing")})
	require.NoError(t, err)
	rwBind := indexOfSeq(cmd.Args, "--bind", rw, rw)
	require.GreaterOrEqual(t, rwBind, 0)
	assert.Greater(t, indexOfSeq(cmd.Args, "--ro-bind", sub, sub), rwBind)
	assert.Equal(t, -1, indexOfSeq(cmd.Args, "--ro-bind", filepath.Join(rw, "missing"), filepath.Join(rw, "missing")), "absent roots are skipped")

	// A sibling with the same name prefix is not "beneath" the readonly root.
	other := roOut + "x"
	require.NoError(t, os.MkdirAll(other, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(other) })
	cmd, err = CommandForCommandExecAt(other, other, "sock", chat, "", false, []string{roOut})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--bind", other, other), 0)
}

func TestBuildSandboxArgs_ReadonlyMountsComeAfterEveryReadWriteBind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runDir := t.TempDir()
	check := func(t *testing.T, args []string, roDir string, rwDirs ...string) {
		t.Helper()
		ro := indexOfSeq(args, "--ro-bind", roDir, roDir)
		require.GreaterOrEqual(t, ro, 0, "%v", args)
		for _, rw := range rwDirs {
			i := indexOfSeq(args, "--bind", rw, rw)
			require.GreaterOrEqual(t, i, 0, rw)
			assert.Greater(t, ro, i, "the ro-bind of %s must come after the rw bind of %s", roDir, rw)
		}
		assert.Less(t, ro, indexOfSeq(args, "--"), "before the end of the sandbox args")
	}

	// Under an rw ancestor given by mount_dirs.readwrite.
	rw := t.TempDir()
	ro := filepath.Join(rw, "ro")
	require.NoError(t, os.MkdirAll(ro, 0o755))
	cfg := &agentspec.AgentConfig{MountDirs: agentspec.MountConfig{ReadOnly: []string{ro}, ReadWrite: []string{rw}}}
	args, err := buildSandboxArgs(cfg, "", agentspec.CLITarget{}, false, runDir, "", "c1", "", "")
	require.NoError(t, err)
	check(t, args, ro, rw)

	// Under an ancestor that is a run dir, and under the (extra) bound run dir.
	cfg = &agentspec.AgentConfig{RunDirs: []string{rw}, MountDirs: agentspec.MountConfig{ReadOnly: []string{ro}}}
	args, err = buildSandboxArgs(cfg, "", agentspec.CLITarget{}, false, runDir, "", "c2", "", "")
	require.NoError(t, err)
	check(t, args, ro, rw, runDir)
	sub := filepath.Join(runDir, "docs")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	cfg = &agentspec.AgentConfig{MountDirs: agentspec.MountConfig{ReadOnly: []string{sub}}}
	args, err = buildSandboxArgs(cfg, "", agentspec.CLITarget{}, false, runDir, "", "c3", "", "")
	require.NoError(t, err)
	check(t, args, sub, runDir)

	// Under a CLI mount directory (~/.cache is bound for the CLIs).
	cacheRo := filepath.Join(home, ".cache", "refs")
	require.NoError(t, os.MkdirAll(cacheRo, 0o755))
	cfg = &agentspec.AgentConfig{MountDirs: agentspec.MountConfig{ReadOnly: []string{cacheRo}}}
	args, err = buildSandboxArgs(cfg, "", agentspec.CLITarget{}, true, runDir, "", "c4", "", "")
	require.NoError(t, err)
	check(t, args, cacheRo, filepath.Join(home, ".cache"))
}

func TestReadonlyMountsFollowSymlinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	chat := "chat-1"
	real := t.TempDir()
	proj := filepath.Join(real, "proj")
	require.NoError(t, os.MkdirAll(proj, 0o755))
	link := filepath.Join(t.TempDir(), "refs")
	require.NoError(t, os.Symlink(real, link))
	evReal, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)
	evProj := filepath.Join(evReal, "proj")

	// Both names of the root are bound read-only, so the working dir under the
	// resolved target exists in the sandbox, read-only and not re-bound rw.
	cmd, err := CommandForCommandExecAt(evProj, evProj, "sock", chat, "", false, []string{link})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--ro-bind", link, link), 0)
	assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--ro-bind", evReal, evReal), 0)
	assert.Equal(t, -1, indexOfSeq(cmd.Args, "--bind", evProj, evProj))
	assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--chdir", evProj), 0)

	// Backend-local shape: the run dir is reached through a symlink into the
	// readonly root, and must not get a read-write bind either.
	viaLink := filepath.Join(link, "proj")
	cmd, err = CommandForCommandExecAt(viaLink, viaLink, "sock", chat, "", false, []string{real})
	require.NoError(t, err)
	assert.Equal(t, -1, indexOfSeq(cmd.Args, "--bind", viaLink, viaLink))
	assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--ro-bind", real, real), 0)
}

func TestReadonlyAliasRunDirIsMountedReadOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(real, alias))

	// The run dir is an alias (outside HOME) of a readonly root: its path must
	// exist in the sandbox, read-only, never read-write.
	cmd, err := CommandForCommandExecAt(alias, alias, "sock", "chat-1", "", false, []string{real})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--ro-bind", alias, alias), 0, "%v", cmd.Args)
	assert.Equal(t, -1, indexOfSeq(cmd.Args, "--bind", alias, alias))
	assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--chdir", alias), 0)

	// Without a readonly root it stays read-write.
	cmd, err = CommandForCommandExecAt(alias, alias, "sock", "chat-1", "", false, nil)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--bind", alias, alias), 0)
}

func TestBuildSandboxArgs_ReadWriteBindsAliasingReadonlyRootsAreDemoted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	realRO := t.TempDir()
	alias := func() string {
		l := filepath.Join(t.TempDir(), "alias")
		require.NoError(t, os.Symlink(realRO, l))
		return l
	}
	runDir := t.TempDir()
	demoted := func(args []string, dir string) {
		t.Helper()
		assert.GreaterOrEqual(t, indexOfSeq(args, "--ro-bind", dir, dir), 0, "%s must be read-only: %v", dir, args)
		assert.Equal(t, -1, indexOfSeq(args, "--bind", dir, dir), "%s must not be read-write", dir)
	}

	rwAlias, runAlias, extra := alias(), alias(), alias()
	cfg := &agentspec.AgentConfig{
		RunDirs:   []string{runAlias},
		MountDirs: agentspec.MountConfig{ReadOnly: []string{realRO}, ReadWrite: []string{rwAlias}},
	}
	args, err := buildSandboxArgs(cfg, "", agentspec.CLITarget{}, false, extra, "", "c1", "", "")
	require.NoError(t, err)
	demoted(args, rwAlias)
	demoted(args, runAlias)
	demoted(args, extra) // the extra run dir bind

	// A CLI mount directory that aliases a readonly root.
	cache := filepath.Join(home, ".cache")
	require.NoError(t, os.Symlink(realRO, cache))
	cfg = &agentspec.AgentConfig{MountDirs: agentspec.MountConfig{ReadOnly: []string{realRO}}}
	args, err = buildSandboxArgs(cfg, "", agentspec.CLITarget{}, true, runDir, "", "c2", "", "")
	require.NoError(t, err)
	demoted(args, cache)
	// Unrelated dirs stay read-write.
	assert.GreaterOrEqual(t, indexOfSeq(args, "--bind", runDir, runDir), 0)
}

func TestReadonlyRootUnderDataRootIsRemountedAfterTheMask(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	chat := "chat-1"
	roUnder := filepath.Join(paths.DataDir(), "shared", "docs")
	require.NoError(t, os.MkdirAll(roUnder, 0o755))
	roElsewhere := t.TempDir()

	cmd, err := CommandForCommandExecAt(roUnder, roUnder, "sock", chat, "", false, []string{roUnder, roElsewhere})
	require.NoError(t, err)
	mask := indexOfSeq(cmd.Args, "--tmpfs", paths.DataDir())
	require.GreaterOrEqual(t, mask, 0)
	under := indexOfSeq(cmd.Args, "--ro-bind", roUnder, roUnder)
	require.GreaterOrEqual(t, under, 0)
	assert.Greater(t, under, mask, "re-mounted after the data-root mask so it stays visible")
	elsewhere := indexOfSeq(cmd.Args, "--ro-bind", roElsewhere, roElsewhere)
	require.GreaterOrEqual(t, elsewhere, 0)
	assert.Less(t, elsewhere, mask, "other readonly roots keep their place")

	// With cross-session access there is no mask: normal place.
	cmd, err = CommandForCommandExecAt(roUnder, roUnder, "sock", chat, "", true, []string{roUnder})
	require.NoError(t, err)
	assert.Equal(t, -1, indexOfSeq(cmd.Args, "--tmpfs", paths.DataDir()))
	assert.GreaterOrEqual(t, indexOfSeq(cmd.Args, "--ro-bind", roUnder, roUnder), 0)

	// The cwd under such a root is visible, so it is accepted.
	got, err := SandboxCwd(chat, roUnder, false, []string{roUnder})
	require.NoError(t, err)
	assert.Equal(t, roUnder, got)
	_, err = SandboxCwd(chat, roUnder, false, nil)
	assert.Error(t, err)
}

func TestRelativeReadonlyMountsFailClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runDir := t.TempDir()

	_, err := appendReadonlyBinds(nil, []string{"/abs-but-missing", "relative/dir"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"relative/dir"`)

	_, err = CommandForCommandExecAt(runDir, runDir, "sock", "chat-1", "", false, []string{"relative/dir"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "relative/dir")

	cfg := &agentspec.AgentConfig{MountDirs: agentspec.MountConfig{ReadOnly: []string{"relative/dir"}}}
	_, err = buildSandboxArgs(cfg, "", agentspec.CLITarget{}, false, runDir, "", "c1", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "relative/dir")
}

func TestValidateReadonlyRoots(t *testing.T) {
	home := t.TempDir()
	masked := []string{filepath.Join(home, ".ssh")}
	require.NoError(t, validateReadonlyRoots([]string{filepath.Join(home, "proj")}, masked))
	require.NoError(t, validateReadonlyRoots([]string{home}, masked))

	err := validateReadonlyRoots([]string{filepath.Join(home, ".ssh", "keys")}, masked)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "masks")

	for _, d := range []string{"/tmp", "/session", "/session/x"} {
		err = validateReadonlyRoots([]string{d}, nil)
		require.Error(t, err, d)
		assert.Contains(t, err.Error(), "reserved")
	}
}

func TestReadonlyRootInMaskedDirIsRejected(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	runDir := t.TempDir()
	ro := []string{filepath.Join(home, ".ssh", "sub")}

	_, err = CommandForCommandExecAt(runDir, runDir, "sock", "chat-1", "", false, ro)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "masks")

	cfg := &agentspec.AgentConfig{MountDirs: agentspec.MountConfig{ReadOnly: ro}}
	_, err = buildSandboxArgs(cfg, "", agentspec.CLITarget{}, false, runDir, "", "c1", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "masks")
}
