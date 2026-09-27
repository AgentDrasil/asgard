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
	keyContent := `
providers:
  custom-openai:
    apiKey: ${TEST_API_KEY}
  gemini:
    apiKey: "gemini-key"
`
	providersContent := `
providers:
  custom-openai:
    api: openai-compat
    baseUrl: ${TEST_BASE_URL}
    headers:
      X-Custom-Header: "provider-val"
  gemini:
    api: gemini
`
	modelsContent := `
models:
  - id: custom-model-1
    name: "Custom Model 1"
    provider: custom-openai
    model: custom-model-1-expires-on-2026-12-31
    contextWindow: 65536
    maxTokens: 4096
    headers:
      X-Model-Header: "model-val"
  - id: gemini-3.7-flash
    name: "Gemini 3.7 Flash"
    provider: gemini
`
	keyPath := filepath.Join(tempDir, "key.yaml")
	providersPath := filepath.Join(tempDir, "providers.yaml")
	modelsPath := filepath.Join(tempDir, "models.yaml")
	require.NoError(t, os.WriteFile(keyPath, []byte(keyContent), 0o600))
	require.NoError(t, os.WriteFile(providersPath, []byte(providersContent), 0o600))
	require.NoError(t, os.WriteFile(modelsPath, []byte(modelsContent), 0o600))

	cfg, err := LoadFrom(keyPath)
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
	assert.Equal(t, "custom-model-1-expires-on-2026-12-31", m.Model)
	assert.Equal(t, int64(65536), m.ContextWindow)

	// Verify GetAvailableModels
	available := cfg.GetAvailableModels()
	require.Len(t, available, 2)
	assert.Equal(t, "custom-model-1", available[0].ID)
	assert.Equal(t, "custom-model-1-expires-on-2026-12-31", available[0].Model)
	assert.Equal(t, "custom-model-1-expires-on-2026-12-31", available[0].WireID())
	assert.Equal(t, "openai-compat", available[0].API)
	assert.Equal(t, "https://api.openai.com/v1", available[0].BaseURL)
	assert.Equal(t, "provider-val", available[0].Headers["X-Custom-Header"])
	assert.Equal(t, "model-val", available[0].Headers["X-Model-Header"])

	assert.Equal(t, "gemini-3.7-flash", available[1].ID)
	assert.Equal(t, "gemini", available[1].API)
	assert.Equal(t, "gemini-3.7-flash", available[1].WireID()) // no model override: falls back to ID
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

func TestResolveModelAndProvider_MissingProviderAPI(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"broken": {APIKey: "key", BaseURL: "https://example.com/v1"}, // no api declared
		},
		Models: []ModelConfig{
			{ID: "m1", Name: "M1", Provider: "broken", ContextWindow: 128000},
		},
	}
	m, p, err := cfg.ResolveModelAndProvider("m1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no api configured")
	assert.Nil(t, m)
	assert.Nil(t, p)
}

func TestResolveModelAndProvider_MissingProviderEntry(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{},
		Models: []ModelConfig{
			{ID: "m1", Name: "M1", Provider: "ghost", ContextWindow: 128000},
		},
	}
	m, p, err := cfg.ResolveModelAndProvider("m1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no api configured")
	assert.Nil(t, m)
	assert.Nil(t, p)
}

func TestLoad_ReasoningEffort(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "key.yaml")
	providersFile := filepath.Join(tempDir, "providers.yaml")
	modelsFile := filepath.Join(tempDir, "models.yaml")

	keyContent := `
providers:
  deepseek:
    apiKey: "dummy-key"
  zai-coding-plan:
    apiKey: "dummy-key"
`
	providersContent := `
providers:
  deepseek:
    api: openai-compat
    baseUrl: "https://api.deepseek.com"
  zai-coding-plan:
    api: openai-compat
`
	modelsContent := `
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
	require.NoError(t, os.WriteFile(keyFile, []byte(keyContent), 0644))
	require.NoError(t, os.WriteFile(providersFile, []byte(providersContent), 0644))
	require.NoError(t, os.WriteFile(modelsFile, []byte(modelsContent), 0644))

	cfg, err := LoadFrom(keyFile)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	models := cfg.GetAvailableModels()
	require.Len(t, models, 2)

	// Check reasoning_effort snake_case mapping
	assert.Equal(t, "deepseek-v4-flash", models[0].ID)
	assert.Equal(t, []string{"low", "high", "max"}, models[0].ReasoningEffort)
	assert.True(t, models[0].SupportsThinkingLevel("low"))
	assert.True(t, models[0].SupportsThinkingLevel("high"))
	assert.True(t, models[0].SupportsThinkingLevel("max"))
	assert.True(t, models[0].SupportsThinkingLevel("LOW"))
	assert.False(t, models[0].SupportsThinkingLevel("minimal"))
	assert.False(t, models[0].SupportsThinkingLevel("medium"))

	// Check reasoningEffort camelCase mapping
	assert.Equal(t, "glm-5.3", models[1].ID)
	assert.Equal(t, []string{"low", "high"}, models[1].ReasoningEffort)
	assert.True(t, models[1].SupportsThinkingLevel("low"))
	assert.True(t, models[1].SupportsThinkingLevel("high"))
	assert.False(t, models[1].SupportsThinkingLevel("max"))
}

func TestConfig_ModelMetadataRoundTrip(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "key.yaml")
	providersFile := filepath.Join(tempDir, "providers.yaml")
	modelsFile := filepath.Join(tempDir, "models.yaml")

	keyContent := `
