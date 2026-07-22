package bwrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/lib/agents"
)

func TestBuildArgs(t *testing.T) {
	tmpDir := t.TempDir()

	runDir := filepath.Join(tmpDir, "rundir")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("failed to create rundir: %v", err)
	}

	roDir := filepath.Join(tmpDir, "rodir")
	if err := os.MkdirAll(roDir, 0755); err != nil {
		t.Fatalf("failed to create rodir: %v", err)
	}

	rwDir := filepath.Join(tmpDir, "rwdir")
	if err := os.MkdirAll(rwDir, 0755); err != nil {
		t.Fatalf("failed to create rwdir: %v", err)
	}

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
	for _, subDir := range []string{".gemini", ".cache", ".config", ".local"} {
		if err := os.MkdirAll(filepath.Join(home, subDir), 0755); err != nil {
			t.Fatalf("failed to create %s dir: %v", subDir, err)
		}
	}

	// Create simulated agent configuration directory with AGENTS.md and skills
	agentPath := filepath.Join(tmpDir, "test-agent-dir")
	if err := os.MkdirAll(filepath.Join(agentPath, "skills"), 0755); err != nil {
		t.Fatalf("failed to create skills dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentPath, "AGENTS.md"), []byte("agents instructions"), 0644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	// Test case 1: agy CLITarget with session
	targetAgy := agents.CLITarget{
		CLI:   "agy",
		Model: "some-model",
	}

	args, err := buildArgsForAgent(cfg, agentPath, targetAgy, "some prompt", optional.Some("my-session-id"), runDir, "test-sock-dir", "test-chat")
	if err != nil {
		t.Fatalf("buildArgsForAgent error: %v", err)
	}

	argStr := strings.Join(args, " ")

	// Verify required bwrap components
	expectedTmpDir := filepath.Join(home, "tmp", "test-chat")
	if !strings.Contains(argStr, "--bind "+expectedTmpDir+" /tmp") {
		t.Errorf("expected '--bind %s /tmp' in args, got: %s", expectedTmpDir, argStr)
	}
	if !strings.Contains(argStr, "--setenv HOME "+home) {
		t.Errorf("expected home env setup, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--bind "+runDir+" "+runDir) {
		t.Errorf("expected run_dirs bind mount, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--chdir "+runDir) {
		t.Errorf("expected '--chdir %s' in args, got: %s", runDir, argStr)
	}
	if !strings.Contains(argStr, "--ro-bind "+roDir+" "+roDir) {
		t.Errorf("expected mount_dirs readonly mount, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--bind "+rwDir+" "+rwDir) {
		t.Errorf("expected mount_dirs readwrite mount, got: %s", argStr)
	}

	// Verify agy specific mounts
	geminiDir := filepath.Join(home, ".gemini")
	if !strings.Contains(argStr, "--bind "+geminiDir+" "+geminiDir) {
		t.Errorf("expected agy .gemini bind mount, got: %s", argStr)
	}

	// Verify agy agent path mounts
	expectedAgyAgentsMD := filepath.Join(home, ".gemini", "GEMINI.md")
	expectedAgySkills := filepath.Join(home, ".gemini", "antigravity-cli", "skills")
	if !strings.Contains(argStr, "--ro-bind "+filepath.Join(agentPath, "AGENTS.md")+" "+expectedAgyAgentsMD) {
		t.Errorf("expected agy AGENTS.md mount, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--ro-bind "+filepath.Join(agentPath, "skills")+" "+expectedAgySkills) {
		t.Errorf("expected agy skills mount, got: %s", argStr)
	}

	// Verify ending command structure with --session and --prompt
	expectedEnd := "-- aw agy --model some-model --session my-session-id --prompt some prompt"
	if !strings.HasSuffix(argStr, expectedEnd) {
		t.Errorf("expected suffix %q, got: %s", expectedEnd, argStr)
	}

	// Test case 2: opencode CLITarget without session (None)
	targetOpencode := agents.CLITarget{
		CLI:   "opencode",
		Model: "another-model",
	}

	argsOpencode, err := buildArgsForAgent(cfg, agentPath, targetOpencode, "run", optional.None[string](), runDir, "test-sock-dir", "")
	if err != nil {
		t.Fatalf("buildArgsForAgent error: %v", err)
	}

	argStrOpencode := strings.Join(argsOpencode, " ")

	expectedDefaultTmpDir := filepath.Join(home, "tmp", "default")
	if !strings.Contains(argStrOpencode, "--bind "+expectedDefaultTmpDir+" /tmp") {
		t.Errorf("expected '--bind %s /tmp' in argsOpencode, got: %s", expectedDefaultTmpDir, argStrOpencode)
	}

	// Verify opencode specific mounts
	cacheDir := filepath.Join(home, ".cache")
	configDir := filepath.Join(home, ".config")
	localDir := filepath.Join(home, ".local")
	if !strings.Contains(argStrOpencode, "--bind "+cacheDir+" "+cacheDir) {
		t.Errorf("expected opencode .cache bind mount, got: %s", argStrOpencode)
	}
	if !strings.Contains(argStrOpencode, "--bind "+configDir+" "+configDir) {
		t.Errorf("expected opencode .config bind mount, got: %s", argStrOpencode)
	}
	if !strings.Contains(argStrOpencode, "--bind "+localDir+" "+localDir) {
		t.Errorf("expected opencode .local bind mount, got: %s", argStrOpencode)
	}
	if !strings.Contains(argStrOpencode, "--chdir "+runDir) {
		t.Errorf("expected '--chdir %s' in argsOpencode, got: %s", runDir, argStrOpencode)
	}

	// Verify opencode agent path mounts
	expectedOpencodeAgentsMD := filepath.Join(home, ".config", "opencode", "AGENTS.md")
	expectedOpencodeSkills := filepath.Join(home, ".config", "opencode", "skills")
	if !strings.Contains(argStrOpencode, "--ro-bind "+filepath.Join(agentPath, "AGENTS.md")+" "+expectedOpencodeAgentsMD) {
		t.Errorf("expected opencode AGENTS.md mount, got: %s", argStrOpencode)
	}
	if !strings.Contains(argStrOpencode, "--ro-bind "+filepath.Join(agentPath, "skills")+" "+expectedOpencodeSkills) {
		t.Errorf("expected opencode skills mount, got: %s", argStrOpencode)
	}

	expectedEndOpencode := "-- aw opencode --model another-model --prompt run"
	if !strings.HasSuffix(argStrOpencode, expectedEndOpencode) {
		t.Errorf("expected suffix %q, got: %s", expectedEndOpencode, argStrOpencode)
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
