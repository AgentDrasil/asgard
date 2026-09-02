package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func TestRun(t *testing.T) {
	// Set up temporary home and required bwrap directories
	tmpDir := t.TempDir()

	// Preserve GOPATH and GOCACHE to avoid permission issues during cleanup and speed up go build
	origHome := os.Getenv("HOME")
	origGopath := os.Getenv("GOPATH")
	origGocache := os.Getenv("GOCACHE")

	if origGopath != "" {
		t.Setenv("GOPATH", origGopath)
	} else if origHome != "" {
		t.Setenv("GOPATH", filepath.Join(origHome, "go"))
	}
	if origGocache != "" {
		t.Setenv("GOCACHE", origGocache)
	} else if origHome != "" {
		t.Setenv("GOCACHE", filepath.Join(origHome, ".cache", "go-build"))
	}

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
				{Model: "agy-model-zero", Remaining: 0.0},
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
	t.Cleanup(func() { agentwrapper.SetClients(nil) })

	// 1. Test case: successful run choosing the first target with > 10% quota
	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:          "test-agent",
			Name:        "Test Agent",
			Description: "A test agent for testing run pkg",
			CLI: []agentspec.CLITarget{
				{CLI: "agy", Model: "agy-model-low"},            // 15% quota (>10%, chosen)
				{CLI: "agy", Model: "agy-model-high"},           // 50% quota
				{CLI: "opencode", Model: "opencode-model-high"}, // 80% quota
			},
			RunDirs: []string{filepath.Join(tmpDir, "some-allowed-dir")},
		},
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "some-allowed-dir"), 0755); err != nil {
		t.Fatalf("failed to create run dir: %v", err)
	}

	out, err := Run(context.Background(), agent, "hello agent", optional.Some("my-session"), optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, nil)
	if err != nil {
		t.Fatalf("unexpected error running agent: %v", err)
	}

	outStr := string(out)
	if !strings.Contains(outStr, "mock bwrap execution succeeded") {
		t.Errorf("expected mock output, got: %q", outStr)
	}
	// Verify that agy-model-low (15% > 10%) was chosen
	if !strings.Contains(outStr, "agy-model-low") {
		t.Errorf("expected chosen model to be agy-model-low, output was: %q", outStr)
	}

	// 2. Test case: no targets have more than 10% quota
	insufficientQuotaAgent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:          "insufficient-quota-agent",
			Name:        "Insufficient Quota Agent",
			Description: "An agent with target below 10% quota",
			CLI: []agentspec.CLITarget{
				{CLI: "agy", Model: "agy-model-zero"}, // 0% quota
			},
			RunDirs: []string{filepath.Join(tmpDir, "some-allowed-dir")},
		},
	}

	_, err = Run(context.Background(), insufficientQuotaAgent, "hello", optional.None[string](), optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, nil)
	if err == nil {
		t.Error("expected error due to insufficient quota, but got nil")
	} else if !strings.Contains(err.Error(), "no CLI target with more than 10% quota") {
		t.Errorf("expected quota limit error message, got: %v", err)
	}

	// 3. Test case: runDir is not allowed
	_, err = Run(context.Background(), agent, "hello", optional.None[string](), optional.Some(filepath.Join(tmpDir, "disallowed")), optional.None[string](), "test-chat", StatusScope{}, nil)
	if err == nil {
		t.Error("expected error due to disallowed run directory, but got nil")
	} else if !strings.Contains(err.Error(), "is not allowed by agent configuration") {
		t.Errorf("expected disallowed run dir error message, got: %v", err)
	}

	// 4. Test case: runDir is a valid subdirectory
	validSubDir := filepath.Join(tmpDir, "some-allowed-dir", "subdir1")
	_, err = Run(context.Background(), agent, "hello", optional.None[string](), optional.Some(validSubDir), optional.None[string](), "test-chat", StatusScope{}, nil)
	if err != nil {
		t.Fatalf("unexpected error with valid subdirectory: %v", err)
	}
	if _, err := os.Stat(validSubDir); os.IsNotExist(err) {
		t.Errorf("expected subdirectory %s to be created, but it does not exist", validSubDir)
	}

	// 5. Test case: fallback to creating $HOME/tmp/$uuid
	agentWithoutRunDirs := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:          "no-rundirs-agent",
			Name:        "No RunDirs Agent",
			Description: "An agent with no run dirs config",
			CLI: []agentspec.CLITarget{
				{CLI: "agy", Model: "agy-model-high"},
			},
		},
	}
	_, err = Run(context.Background(), agentWithoutRunDirs, "hello", optional.None[string](), optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, nil)
	if err != nil {
		t.Fatalf("unexpected error with fallback runDir: %v", err)
	}
	// Verify that the run directory was created inside $HOME/tmp (which is tmpDir/tmp in our test env)
	tmpPath := filepath.Join(tmpDir, "tmp")
	files, err := os.ReadDir(tmpPath)
	if err != nil {
		t.Fatalf("failed to read tmp dir: %v", err)
	}
	if len(files) == 0 {
		t.Error("expected uuid subdirectory to be created in tmp, but it was empty")
	}

	// 5b. Test case: agent without run_dirs receiving runDirOpt is rejected by strict allowlist
	unallowedDir := filepath.Join(tmpDir, "some-arbitrary-dir")
	_, err = Run(context.Background(), agentWithoutRunDirs, "hello", optional.None[string](), optional.Some(unallowedDir), optional.None[string](), "test-chat", StatusScope{}, nil)
	if err == nil {
		t.Error("expected error due to unallowed run directory on agent with no RunDirs, but got nil")
	} else if !strings.Contains(err.Error(), "is not allowed by agent configuration") {
		t.Errorf("expected disallowed run dir error message, got: %v", err)
	}

	// 6. Test case: explicitly selecting model with available quota
	out, err = Run(context.Background(), agent, "hello agent", optional.None[string](), optional.None[string](), optional.Some("opencode-model-high"), "test-chat", StatusScope{}, nil)
	if err != nil {
		t.Fatalf("unexpected error running explicitly selected model: %v", err)
	}
	if !strings.Contains(string(out), "opencode-model-high") {
		t.Errorf("expected output to contain opencode-model-high, got: %s", string(out))
	}

	// 7. Test case: explicitly selecting model with no quota (0% or <= 0) should error without fallback
	agentWithZeroQuota := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:          "zero-quota-agent",
			Name:        "Zero Quota Agent",
			Description: "An agent with zero quota target",
			CLI: []agentspec.CLITarget{
				{CLI: "agy", Model: "agy-model-zero"},
			},
			RunDirs: []string{filepath.Join(tmpDir, "some-allowed-dir")},
		},
	}
	_, err = Run(context.Background(), agentWithZeroQuota, "hello agent", optional.None[string](), optional.None[string](), optional.Some("agy-model-zero"), "test-chat", StatusScope{}, nil)
	if err == nil {
		t.Error("expected error for model with zero quota when explicitly selected, but got nil")
	} else if !strings.Contains(err.Error(), "has no quota remaining") {
		t.Errorf("expected 'has no quota remaining' error, got: %v", err)
	}

	// 8. Test case: run with custom config injecting language rules into prompt file
	confWithLangs := &config.Config{
		ChatLang:    "Japanese",
		DocLang:     "Japanese",
		CommentLang: "English",
	}
	out, err = Run(context.Background(), agent, "hello agent", optional.None[string](), optional.None[string](), optional.None[string](), "test-chat-lang", StatusScope{}, confWithLangs)
	if err != nil {
		t.Fatalf("unexpected error running agent with custom language config: %v", err)
	}
	if !strings.Contains(string(out), "mock bwrap execution succeeded") {
		t.Errorf("expected mock output, got: %q", string(out))
	}
	langPromptPath := filepath.Join(tmpDir, "tmp", "test-chat-lang", ".asgard_system_prompt")
	langPromptContent, err := os.ReadFile(langPromptPath)
	if err != nil {
		t.Fatalf("failed to read generated prompt file: %v", err)
	}
	if !strings.Contains(string(langPromptContent), "Responses/Conversations: Japanese") {
		t.Errorf("expected prompt file to contain 'Responses/Conversations: Japanese', got: %s", string(langPromptContent))
	}
	if !strings.Contains(string(langPromptContent), "Documents and Artifacts: Japanese") {
		t.Errorf("expected prompt file to contain 'Documents and Artifacts: Japanese', got: %s", string(langPromptContent))
	}
	if !strings.Contains(string(langPromptContent), "Code Comments and Docstrings: English") {
		t.Errorf("expected prompt file to contain 'Code Comments and Docstrings: English', got: %s", string(langPromptContent))
	}
}