providers:
  custom-provider:
    apiKey: "dummy-key"
`
	providersContent := `
providers:
  custom-provider:
    api: openai-compat
`
	modelsContent := `
models:
  - id: advanced-model
    name: "Advanced Model"
    type: chat
    provider: custom-provider
    thinkingLevelMap:
      minimal: null
      low: "low"
      high: "max"
    promptCache:
      short: 1024
      long: 8192
    samplingParams:
      temperature: 0.7
      top_p: 0.95
    inputLimits:
      maxRequestBytes: 10485760
      images:
        maxPerMessage: 5
        maxPerRequest: 10
        resize:
          maxWidth: 1920
          maxHeight: 1080
          maxBytes: 2097152
          jpegQuality: 85
    cost:
      input: 1.5
      output: 3.0
      cacheRead: 0.5
      cacheWrite: 1.0
      tiers:
        - inputTokensAbove: 100000
          input: 1.0
          output: 2.0
          cacheRead: 0.25
          cacheWrite: 0.5
`
	require.NoError(t, os.WriteFile(keyFile, []byte(keyContent), 0o600))
	require.NoError(t, os.WriteFile(providersFile, []byte(providersContent), 0o600))
	require.NoError(t, os.WriteFile(modelsFile, []byte(modelsContent), 0o600))

	cfg, err := LoadFrom(keyFile)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	models := cfg.GetAvailableModels()
	require.Len(t, models, 1)

	m := models[0]
	assert.Equal(t, "advanced-model", m.ID)
	assert.Equal(t, types.ModelTypeChat, m.Type)

	// ThinkingLevelMap: minimal should be nil, low should be "low", high should be "max"
	require.NotNil(t, m.ThinkingLevelMap)
	minVal, hasMin := m.ThinkingLevelMap[types.ThinkingMinimal]
	assert.True(t, hasMin)
	assert.Nil(t, minVal)

	lowVal, hasLow := m.ThinkingLevelMap[types.ThinkingLow]
	assert.True(t, hasLow)
	require.NotNil(t, lowVal)
	assert.Equal(t, "low", *lowVal)

	highVal, hasHigh := m.ThinkingLevelMap[types.ThinkingHigh]
	assert.True(t, hasHigh)
	require.NotNil(t, highVal)
	assert.Equal(t, "max", *highVal)

	// PromptCache
	require.NotNil(t, m.PromptCache)
	assert.Equal(t, 1024, m.PromptCache.Short)
	assert.Equal(t, 8192, m.PromptCache.Long)

	// SamplingParams
	require.NotNil(t, m.SamplingParams)
	assert.Equal(t, 0.7, m.SamplingParams["temperature"])
	assert.Equal(t, 0.95, m.SamplingParams["top_p"])

	// InputLimits
	require.NotNil(t, m.InputLimits)
	assert.Equal(t, int64(10485760), m.InputLimits.MaxRequestBytes)
	require.NotNil(t, m.InputLimits.Images)
	assert.Equal(t, 5, m.InputLimits.Images.MaxPerMessage)
	assert.Equal(t, 10, m.InputLimits.Images.MaxPerRequest)
	require.NotNil(t, m.InputLimits.Images.Resize)
	assert.Equal(t, 1920, m.InputLimits.Images.Resize.MaxWidth)
	assert.Equal(t, 1080, m.InputLimits.Images.Resize.MaxHeight)
	assert.Equal(t, int64(2097152), m.InputLimits.Images.Resize.MaxBytes)
	assert.Equal(t, 85, m.InputLimits.Images.Resize.JPEGQuality)

	// Cost & Tiers
	assert.Equal(t, 1.5, m.Cost.Input)
	assert.Equal(t, 3.0, m.Cost.Output)
	assert.Equal(t, 0.5, m.Cost.CacheRead)
	assert.Equal(t, 1.0, m.Cost.CacheWrite)
	require.Len(t, m.Cost.Tiers, 1)
	assert.Equal(t, int64(100000), m.Cost.Tiers[0].InputTokensAbove)
	assert.Equal(t, 1.0, m.Cost.Tiers[0].Input)
	assert.Equal(t, 2.0, m.Cost.Tiers[0].Output)
	assert.Equal(t, 0.25, m.Cost.Tiers[0].CacheRead)
	assert.Equal(t, 0.5, m.Cost.Tiers[0].CacheWrite)
}

func TestConfig_TypeNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		configured   string
		expectedType types.ModelType
	}{
		{
			name:         "empty type defaults to chat",
			configured:   "",
			expectedType: types.ModelTypeChat,
		},
		{
			name:         "explicit chat type preserved",
			configured:   "chat",
			expectedType: types.ModelTypeChat,
		},
		{
			name:         "explicit image type preserved",
			configured:   "image",
			expectedType: types.ModelTypeImage,
		},
		{
			name:         "explicit classifier type preserved",
			configured:   "classifier",
			expectedType: types.ModelTypeClassifier,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{
				Providers: map[string]ProviderConfig{
					"test-prov": {API: types.APIOpenAICompat},
				},
				Models: []ModelConfig{
					{
						ID:       "m1",
						Provider: "test-prov",
						Type:     tt.configured,
					},
				},
			}

			models := cfg.GetAvailableModels()
			require.Len(t, models, 1)
			assert.Equal(t, tt.expectedType, models[0].Type)
		})
	}
}

func TestConfig_DefaultFallbackConfig(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "dummy-gemini-key")
	t.Setenv("OPENAI_API_KEY", "dummy-openai-key")

	cfg := defaultFallbackConfig()
	require.NotNil(t, cfg)
	require.NotEmpty(t, cfg.Models)

	for _, m := range cfg.Models {
		assert.Equal(t, "chat", m.Type)
	}
}

func TestConfig_ThinkingLevelMap_CaseInsensitiveNormalization(t *testing.T) {
	t.Parallel()

	maxVal := "max"
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"test-prov": {API: types.APIOpenAICompat},
		},
		Models: []ModelConfig{
			{
				ID:        "m1",
				Provider:  "test-prov",
				Reasoning: true,
				ThinkingLevelMap: map[string]*string{
					"High": &maxVal,
				},
			},
		},
	}

	m, _, err := cfg.ResolveModelAndProvider("m1")
	require.NoError(t, err)
	require.NotNil(t, m)

	// Normalized to lowercase ThinkingHigh ("high")
	assert.True(t, m.SupportsThinkingLevel(types.ThinkingHigh))
	level, wireVal, ok := m.ClampThinkingLevel(types.ThinkingHigh)
	assert.True(t, ok)
	assert.Equal(t, types.ThinkingHigh, level)
	require.NotNil(t, wireVal)
	assert.Equal(t, "max", *wireVal)
}

func TestLoadFrom_SplitFiles(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	keyYAML := `providers:
  google:
    apiKey: "test-gemini-key"
