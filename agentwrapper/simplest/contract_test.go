package simplest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/simplest"
)

func setupCapturedPrompt(t *testing.T, home, runDirName string) (*capturingMockProvider, *simplest.Model) {
	t.Helper()
	t.Setenv("HOME", home)
	testDir := filepath.Join(home, runDirName)
	require.NoError(t, os.MkdirAll(testDir, 0755))

	capturingP := &capturingMockProvider{
		responses: []*simplest.AssistantMessage{{
			Content:    []simplest.AssistantContent{simplest.TextContent{Type: "text", Text: "done."}},
			Usage:      simplest.Usage{Input: 20, Output: 5},
			StopReason: simplest.StopStop,
			Timestamp:  time.Now().UnixMilli(),
		}},
	}
	testModel := &simplest.Model{
		ID: "test-model", Name: "Test Model", Provider: "mock", API: "mock", ContextWindow: 1048576,
	}
	SetProviderResolver(func(modelID string) (*simplest.Model, simplest.Provider, error) {
		return testModel, capturingP, nil
	})
	t.Cleanup(ResetProviderResolver)
	return capturingP, testModel
}

func writeContractFile(t *testing.T, home string, contract *common.Contract) string {
	t.Helper()
	path := filepath.Join(home, "AW_AGENTS.md")
	require.NoError(t, os.WriteFile(path, []byte(common.Render(contract)), 0644))
	t.Setenv(common.EnvVar, path)
	return path
}

func toolNamesOf(t *testing.T, cx *simplest.Context) []string {
	t.Helper()
	var names []string
	for _, td := range cx.Tools {
		names = append(names, td.Name)
	}
	return names
}

func TestPrompt_ContractDrivesIdentity(t *testing.T) {
	home := t.TempDir()
	capturingP, _ := setupCapturedPrompt(t, home, "workspace")
	testDir := filepath.Join(home, "workspace")

	// Host user's own AGENTS.md under ~/.config/simplest (rw-bound into the
	// sandbox) must NOT compete with the contract.
	configDir := filepath.Join(home, ".config", "simplest")
	require.NoError(t, os.MkdirAll(configDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "AGENTS.md"),
		[]byte("HOST_USER_CONTRACT_COMPETITOR"), 0644))

	writeContractFile(t, home, &common.Contract{
		AgentID:    "intend",
		AgentName:  "Intend",
		ToolAccess: types.ToolAccessFull,
		Team:       "core",
		Body:       "CONTRACT_IDENTITY_BODY",
	})

	res, err := Prompt(context.Background(), "run", types.PromptOptions{Dir: testDir})
	require.NoError(t, err)
	assert.Equal(t, "done.", res.LastContent)

	require.NotEmpty(t, capturingP.capturedCtxs)
	sysPrompt := capturingP.capturedCtxs[0].SystemPrompt

	// Contract body wins; CLI protocol header and peer header assembled.
	assert.Contains(t, sysPrompt, "CONTRACT_IDENTITY_BODY")
	assert.Contains(t, sysPrompt, "/bin/ask-user")
	assert.Contains(t, sysPrompt, "call-peer")
	assert.NotContains(t, sysPrompt, "HOST_USER_CONTRACT_COMPETITOR")
	assert.NotContains(t, sysPrompt, "expert coding assistant")
}

func TestPrompt_ContractTeamGatesPeerHeader(t *testing.T) {
	home := t.TempDir()
	capturingP, _ := setupCapturedPrompt(t, home, "workspace")
	testDir := filepath.Join(home, "workspace")

	writeContractFile(t, home, &common.Contract{AgentID: "solo", Body: "SOLO_BODY"})

	res, err := Prompt(context.Background(), "run", types.PromptOptions{Dir: testDir})
	require.NoError(t, err)
	assert.Equal(t, "done.", res.LastContent)

	require.NotEmpty(t, capturingP.capturedCtxs)
	sysPrompt := capturingP.capturedCtxs[0].SystemPrompt
	assert.Contains(t, sysPrompt, "SOLO_BODY")
	assert.Contains(t, sysPrompt, "/bin/ask-user")
	assert.NotContains(t, sysPrompt, "call-peer")
}

func TestPrompt_ContractToolAccessFallback(t *testing.T) {
	home := t.TempDir()
	capturingP, _ := setupCapturedPrompt(t, home, "workspace")
	testDir := filepath.Join(home, "workspace")

	// No --tool-access flag, but the contract declares doc-only.
	writeContractFile(t, home, &common.Contract{AgentID: "intend", ToolAccess: types.ToolAccessDocOnly, Body: "x"})

	_, err := Prompt(context.Background(), "run", types.PromptOptions{Dir: testDir})
	require.NoError(t, err)

	require.NotEmpty(t, capturingP.capturedCtxs)
	names := toolNamesOf(t, capturingP.capturedCtxs[0])
	assert.NotContains(t, names, "bash")
	assert.NotContains(t, names, "edit")
	assert.Contains(t, names, "write_doc")
	assert.Contains(t, names, "read")
}

func TestPrompt_FlagToolAccessWinsOverContract(t *testing.T) {
	home := t.TempDir()
	capturingP, _ := setupCapturedPrompt(t, home, "workspace")
	testDir := filepath.Join(home, "workspace")

	writeContractFile(t, home, &common.Contract{AgentID: "coder", ToolAccess: types.ToolAccessDocOnly, Body: "x"})

	// Explicit flag overrides the contract metadata.
	_, err := Prompt(context.Background(), "run", types.PromptOptions{Dir: testDir, ToolAccess: types.ToolAccessFull})
	require.NoError(t, err)

	require.NotEmpty(t, capturingP.capturedCtxs)
	names := toolNamesOf(t, capturingP.capturedCtxs[0])
	assert.Contains(t, names, "bash")
	assert.Contains(t, names, "edit")
}

func TestPrompt_ContractInvalidToolAccessFailsClosed(t *testing.T) {
	home := t.TempDir()
	_, _ = setupCapturedPrompt(t, home, "workspace")
	testDir := filepath.Join(home, "workspace")

	writeContractFile(t, home, &common.Contract{AgentID: "x", ToolAccess: "banana", Body: "x"})

	_, err := Prompt(context.Background(), "run", types.PromptOptions{Dir: testDir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported tool access mode")
}

func TestFilterAgentCfgFiles(t *testing.T) {
	t.Parallel()

	files := []simplest.ContextFile{
		{Path: "/home/u/.config/simplest/AGENTS.md", Content: "a"},
		{Path: "/home/u/.config/simplest/extra.md", Content: "b"},
		{Path: "/proj/AGENTS.md", Content: "c"},
	}
	kept := filterAgentCfgFiles("/home/u/.config/simplest", files)
	require.Len(t, kept, 1)
	assert.Equal(t, "/proj/AGENTS.md", kept[0].Path)

	assert.Empty(t, filterAgentCfgFiles("/home/u/.config/simplest", nil))
}