func setupTestRunEnv(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	origHome := os.Getenv("HOME")
	origGopath := os.Getenv("GOPATH")
	origGocache := os.Getenv("GOCACHE")

	if origGopath != "" {
		t.Setenv("GOPATH", origGopath)
	} else if origHome != "" {
		t.Setenv("GOPATH", filepath.Join(origHome, "go"))
	}
	if origGocache != "" {
		t.Setenv("GOCACHE", origGocache)
	} else if origHome != "" {
		t.Setenv("GOCACHE", filepath.Join(origHome, ".cache", "go-build"))
	}

	t.Setenv("HOME", tmpDir)

	for _, subDir := range []string{".gemini", ".cache", ".config", ".local"} {
		if err := os.MkdirAll(filepath.Join(tmpDir, subDir), 0755); err != nil {
			t.Fatalf("failed to create %s dir: %v", subDir, err)
		}
	}

	mockBwrapPath := filepath.Join(tmpDir, "bwrap")
	scriptContent := "#!/bin/sh\nfor arg in \"$@\"; do\n  echo \"$arg\"\ndone\necho \"mock bwrap execution succeeded\"\n"
	if err := os.WriteFile(mockBwrapPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to write mock bwrap script: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath)

	fakeAgy := &agentwrapper.FakeClient{
		UsageFunc: func(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
			return []types.ModelUsage{
				{Model: "agy-model-zero", Remaining: 0.0},
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
	t.Cleanup(func() { agentwrapper.SetClients(nil) })

	return tmpDir
}

func newTestAgent(t *testing.T, tmpDir string, cliTargets []agentspec.CLITarget) *agentspec.Agent {
	t.Helper()
	allowedDir := filepath.Join(tmpDir, "allowed-dir")
	require.NoError(t, os.MkdirAll(allowedDir, 0755))
	return &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:      "test-agent",
			Name:    "Test Agent",
			CLI:     cliTargets,
			RunDirs: []string{allowedDir},
		},
	}
}

func TestRun_ExplicitModel_ProviderDisabled(t *testing.T) {
	tmpDir := setupTestRunEnv(t)
	agent := newTestAgent(t, tmpDir, []agentspec.CLITarget{
		{CLI: "opencode", Model: "opencode-model-high"},
	})

	conf := &config.Config{
		Providers: []string{"simplest"},
	}

	_, err := Run(context.Background(), agent, "hello", optional.None[string](), optional.None[string](), optional.Some("opencode-model-high"), "test-chat", StatusScope{}, conf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `provider "opencode" for model "opencode-model-high" is disabled in configuration`)
}

func TestRun_AutoSelection_FallbackSkipDisabledProvider(t *testing.T) {
	tmpDir := setupTestRunEnv(t)
	agent := newTestAgent(t, tmpDir, []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-high"},
		{CLI: "opencode", Model: "opencode-model-high"},
	})

	conf := &config.Config{
		Providers: []string{"opencode"},
	}

	out, err := Run(context.Background(), agent, "hello", optional.None[string](), optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, conf)
	require.NoError(t, err)
	assert.Contains(t, string(out), "opencode-model-high")
}

