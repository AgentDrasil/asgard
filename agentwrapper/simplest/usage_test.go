package simplest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/simplest"
)

func TestModelsAndUsage(t *testing.T) {
	// Set mock global config in simplest
	t.Cleanup(func() {
		simplest.ResetGlobalConfig()
	})

	simplest.SetGlobalConfig(&simplest.Config{
		Providers: map[string]simplest.ProviderConfig{
			"gemini": {API: simplest.APIGemini, APIKey: "fake-key"},
		},
		Models: []simplest.ModelConfig{
			{
				ID:            "gemini-3.7-flash",
				Name:          "Gemini 3.7 Flash",
				Provider:      "gemini",
				ContextWindow: 1048576,
			},
		},
	})

	ctx := context.Background()
	opts := types.UsageOptions{}

	models, err := Models(ctx, opts)
	require.NoError(t, err)
	assert.Equal(t, []string{"gemini/gemini-3.7-flash"}, models)

	usages, err := Usage(ctx, opts)
	require.NoError(t, err)
	require.Len(t, usages, 1)
	assert.Equal(t, "gemini/gemini-3.7-flash", usages[0].Model)
	assert.Equal(t, 1.0, usages[0].Remaining)
	assert.Equal(t, int64(0), usages[0].RefreshDate)
}

func TestUsage_ZaiCodingPlan(t *testing.T) {
	// Local mock of the Z.AI quota endpoint so the test never touches the
	// real api.z.ai backend.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/monitor/usage/quota/limit", r.URL.Path)
		assert.Equal(t, "Bearer test-zai-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"code": 200,
			"data": {"limits": [{"type": "TOKENS_LIMIT", "percentage": 25.0, "nextResetTime": 1760000000000}]}
		}`))
	}))
	t.Cleanup(server.Close)

	t.Cleanup(func() {
		simplest.ResetGlobalConfig()
	})

	simplest.SetGlobalConfig(&simplest.Config{
		Providers: map[string]simplest.ProviderConfig{
			"zai-coding-plan": {API: simplest.APIOpenAICompat, APIKey: "test-zai-key", BaseURL: server.URL},
		},
		Models: []simplest.ModelConfig{
			{
				ID:            "glm-5.3",
				Name:          "GLM 5.3",
				Provider:      "zai-coding-plan",
				ContextWindow: 1048576,
			},
		},
	})

	ctx := context.Background()
	opts := types.UsageOptions{Detailed: true}

	models, err := Models(ctx, opts)
	require.NoError(t, err)
	assert.Equal(t, []string{"zai-coding-plan/glm-5.3"}, models)

	usages, err := Usage(ctx, opts)
	require.NoError(t, err)
	require.Len(t, usages, 1)
	assert.Equal(t, "zai-coding-plan/glm-5.3", usages[0].Model)
	assert.InDelta(t, 0.75, usages[0].Remaining, 0.0001)
}

func TestUsage_DeepSeekBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/user/balance", r.URL.Path)
		assert.Equal(t, "Bearer test-ds-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"is_available": true,
			"balance_infos": []map[string]string{
				{
					"currency":          "CNY",
					"total_balance":     "110.00",
					"granted_balance":   "10.00",
					"topped_up_balance": "100.00",
				},
			},
		})
	}))
	t.Cleanup(server.Close)

	t.Cleanup(func() {
		simplest.ResetGlobalConfig()
	})

	simplest.SetGlobalConfig(&simplest.Config{
		Providers: map[string]simplest.ProviderConfig{
			"deepseek": {API: simplest.APIOpenAICompat, APIKey: "test-ds-key", BaseURL: server.URL},
		},
		Models: []simplest.ModelConfig{
			{
				ID:            "deepseek-chat",
				Name:          "DeepSeek Chat",
				Provider:      "deepseek",
				ContextWindow: 1048576,
			},
		},
	})

	ctx := context.Background()
	opts := types.UsageOptions{}

	models, err := Models(ctx, opts)
	require.NoError(t, err)
	assert.Equal(t, []string{"deepseek/deepseek-chat"}, models)

	// The balance must surface through the quota pipeline consumed by the
	// backend's /api/quota handler and the WebUI quota modal.
	usages, err := Usage(ctx, opts)
	require.NoError(t, err)
	require.Len(t, usages, 1)
	assert.Equal(t, "deepseek/deepseek-chat", usages[0].Model)
	assert.Equal(t, 1.0, usages[0].Remaining)
	require.NotNil(t, usages[0].Balance)
	require.True(t, usages[0].Balance.IsAvailable)
	require.Len(t, usages[0].Balance.BalanceInfos, 1)
	assert.Equal(t, "CNY", usages[0].Balance.BalanceInfos[0].Currency)
	assert.Equal(t, "110.00", usages[0].Balance.BalanceInfos[0].TotalBalance)
	assert.Equal(t, "10.00", usages[0].Balance.BalanceInfos[0].GrantedBalance)
	assert.Equal(t, "100.00", usages[0].Balance.BalanceInfos[0].ToppedUpBalance)
}
