package bwrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AgentDrasil/asgard/lib/roles"
	"github.com/moznion/go-optional"
)

func TestBuildArgs(t *testing.T) {
	// Create some temp directories to use in config
	tmpDir, err := os.MkdirTemp("", "bwrap-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

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

	cfg := &roles.AgentConfig{
		ID:          "test-agent",
		Name:        "Test Agent",
		Description: "A test agent",
		RunDirs:     []string{runDir},
		MountDirs: roles.MountConfig{
			ReadOnly:  []string{roDir},
			ReadWrite: []string{rwDir},
		},
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("getting home dir: %v", err)
	}

	// Test case 1: agy CLITarget with session
	targetAgy := roles.CLITarget{
		CLI:   "agy",
		Model: "some-model",
	}

	args, err := buildArgsForAgent(cfg, targetAgy, "some prompt", optional.Some("my-session-id"))
	if err != nil {
		t.Fatalf("buildArgsForAgent error: %v", err)
	}

	argStr := strings.Join(args, " ")

	// Verify required bwrap components
	if !strings.Contains(argStr, "--tmpfs /tmp") {
		t.Errorf("expected '--tmpfs /tmp' in args, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--setenv HOME "+home) {
		t.Errorf("expected home env setup, got: %s", argStr)
	}
	if !strings.Contains(argStr, "--bind "+runDir+" "+runDir) {
		t.Errorf("expected run_dirs bind mount, got: %s", argStr)
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

	// Verify ending command structure with --session and --prompt
	expectedEnd := "-- aw agy --model some-model --session my-session-id --prompt some prompt"
	if !strings.HasSuffix(argStr, expectedEnd) {
		t.Errorf("expected suffix %q, got: %s", expectedEnd, argStr)
	}

	// Test case 2: opencode CLITarget without session (None)
	targetOpencode := roles.CLITarget{
		CLI:   "opencode",
		Model: "another-model",
	}

	argsOpencode, err := buildArgsForAgent(cfg, targetOpencode, "run", optional.None[string]())
	if err != nil {
		t.Fatalf("buildArgsForAgent error: %v", err)
	}

	argStrOpencode := strings.Join(argsOpencode, " ")

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

	expectedEndOpencode := "-- aw opencode --model another-model --prompt run"
	if !strings.HasSuffix(argStrOpencode, expectedEndOpencode) {
		t.Errorf("expected suffix %q, got: %s", expectedEndOpencode, argStrOpencode)
	}
}
