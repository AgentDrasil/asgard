package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func TestLoad_FromPath(t *testing.T) {
	t.Setenv("TEST_API_KEY", "sk-secret-12345")
	t.Setenv("TEST_BASE_URL", "https://api.openai.com/v1")

	tempDir := t.TempDir()
	configContent := `
providers:
  custom-openai:
    api: openai-completions
    apiKey: ${TEST_API_KEY}
    baseUrl: ${TEST_BASE_URL}
    headers:
      X-Custom-Header: "provider-val"
models:
  - id: custom-model-1
    name: "Custom Model 1"
    provider: custom-openai
    contextWindow: 65536
    maxTokens: 4096
    headers:
      X-Model-Header: "model-val"
whitelist:
  - "custom-model-1"
`
	configPath := filepath.Join(tempDir, "config.yaml")
	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoError(t, err)

	cfg, err := LoadFrom(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify provider & env expansion
	prov, ok := cfg.Providers["custom-openai"]
	require.True(t, ok)
	assert.Equal(t, "openai-completions", prov.API)
	assert.Equal(t, "sk-secret-12345", prov.APIKey)
	assert.Equal(t, "https://api.openai.com/v1", prov.BaseURL)
	assert.Equal(t, "provider-val", prov.Headers["X-Custom-Header"])

	// Verify model
	require.Len(t, cfg.Models, 1)
	m := cfg.Models[0]
	assert.Equal(t, "custom-model-1", m.ID)
	assert.Equal(t, int64(65536), m.ContextWindow)

	// Verify whitelist
	assert.True(t, cfg.IsModelAllowed("custom-model-1"))
	assert.False(t, cfg.IsModelAllowed("other-model"))

	// Verify GetAvailableModels
	available := cfg.GetAvailableModels()
	require.Len(t, available, 1)
	assert.Equal(t, "custom-model-1", available[0].ID)
	assert.Equal(t, "openai-completions", available[0].API)
	assert.Equal(t, "https://api.openai.com/v1", available[0].BaseURL)
	assert.Equal(t, "provider-val", available[0].Headers["X-Custom-Header"])
	assert.Equal(t, "model-val", available[0].Headers["X-Model-Header"])
}

func TestLoad_FailClosed_CorruptedYAML(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "invalid.yaml")
	err := os.WriteFile(configPath, []byte("providers:\n  broken_yaml: [unclosed"), 0o600)
	require.NoError(t, err)

	cfg, err := LoadFrom(configPath)
	require.Error(t, err)
	assert.Nil(t, cfg)
}

func TestLoad_FailClosed_InvalidRegex(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "invalid_regex.yaml")
	configContent := `
models:
  - id: gpt-4o
whitelist:
  - "[unclosed-bracket("
`
	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	require.NoError(t, err)

	cfg, err := LoadFrom(configPath)
	require.Error(t, err)
	assert.Nil(t, cfg)
}

func TestWhitelist_Filtering(t *testing.T) {
	tests := []struct {
		name      string
		whitelist []string
		modelID   string
		allowed   bool
	}{
		{
			name:      "empty whitelist allows all",
			whitelist: []string{},
			modelID:   "any-model-id",
			allowed:   true,
		},
		{
			name:      "exact match case-insensitive",
			whitelist: []string{"GPT-4O", "gemini-3.7-flash"},
			modelID:   "gpt-4o",
			allowed:   true,
		},
		{
			name:      "model with dot and slash",
			whitelist: []string{`zai-coding-plan/glm-5\.3`, "claude-3-5-sonnet-.*"},
			modelID:   "zai-coding-plan/glm-5.3",
			allowed:   true,
		},
		{
			name:      "wildcard regex match",
			whitelist: []string{"gemini-3.*"},
			modelID:   "gemini-3.7-flash",
			allowed:   true,
		},
		{
			name:      "anchored regex does not prefix match unintended models",
			whitelist: []string{"gemini-3.7-flash"},
			modelID:   "gemini-3.7-flash-extra",
			allowed:   false,
		},
		{
			name:      "unmatched model blocked",
			whitelist: []string{"gpt-4o"},
			modelID:   "gemini-3.7-flash",
			allowed:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Whitelist: tt.whitelist}
			// Precompile
			for _, p := range tt.whitelist {
				if p == "" {
					continue
				}
				rx, err := regexpCompile("(?i)^(?:" + p + ")$")
				if err == nil {
					cfg.compiledWhitelist = append(cfg.compiledWhitelist, rx)
				}
			}

			assert.Equal(t, tt.allowed, cfg.IsModelAllowed(tt.modelID))
		})
	}
}

func regexpCompile(p string) (*regexp.Regexp, error) {
	return regexp.Compile(p)
}

func TestDefaultFallback_MissingConfig(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("SIMPLEST_CONFIG_PATH", filepath.Join(tempDir, "non_existent_config.yaml"))
	t.Setenv("GEMINI_API_KEY", "test-gemini-key")
	t.Setenv("OPENAI_API_KEY", "test-openai-key")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	available := cfg.GetAvailableModels()
	require.Len(t, available, 2)

	assert.Equal(t, "gemini-3.7-flash", available[0].ID)
	assert.Equal(t, types.APIGoogleGenerativeAI, available[0].API)
	assert.Equal(t, "google", available[0].Provider)

	assert.Equal(t, "gpt-4o", available[1].ID)
	assert.Equal(t, types.APIOpenAICompat, available[1].API)
	assert.Equal(t, "openai", available[1].Provider)
}

func TestResolveModelAndProvider(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"google": {
				API:    types.APIGoogleGenerativeAI,
				APIKey: "gemini-secret-key",
			},
			"openai": {
				API:     types.APIOpenAICompat,
				APIKey:  "openai-secret-key",
				BaseURL: "https://custom.openai.api/v1",
			},
		},
		Models: []ModelConfig{
			{
				ID:            "gemini-3.7-flash",
				Name:          "Gemini Flash",
				Provider:      "google",
				ContextWindow: 1048576,
			},
			{
				ID:            "gpt-4o",
				Name:          "GPT 4o",
				Provider:      "openai",
				ContextWindow: 128000,
			},
		},
		Whitelist: []string{"gemini-3.7-flash", "gpt-4o"},
	}

	// 1. Resolve default (empty string)
	mDefault, pDefault, err := cfg.ResolveModelAndProvider("")
	require.NoError(t, err)
	require.NotNil(t, mDefault)
	require.NotNil(t, pDefault)
	assert.Equal(t, "gemini-3.7-flash", mDefault.ID)

	// 2. Resolve OpenAI model
	mOpenAI, pOpenAI, err := cfg.ResolveModelAndProvider("gpt-4o")
	require.NoError(t, err)
	require.NotNil(t, mOpenAI)
	require.NotNil(t, pOpenAI)
	assert.Equal(t, "gpt-4o", mOpenAI.ID)
	assert.Equal(t, types.APIOpenAICompat, mOpenAI.API)

	// 3. Resolve non-whitelisted / missing model
	mErr, pErr, err := cfg.ResolveModelAndProvider("claude-3-opus")
	require.Error(t, err)
	assert.Nil(t, mErr)
	assert.Nil(t, pErr)
}
