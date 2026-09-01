package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/llms/zai"
	"github.com/AgentDrasil/asgard/simplest/internal/config"
	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func TestLoadZaiCodingPlanToken_FromConfig(t *testing.T) {
	t.Parallel()

	// Case 1: From cfg.Providers["zai-coding-plan"]
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"zai-coding-plan": {APIKey: "key-from-config"},
		},
	}
	assert.Equal(t, "key-from-config", LoadZaiCodingPlanToken(cfg))

	// Case 2: From generic cfg.Providers["zai"] (should NOT be picked up as coding-plan)
	cfg2 := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"zai": {APIKey: "key-from-zai"},
		},
	}
	assert.Equal(t, "", LoadZaiCodingPlanToken(cfg2))

	// Case 3: Empty / nil config
	assert.Equal(t, "", LoadZaiCodingPlanToken(nil))
}

func TestIsZaiCodingPlan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		provider string
		modelID  string
		want     bool
	}{
		{"zai-coding-plan", "glm-5.3", true},
		{"", "zai-coding-plan/glm-5.3", true},
		{"", "zai-coding-plan", true},
		{"zai", "glm-5.3", false},
		{"zaixxx", "glm-5.3", false},
		{"openai", "gpt-4o", false},
	}

	for _, tt := range tests {
		got := isZaiCodingPlan(tt.provider, tt.modelID)
		assert.Equal(t, tt.want, got, "isZaiCodingPlan(%q, %q)", tt.provider, tt.modelID)
	}
}

func TestGetModelUsages_WithZaiCodingPlanAndGenericAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			Success bool `json:"success"`
			Code    int  `json:"code"`
			Data    struct {
				Limits []zai.Limit `json:"limits"`
			} `json:"data"`
		}{
			Success: true,
			Code:    200,
			Data: struct {
				Limits []zai.Limit `json:"limits"`
			}{
				Limits: []zai.Limit{
					{
						Type:          "TOKENS_LIMIT",
						Percentage:    10.0,
						NextResetTime: 1760000000000,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"gemini":          {API: types.APIGemini, APIKey: "gem-key"},
			"zai":             {API: types.APIOpenAICompat, APIKey: "generic-zai-api-key"},
			"zai-coding-plan": {API: types.APIOpenAICompat, APIKey: "zai-plan-key"},
		},
		Models: []config.ModelConfig{
			{
				ID:       "gemini-3.7-flash",
				Provider: "gemini",
			},
			{
				ID:       "glm-4-plus",
				Provider: "zai", // Generic API pay-as-you-go: should NOT check quota
			},
			{
				ID:       "glm-5.3",
				Provider: "zai-coding-plan", // Coding Plan: should check quota
			},
		},
	}

	ctx := context.Background()
	usages, err := GetModelUsages(ctx, cfg, types.UsageOptions{Detailed: true}, server.URL)
	require.NoError(t, err)
	require.Len(t, usages, 3)

	// Gemini model usage
	assert.Equal(t, "gemini/gemini-3.7-flash", usages[0].Model)
	assert.Equal(t, 1.0, usages[0].Remaining)
	assert.Equal(t, int64(0), usages[0].RefreshDate)

	// Generic Zai API model usage (no quota fetch, default Remaining = 1.0)
	assert.Equal(t, "zai/glm-4-plus", usages[1].Model)
	assert.Equal(t, 1.0, usages[1].Remaining)
	assert.Equal(t, int64(0), usages[1].RefreshDate)

	// Zai Coding Plan model usage (quota fetched)
	assert.Equal(t, "zai-coding-plan/glm-5.3", usages[2].Model)
	assert.InDelta(t, 0.90, usages[2].Remaining, 0.0001)
	assert.Equal(t, int64(1760000000), usages[2].RefreshDate)
	require.Len(t, usages[2].Limits, 1)
	assert.Equal(t, "TOKENS_LIMIT", usages[2].Limits[0].Name)
}