func TestRun_AutoSelection_AllProvidersDisabled(t *testing.T) {
	tmpDir := setupTestRunEnv(t)
	agent := newTestAgent(t, tmpDir, []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-high"},
		{CLI: "opencode", Model: "opencode-model-high"},
	})

	conf := &config.Config{
		Providers: []string{"simplest"},
	}

	_, err := Run(context.Background(), agent, "hello", optional.None[string](), optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, conf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no enabled CLI targets available for agent test-agent")
}

func TestRun_NoQuotaError_AutoSelection_TypedSnapshot(t *testing.T) {
	tmpDir := setupTestRunEnv(t)
	agent := newTestAgent(t, tmpDir, []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-zero"}, // 0% quota
	})

	_, err := Run(context.Background(), agent, "hello", optional.None[string](), optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, nil)
	require.Error(t, err)

	var nq *NoQuotaError
	require.ErrorAs(t, err, &nq)
	assert.Equal(t, "test-agent", nq.AgentID)
	assert.Empty(t, nq.ExplicitModel)
	assert.InDelta(t, MinAutoQuotaThreshold, nq.MinThreshold, 0.0001)
	require.Len(t, nq.Targets, 1)
	assert.Equal(t, "agy", nq.Targets[0].CLI)
	assert.Equal(t, "agy-model-zero", nq.Targets[0].Model)
	assert.True(t, nq.Targets[0].Enabled)
	assert.InDelta(t, 0.0, nq.Targets[0].Remaining, 0.0001)
	assert.Contains(t, err.Error(), "no CLI target with more than 10% quota")
}

