package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/agentwrapper"
	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

func TestRun(t *testing.T) {
	// Set up temporary home and required bwrap directories
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	for _, subDir := range []string{".gemini", ".cache", ".config", ".local"} {
		if err := os.MkdirAll(filepath.Join(tmpDir, subDir), 0755); err != nil {
			t.Fatalf("failed to create %s dir: %v", subDir, err)
		}
	}

	// Create a mock bwrap executable shell script
	mockBwrapPath := filepath.Join(tmpDir, "bwrap")
	scriptContent := "#!/bin/sh\nfor arg in \"$@\"; do\n  echo \"$arg\"\ndone\necho \"mock bwrap execution succeeded\"\n"
	if err := os.WriteFile(mockBwrapPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to write mock bwrap script: %v", err)
	}

	// Prepended tmpDir to PATH
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath)

	// Set up fake clients to control quota responses
	fakeAgy := &agentwrapper.FakeClient{
		UsageFunc: func(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
			return []types.ModelUsage{
				{Model: "agy-model-low", Remaining: 0.15},
				{Model: "agy-model-high", Remaining: 0.50},
			}, nil
		},
	}
	fakeOpencode := &agentwrapper.FakeClient{
		UsageFunc: func(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
			return []types.ModelUsage{
				{Model: "opencode-model-high", Remaining: 0.80},
			}, nil
		},
	}

	agentwrapper.SetClients(map[string]types.CLIClient{
		"agy":      fakeAgy,
		"opencode": fakeOpencode,
	})
	defer agentwrapper.SetClients(nil)

	// 1. Test case: successful run choosing the first target with > 20% quota
	agent := &agents.Agent{
		Config: agents.AgentConfig{
			ID:          "test-agent",
			Name:        "Test Agent",
			Description: "A test agent for testing run pkg",
			CLI: []agents.CLITarget{
				{CLI: "agy", Model: "agy-model-low"},            // 15% quota (should be skipped)
				{CLI: "agy", Model: "agy-model-high"},           // 50% quota (should be chosen)
				{CLI: "opencode", Model: "opencode-model-high"}, // 80% quota (not reached because we pick first > 20%)
			},
		},
	}

	out, err := Run(context.Background(), agent, "hello agent", optional.Some("my-session"))
	if err != nil {
		t.Fatalf("unexpected error running agent: %v", err)
	}

	outStr := string(out)
	if !strings.Contains(outStr, "mock bwrap execution succeeded") {
		t.Errorf("expected mock output, got: %q", outStr)
	}
	// Verify that agy-model-high was chosen
	if !strings.Contains(outStr, "agy-model-high") {
		t.Errorf("expected chosen model to be agy-model-high, output was: %q", outStr)
	}
	// Verify that agy-model-low was NOT chosen
	if strings.Contains(outStr, "agy-model-low") {
		t.Errorf("expected agy-model-low not to be chosen, output was: %q", outStr)
	}

	// 2. Test case: no targets have more than 20% quota
	lowQuotaAgent := &agents.Agent{
		Config: agents.AgentConfig{
			ID:          "low-quota-agent",
			Name:        "Low Quota Agent",
			Description: "An agent with only low quota targets",
			CLI: []agents.CLITarget{
				{CLI: "agy", Model: "agy-model-low"},
			},
		},
	}

	_, err = Run(context.Background(), lowQuotaAgent, "hello", optional.None[string]())
	if err == nil {
		t.Error("expected error due to insufficient quota, but got nil")
	} else if !strings.Contains(err.Error(), "no CLI target with more than 20% quota") {
		t.Errorf("expected quota limit error message, got: %v", err)
	}
}
