package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/config"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/simplest"
)

func TestSimplestModelsRunE(t *testing.T) {
	simplest.SetGlobalConfig(&simplest.Config{
		Models: []simplest.ModelConfig{
			{ID: "test-model", Provider: "test-provider"},
		},
	})
	defer simplest.ResetGlobalConfig()

	GlobalConfig = nil
	defer func() { GlobalConfig = nil }()

	buf := new(bytes.Buffer)
	simplestModelsCmd.SetOut(buf)
	defer simplestModelsCmd.SetOut(nil)

	err := simplestModelsCmd.RunE(simplestModelsCmd, []string{})
	require.NoError(t, err)

	var models []string
	err = json.Unmarshal(buf.Bytes(), &models)
	require.NoError(t, err)
	assert.NotEmpty(t, models)
}

func TestRootModelsRunE(t *testing.T) {
	fakeClient := &agentwrapper.FakeClient{
		ModelsFunc: func(ctx context.Context, opts types.UsageOptions) ([]string, error) {
			return []string{"mock-1", "mock-2"}, nil
		},
	}

	agentwrapper.SetClients(map[string]types.CLIClient{
		"mock-agent": fakeClient,
	})
	defer agentwrapper.SetClients(nil)

	GlobalConfig = &config.Config{
		Agents: []map[string][]string{
			{"mock-agent": {"mock-1"}},
		},
	}
	defer func() { GlobalConfig = nil }()

	buf := new(bytes.Buffer)
	modelsCmd.SetOut(buf)
	defer modelsCmd.SetOut(nil)

	err := modelsCmd.RunE(modelsCmd, []string{})
	require.NoError(t, err)

	var out map[string][]string
	err = json.Unmarshal(buf.Bytes(), &out)
	require.NoError(t, err)
	assert.Equal(t, []string{"mock-1"}, out["mock-agent"])
}

func TestCommandHierarchy(t *testing.T) {
	subCommands := rootCmd.Commands()
	cmdNames := make(map[string]bool)
	for _, c := range subCommands {
		cmdNames[c.Name()] = true
	}

	assert.True(t, cmdNames["agy"], "rootCmd should have agy command")
	assert.True(t, cmdNames["opencode"], "rootCmd should have opencode command")
	assert.True(t, cmdNames["simplest"], "rootCmd should have simplest command")
	assert.True(t, cmdNames["models"], "rootCmd should have models command")

	// Verify each agent command has models subcommand
	agySubCmds := make(map[string]bool)
	for _, c := range agyCmd.Commands() {
		agySubCmds[c.Name()] = true
	}
	assert.True(t, agySubCmds["models"], "agyCmd should have models subcommand")

	opencodeSubCmds := make(map[string]bool)
	for _, c := range opencodeCmd.Commands() {
		opencodeSubCmds[c.Name()] = true
	}
	assert.True(t, opencodeSubCmds["models"], "opencodeCmd should have models subcommand")

	simplestSubCmds := make(map[string]bool)
	for _, c := range simplestCmd.Commands() {
		simplestSubCmds[c.Name()] = true
	}
	assert.True(t, simplestSubCmds["models"], "simplestCmd should have models subcommand")
}
