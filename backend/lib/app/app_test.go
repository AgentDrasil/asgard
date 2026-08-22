package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/agentwrapper/types"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
)

type mockClient struct {
	models []string
}

func (m *mockClient) Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
	var usages []types.ModelUsage
	for _, model := range m.models {
		usages = append(usages, types.ModelUsage{Model: model, Remaining: 1.0})
	}
	return usages, nil
}

func (m *mockClient) Models(ctx context.Context, opts types.UsageOptions) ([]string, error) {
	return m.models, nil
}

func (m *mockClient) Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	return &types.PromptResult{}, nil
}

func TestMain(m *testing.M) {
	mockClients := map[string]types.CLIClient{
		"agy":      &mockClient{models: []string{"gemini-3.7-flash", "Gemini 3.5 Flash (Low)"}},
		"opencode": &mockClient{models: []string{"deepseek-chat"}},
	}
	agentwrapper.SetClients(mockClients)
	code := m.Run()
	agentwrapper.SetClients(nil)
	os.Exit(code)
}

func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		_ = l.Close()
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func createTestConfig(t *testing.T, tempDir string) *config.Config {
	t.Helper()
	fatherDir := filepath.Join(tempDir, "agents", "agent_father")
	require.NoError(t, os.MkdirAll(fatherDir, 0755))
	teamsYAML := filepath.Join(tempDir, "teams.yaml")
	require.NoError(t, os.WriteFile(teamsYAML, []byte("teams:\n  - my-team\n"), 0644))

	fatherYAML := `
id: "agent_father"
name: "Agent Father"
description: "The agent creates other agents."
team: "my-team"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "gemini-3.7-flash"
`
	require.NoError(t, os.WriteFile(filepath.Join(fatherDir, "config.yaml"), []byte(fatherYAML), 0644))

	port := getFreePort(t)
	internalPort := getFreePort(t)

	return &config.Config{
		Host:                    "127.0.0.1",
		Port:                    port,
		InternalPort:            internalPort,
		DB:                      "sqlite",
		DSN:                     filepath.Join(tempDir, "test.db"),
		AgentDir:                tempDir,
		GeminiAPIKey:            "test-api-key",
		GeminiModelForChatTitle: "test-model",
		Debug:                   true,
	}
}

func TestApp_New_WithOptions(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	conf := createTestConfig(t, tempDir)
	testDB := db.NewDBForTest(t)

	appInstance, err := New(
		WithConfig(conf),
		WithDB(testDB),
		WithSkipAgentValidation(true),
		WithSkipSSHSetup(true),
	)
	require.NoError(t, err)
	require.NotNil(t, appInstance)
	assert.NotNil(t, appInstance.server)
	assert.NotNil(t, appInstance.scheduler)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = appInstance.Stop(ctx)
	})
}

func TestApp_New_WithFunctionAndRunners(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	conf := createTestConfig(t, tempDir)
	testDB := db.NewDBForTest(t)

	customFn := func(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
		return "test result", nil
	}

	customReg := workflow.NewFunctionRegistry()
	customReg.Register("reg_fn", customFn)

	appInstance, err := New(
		WithConfig(conf),
		WithDB(testDB),
		WithSkipAgentValidation(true),
		WithSkipSSHSetup(true),
		WithFunctionRegistry(customReg),
		WithFunction("custom_fn", customFn),
		WithNodeRunner(nil), // test nil tolerance
		WithCustomRunners(nil),
	)
	require.NoError(t, err)
	require.NotNil(t, appInstance)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = appInstance.Stop(ctx)
	})
}

func TestApp_Run_GracefulShutdownAndCleanup(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	conf := createTestConfig(t, tempDir)
	testDB := db.NewDBForTest(t)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(
			ctx,
			WithConfig(conf),
			WithDB(testDB),
			WithSkipAgentValidation(true),
			WithSkipSSHSetup(true),
		)
	}()

	// Wait for server to start by polling the public port
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(conf.Host, fmt.Sprintf("%d", conf.Port)), 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "server did not bind port in time")

	// Cancel context to trigger graceful shutdown
	cancel()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit within timeout after context cancellation")
	}
}

func TestApp_Stop_Idempotency(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	conf := createTestConfig(t, tempDir)
	testDB := db.NewDBForTest(t)

	appInstance, err := New(
		WithConfig(conf),
		WithDB(testDB),
		WithSkipAgentValidation(true),
		WithSkipSSHSetup(true),
	)
	require.NoError(t, err)
	require.NotNil(t, appInstance)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Call Stop concurrently and sequentially
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := appInstance.Stop(ctx)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	// Sequential calls
	assert.NoError(t, appInstance.Stop(ctx))
	assert.NoError(t, appInstance.Stop(ctx))

	// Calling Stop on nil App should be safe
	var nilApp *App
	assert.NoError(t, nilApp.Stop(ctx))
}
