package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestState_SaveAndLoad(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	state := newInitState()
	state.TargetDir = tempDir
	state.Host = "https://asgard.example.com"
	state.BackendPort = 8080
	deployEtcd := true
	state.DeployEtcd = &deployEtcd
	state.EtcdEndpoints = "http://10.0.0.1:2379"
	state.EtcdPrefix = "/custom/prefix"
	state.AuthIssuer = "https://idp.example.com"
	state.AuthClientID = "client-123"
	state.AuthClientSecret = "secret-xyz"
	state.InitialRunner = "worker-primary"
	state.WorkspaceMounts = []string{"/host/src:/container/src"}
	state.GCPKMS = "projects/my-proj/locations/global/keyRings/kr/cryptoKeys/k"
	state.GCPSAKey = "/path/to/sa.json"
	state.Providers = []string{"agy", "claude"}

	state.MarkCompleted("target_dir")
	state.MarkCompleted("host")
	state.MarkCompleted("port")

	require.NoError(t, state.Save(tempDir))

	stateFile := filepath.Join(tempDir, stateFileName)
	require.FileExists(t, stateFile)

	loadedPath, loaded, err := loadState(tempDir)
	require.NoError(t, err)
	assert.Equal(t, stateFile, loadedPath)
	assert.Equal(t, state.TargetDir, loaded.TargetDir)
	assert.Equal(t, state.Host, loaded.Host)
	assert.Equal(t, state.BackendPort, loaded.BackendPort)
	assert.Equal(t, state.DeployEtcd, loaded.DeployEtcd)

	// Verify persistence of DeployEtcd = false
	deployEtcdFalse := false
	state.DeployEtcd = &deployEtcdFalse
	require.NoError(t, state.Save(tempDir))
	_, loadedWithFalse, err := loadState(tempDir)
	require.NoError(t, err)
	require.NotNil(t, loadedWithFalse.DeployEtcd)
	assert.False(t, *loadedWithFalse.DeployEtcd)
	assert.Equal(t, state.EtcdEndpoints, loaded.EtcdEndpoints)
	assert.Equal(t, state.EtcdPrefix, loaded.EtcdPrefix)
	assert.Equal(t, state.AuthIssuer, loaded.AuthIssuer)
	assert.Equal(t, state.AuthClientID, loaded.AuthClientID)
	assert.Equal(t, state.AuthClientSecret, loaded.AuthClientSecret)
	assert.Equal(t, state.InitialRunner, loaded.InitialRunner)
	assert.Equal(t, state.WorkspaceMounts, loaded.WorkspaceMounts)
	assert.Equal(t, state.GCPKMS, loaded.GCPKMS)
	assert.Equal(t, state.GCPSAKey, loaded.GCPSAKey)
	assert.Equal(t, state.Providers, loaded.Providers)
	assert.True(t, loaded.IsCompleted("target_dir"))
	assert.True(t, loaded.IsCompleted("host"))
	assert.True(t, loaded.IsCompleted("port"))
	assert.False(t, loaded.IsCompleted("sops"))

	removeState(tempDir)
	assert.NoFileExists(t, stateFile)
}