func TestRun_NoQuotaError_ExplicitModel_TypedSnapshot(t *testing.T) {
	tmpDir := setupTestRunEnv(t)
	agent := newTestAgent(t, tmpDir, []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-zero"}, // 0% quota
		{CLI: "agy", Model: "agy-model-high"}, // 50% quota
	})

	_, err := Run(context.Background(), agent, "hello", optional.None[string](), optional.None[string](), optional.Some("agy-model-zero"), "test-chat", StatusScope{}, nil)
	require.Error(t, err)

	var nq *NoQuotaError
	require.ErrorAs(t, err, &nq)
	assert.Equal(t, "agy-model-zero", nq.ExplicitModel)
	assert.Contains(t, err.Error(), "has no quota remaining")
	// Snapshot covers every configured target so the user can switch.
	require.Len(t, nq.Targets, 2)
	assert.InDelta(t, 0.0, nq.Targets[0].Remaining, 0.0001)
	assert.InDelta(t, 0.50, nq.Targets[1].Remaining, 0.0001)
}

func TestRun_NoQuotaError_DisabledProviderMarkedInSnapshot(t *testing.T) {
	tmpDir := setupTestRunEnv(t)
	// agy disabled -> only opencode enabled, and it has quota, so this run
	// succeeds; instead force NoQuota by leaving the sole enabled provider
	// exhausted via an agent whose opencode model is not in the usage list
	// (unknown models report 0 remaining).
	agent := newTestAgent(t, tmpDir, []agentspec.CLITarget{
		{CLI: "agy", Model: "agy-model-high"},
		{CLI: "opencode", Model: "opencode-model-unknown"},
	})

	conf := &config.Config{Providers: []string{"opencode"}}

	_, err := Run(context.Background(), agent, "hello", optional.None[string](), optional.None[string](), optional.None[string](), "test-chat", StatusScope{}, conf)
	require.Error(t, err)

	var nq *NoQuotaError
	require.ErrorAs(t, err, &nq)
	require.Len(t, nq.Targets, 2)
	assert.False(t, nq.Targets[0].Enabled, "agy should be marked disabled")
	assert.Zero(t, nq.Targets[0].Remaining)
	assert.True(t, nq.Targets[1].Enabled)
}