`
	providersYAML := `providers:
  google:
    api: "gemini"
`
	modelsYAML := `models:
  - id: "gemini-3.8-flash"
    name: "Gemini 3.8 Flash"
    provider: "google"
    contextWindow: 1048576
`
	keyPath := filepath.Join(tempDir, "key.yaml")
	providersPath := filepath.Join(tempDir, "providers.yaml")
	modelsPath := filepath.Join(tempDir, "models.yaml")

	require.NoError(t, os.WriteFile(keyPath, []byte(keyYAML), 0o600))
	require.NoError(t, os.WriteFile(providersPath, []byte(providersYAML), 0o600))
	require.NoError(t, os.WriteFile(modelsPath, []byte(modelsYAML), 0o600))

	cfg, err := LoadFrom(keyPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	prov, ok := cfg.Providers["google"]
	require.True(t, ok)
	assert.Equal(t, "gemini", prov.API)
	assert.Equal(t, "test-gemini-key", prov.APIKey)

	require.Len(t, cfg.Models, 1)
	assert.Equal(t, "gemini-3.8-flash", cfg.Models[0].ID)
	assert.Equal(t, "Gemini 3.8 Flash", cfg.Models[0].Name)
	assert.Equal(t, "google", cfg.Models[0].Provider)
}

func TestLoadFrom_APIInKeyFileError(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	keyYAML := `providers:
  google:
    api: "gemini"
    apiKey: "test-gemini-key"
`
	keyPath := filepath.Join(tempDir, "key.yaml")
	require.NoError(t, os.WriteFile(keyPath, []byte(keyYAML), 0o600))

	cfg, err := LoadFrom(keyPath)
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "defines api or baseUrl")
}

func TestLoadFrom_InlineModelsError(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configYAML := `providers:
  google:
    apiKey: "test-gemini-key"
models:
  - id: "gemini-3.8-flash"
    provider: "google"
`
	configPath := filepath.Join(tempDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(configYAML), 0o600))

	cfg, err := LoadFrom(configPath)
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "inline models are deprecated")
}