func TestState_Resume_FlagWithMissingState_ReturnsError(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	err := run([]string{"--dir", tempDir, "--resume"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no saved state file found to resume")
}

func TestState_InterruptedRun_PreservesStateAndResumes(t *testing.T) {
	// Not t.Parallel() because tests mutate shared defaultStdinReader and promptUser
	tempDir := t.TempDir()
	deployDir := filepath.Join(tempDir, "deploy-resume")

	origStdin := defaultStdinReader
	origPromptUser := promptUser
	t.Cleanup(func() {
		defaultStdinReader = origStdin
		promptUser = origPromptUser
		removeState(deployDir)
	})

	// Part 1: Simulate user answering target_dir, host, port, etcd_endpoints, and then Ctrl+C
	callCount := 0
	promptUser = func(r io.Reader, question, def string, opts ...PromptOption) (string, error) {
		callCount++
		if callCount > 5 {
			return "", ErrPromptCancelled
		}
		return origPromptUser(r, question, def, opts...)
	}

	part1Inputs := []string{
		deployDir,                    // 1. target directory
		"https://myasgard.test:7000", // 2. host
		"7777",                       // 3. port
		"Y",                          // 4. deploy etcd in compose
		"http://etcd-cluster:2379",   // 5. etcd endpoints
	}
	defaultStdinReader = bufioNewReader(strings.NewReader(strings.Join(part1Inputs, "\n") + "\n"))

	err := run([]string{"--dir", deployDir})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPromptCancelled)

	// Verify state file was saved to deployDir/.asgard-init-state.json
	statePath := filepath.Join(deployDir, stateFileName)
	require.FileExists(t, statePath)

	_, savedState, err := loadState(deployDir)
	require.NoError(t, err)
	assert.True(t, savedState.IsCompleted("target_dir"))
	assert.True(t, savedState.IsCompleted("host"))
	assert.True(t, savedState.IsCompleted("port"))
	assert.True(t, savedState.IsCompleted("deploy_etcd"))
	assert.True(t, savedState.IsCompleted("etcd_endpoints"))
	assert.False(t, savedState.IsCompleted("etcd_prefix"))
	assert.Equal(t, "https://myasgard.test:7000", savedState.Host)
	assert.Equal(t, 7777, savedState.BackendPort)
	require.NotNil(t, savedState.DeployEtcd)
	assert.True(t, *savedState.DeployEtcd)
	assert.Equal(t, "http://etcd-cluster:2379", savedState.EtcdEndpoints)

	// Part 2: Resume with --resume flag
	// Restore standard promptUser so it runs to completion
	promptUser = origPromptUser

	// Notice: wizard will NOT ask for target_dir, host, port, etcd_endpoints!
	// It resumes directly from etcd_prefix!
	part2Inputs := []string{
		"/myasgard/configs", // etcd prefix
		// OIDC:
		"", "", "",
		// Runner:
		"", "",
		// SOPS (Age):
		"", "",
		// Providers:
		"s", "n", "s", "n", "s", "n", "s", "n",
		// SSH:
		"s",
	}
	defaultStdinReader = bufioNewReader(strings.NewReader(strings.Join(part2Inputs, "\n") + "\n"))

	err = run([]string{"--dir", deployDir, "--resume"})
	require.NoError(t, err)

	// Verify deployment generated with values from BOTH part 1 and part 2
	cfgData, err := os.ReadFile(filepath.Join(deployDir, "etcd-tree", "asgard", "config.yaml"))
	require.NoError(t, err)
	cfgStr := string(cfgData)
	assert.Contains(t, cfgStr, `host: "https://myasgard.test:7000"`)
	assert.Contains(t, cfgStr, `port: 7777`)

	composeData, err := os.ReadFile(filepath.Join(deployDir, "compose.yaml"))
	require.NoError(t, err)
	composeStr := string(composeData)
	assert.Contains(t, composeStr, `- ETCD_ENDPOINTS=http://etcd-cluster:2379`)
	assert.Contains(t, composeStr, `- ETCD_PREFIX=/myasgard/configs`)

	// Verify temporary state file was removed on completion!
	assert.NoFileExists(t, statePath)
}

func TestState_InteractiveResumePrompt_DeclineStartsFresh(t *testing.T) {
	// Not t.Parallel() because tests mutate shared defaultStdinReader
	tempDir := t.TempDir()
	deployDir := filepath.Join(tempDir, "deploy-decline")

	origStdin := defaultStdinReader
	t.Cleanup(func() {
		defaultStdinReader = origStdin
		removeState(deployDir)
	})

	// Pre-seed an existing state file
	seedState := newInitState()
	seedState.TargetDir = deployDir
	seedState.Host = "https://stale-host.example.com"
	seedState.BackendPort = 9999
	seedState.MarkCompleted("target_dir")
	seedState.MarkCompleted("host")
	seedState.MarkCompleted("port")
	require.NoError(t, seedState.Save(deployDir))

	statePath := filepath.Join(deployDir, stateFileName)
	require.FileExists(t, statePath)

	// Interactive run:
	// 1. "Saved deployment state detected. Resume previous session? [Y/n]" -> "n"
	// 2. Starts fresh! Prompts targetDir, host, port...
	inputs := []string{
		"n",                     // decline resume!
		deployDir,               // targetDir
		"https://fresh-host.io", // host
		"7001",                  // port
		"",                      // deploy etcd in docker compose
		"",                      // etcd endpoints
		"",                      // etcd prefix
		"", "", "",              // OIDC
		"", "", // Runner
		"", "", // SOPS
		"s", "n", "s", "n", "s", "n", "s", "n", // providers
		"s", // ssh
	}
	defaultStdinReader = bufioNewReader(strings.NewReader(strings.Join(inputs, "\n") + "\n"))

	err := run([]string{"--dir", deployDir})
	require.NoError(t, err)

	cfgData, err := os.ReadFile(filepath.Join(deployDir, "etcd-tree", "asgard", "config.yaml"))
	require.NoError(t, err)
	cfgStr := string(cfgData)
	assert.Contains(t, cfgStr, `host: "https://fresh-host.io"`)
	assert.NotContains(t, cfgStr, `stale-host`)

	// State file cleaned up
	assert.NoFileExists(t, statePath)
}

func TestState_CtrlCOnFirstPrompt_SavesStateToCwdAndTargetDir(t *testing.T) {
	tempDir := t.TempDir()
	deployDir := filepath.Join(tempDir, "deploy-ctrlc")

	origStdin := defaultStdinReader
	origPromptUser := promptUser
	t.Cleanup(func() {
		defaultStdinReader = origStdin
		promptUser = origPromptUser
		removeState(deployDir)
	})

	// Abort on the very first prompt
	promptUser = func(r io.Reader, question, def string, opts ...PromptOption) (string, error) {
		return "", ErrPromptCancelled
	}
	defaultStdinReader = bufioNewReader(strings.NewReader("\n"))

	err := run([]string{"--dir", deployDir, "--host", "https://preconfigured.test"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPromptCancelled)

	// State file should exist in deployDir AND in cwd
	deployStatePath := filepath.Join(deployDir, stateFileName)
	require.FileExists(t, deployStatePath)

	cwd, cwdErr := os.Getwd()
	require.NoError(t, cwdErr)
	cwdStatePath := filepath.Join(cwd, stateFileName)
	require.FileExists(t, cwdStatePath)

	// Check saved data has initial flags/defaults
	data, err := os.ReadFile(cwdStatePath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "https://preconfigured.test")
}
