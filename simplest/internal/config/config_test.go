package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func TestFullModelName(t *testing.T) {
	assert.Equal(t, "gemini/gemini-3.7-flash", FullModelName("gemini", "gemini-3.7-flash"))
	assert.Equal(t, "deepseek/deepseek-v4-flash", FullModelName("deepseek", "deepseek-v4-flash"))
	assert.Equal(t, "stealth/ox-alpha", FullModelName("stealth", "stealth/ox-alpha"))
	assert.Equal(t, "stealth/ox-alpha", FullModelName("stealth", "ox-alpha"))
	assert.Equal(t, "openrouter/stealth/ox-alpha", FullModelName("openrouter", "stealth/ox-alpha"))
	assert.Equal(t, "glm-5.3", FullModelName("", "glm-5.3"))
}

func TestLoad_FromPath(t *testing.T) {
	t.Setenv("TEST_API_KEY", "sk-secret-12345")
	t.Setenv("TEST_BASE_URL", "https://api.openai.com/v1")

	tempDir := t.TempDir()
	configContent := `
providers:
  custom-openai:
    api: openai-compat
    apiKey: ${TEST_API_KEY}
    baseUrl: ${TEST_BASE_URL}
    headers:
      X-Custom-Header: "provider-val"
  gemini:
    api: gemini
    apiKey: "gemini-key"
models:
  - id: custom-model-1
    name: "Custom Model 1"
    provider: custom-openai
    contextWindow: 65536
    maxTokens: 4096
    headers:
      X-Model-Header: "model-val"
  - id: gemini-3.7-flash
    name: "Gemini 3.7 Flash"
    provider: gemini
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
	assert.Equal(t, "openai-compat", prov.API)
	assert.Equal(t, "sk-secret-12345", prov.APIKey)
	assert.Equal(t, "https://api.openai.com/v1", prov.BaseURL)
	assert.Equal(t, "provider-val", prov.Headers["X-Custom-Header"])

	geminiProv, ok := cfg.Providers["gemini"]
	require.True(t, ok)
	assert.Equal(t, "gemini", geminiProv.API)

	// Verify model
	require.Len(t, cfg.Models, 2)
	m := cfg.Models[0]
	assert.Equal(t, "custom-model-1", m.ID)
	assert.Equal(t, int64(65536), m.ContextWindow)

	// Verify GetAvailableModels
	available := cfg.GetAvailableModels()
	require.Len(t, available, 2)
	assert.Equal(t, "custom-model-1", available[0].ID)
	assert.Equal(t, "openai-compat", available[0].API)
	assert.Equal(t, "https://api.openai.com/v1", available[0].BaseURL)
	assert.Equal(t, "provider-val", available[0].Headers["X-Custom-Header"])
	assert.Equal(t, "model-val", available[0].Headers["X-Model-Header"])

	assert.Equal(t, "gemini-3.7-flash", available[1].ID)
	assert.Equal(t, "gemini", available[1].API)
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
	assert.Equal(t, types.APIGemini, available[0].API)
	assert.Equal(t, "gemini", available[0].Provider)

	assert.Equal(t, "gpt-4o", available[1].ID)
	assert.Equal(t, types.APIOpenAICompat, available[1].API)
	assert.Equal(t, "openai", available[1].Provider)
}

func TestResolveModelAndProvider(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"gemini": {
				API:    types.APIGemini,
				APIKey: "gemini-secret-key",
			},
			"openai": {
				API:     types.APIOpenAICompat,
				APIKey:  "openai-secret-key",
				BaseURL: "https://custom.openai.api/v1",
			},
			"zai-coding-plan": {
				API:     types.APIOpenAICompat,
				APIKey:  "zai-secret-key",
				BaseURL: "https://api.z.ai/v1",
			},
			"openrouter": {
				API:     types.APIOpenAICompat,
				APIKey:  "openrouter-secret-key",
				BaseURL: "https://openrouter.ai/api/v1",
			},
		},
		Models: []ModelConfig{
			{
				ID:            "gemini-3.7-flash",
				Name:          "Gemini Flash",
				Provider:      "gemini",
				ContextWindow: 1048576,
			},
			{
				ID:            "gpt-4o",
				Name:          "GPT 4o",
				Provider:      "openai",
				ContextWindow: 128000,
			},
			{
				ID:            "glm-5.3",
				Name:          "GLM 5.3",
				Provider:      "zai-coding-plan",
				ContextWindow: 1048576,
			},
			{
				ID:            "stealth/ox-alpha",
				Name:          "Stealth OX Alpha",
				Provider:      "openrouter",
				ContextWindow: 131072,
			},
		},
	}

	// 1. Resolve default (empty string)
	mDefault, pDefault, err := cfg.ResolveModelAndProvider("")
	require.NoError(t, err)
	require.NotNil(t, mDefault)
	require.NotNil(t, pDefault)
	assert.Equal(t, "gemini-3.7-flash", mDefault.ID)

	// 2. Resolve Gemini model by provider/model (gemini/gemini-3.7-flash)
	mGemini, pGemini, err := cfg.ResolveModelAndProvider("gemini/gemini-3.7-flash")
	require.NoError(t, err)
	require.NotNil(t, mGemini)
	require.NotNil(t, pGemini)
	assert.Equal(t, "gemini-3.7-flash", mGemini.ID)
	assert.Equal(t, types.APIGemini, mGemini.API)

	// 3. Resolve Gemini model by bare ID
	mGeminiBare, _, err := cfg.ResolveModelAndProvider("gemini-3.7-flash")
	require.NoError(t, err)
	assert.Equal(t, "gemini-3.7-flash", mGeminiBare.ID)

	// 4. Resolve OpenAI model
	mOpenAI, pOpenAI, err := cfg.ResolveModelAndProvider("gpt-4o")
	require.NoError(t, err)
	require.NotNil(t, mOpenAI)
	require.NotNil(t, pOpenAI)
	assert.Equal(t, "gpt-4o", mOpenAI.ID)
	assert.Equal(t, types.APIOpenAICompat, mOpenAI.API)

	// 5. Resolve by provider prefix (zai-coding-plan/glm-5.3)
	mZai, pZai, err := cfg.ResolveModelAndProvider("zai-coding-plan/glm-5.3")
	require.NoError(t, err)
	require.NotNil(t, mZai)
	require.NotNil(t, pZai)
	assert.Equal(t, "glm-5.3", mZai.ID)
	assert.Equal(t, "zai-coding-plan", mZai.Provider)

	// 6. Resolve OpenRouter model with slash in ID (stealth/ox-alpha and openrouter/stealth/ox-alpha)
	mOR, _, err := cfg.ResolveModelAndProvider("stealth/ox-alpha")
	require.NoError(t, err)
	assert.Equal(t, "stealth/ox-alpha", mOR.ID)

	mORFull, _, err := cfg.ResolveModelAndProvider("openrouter/stealth/ox-alpha")
	require.NoError(t, err)
	assert.Equal(t, "stealth/ox-alpha", mORFull.ID)

	// 7. Resolve missing model
	mErr, pErr, err := cfg.ResolveModelAndProvider("claude-3-opus")
	require.Error(t, err)
	assert.Nil(t, mErr)
	assert.Nil(t, pErr)
}

func TestLoad_ReasoningEffort(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "config_reasoning.yaml")

	yamlContent := `
providers:
  deepseek:
    api: openai-compat
    apiKey: "dummy-key"
    baseUrl: "https://api.deepseek.com"
  zai-coding-plan:
    api: openai-compat
    apiKey: "dummy-key"
models:
  - id: deepseek-v4-flash
    name: "DeepSeek V4 Flash"
    provider: deepseek
    reasoning: true
    reasoning_effort:
      - low
      - high
      - max
  - id: glm-5.3
    name: "GLM 5.3"
    provider: zai-coding-plan
    reasoning: true
    reasoningEffort:
      - low
      - high
`
	require.NoError(t, os.WriteFile(configFile, []byte(yamlContent), 0644))

	cfg, err := LoadFrom(configFile)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	models := cfg.GetAvailableModels()
	require.Len(t, models, 2)

	// Check reasoning_effort snake_case mapping
	assert.Equal(t, "deepseek-v4-flash", models[0].ID)
	assert.Equal(t, []string{"low", "high", "max"}, models[0].ReasoningEffort)
	assert.True(t, models[0].SupportsReasoningEffort("low"))
	assert.True(t, models[0].SupportsReasoningEffort("high"))
	assert.True(t, models[0].SupportsReasoningEffort("max"))
	assert.True(t, models[0].SupportsReasoningEffort("LOW"))
	assert.False(t, models[0].SupportsReasoningEffort("minimal"))
	assert.False(t, models[0].SupportsReasoningEffort("medium"))

	// Check reasoningEffort camelCase mapping
	assert.Equal(t, "glm-5.3", models[1].ID)
	assert.Equal(t, []string{"low", "high"}, models[1].ReasoningEffort)
	assert.True(t, models[1].SupportsReasoningEffort("low"))
	assert.True(t, models[1].SupportsReasoningEffort("high"))
	assert.False(t, models[1].SupportsReasoningEffort("max"))
}
