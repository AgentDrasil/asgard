package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/llms"
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

func TestIsDeepSeek(t *testing.T) {
	t.Parallel()

	tests := []struct {
		provider string
		modelID  string
		want     bool
	}{
		{"deepseek", "deepseek-chat", true},
		{"DeepSeek", "deepseek-chat", true},
		{"deepseek", "DeepSeek-V3", true},
		{"openai", "deepseek/deepseek-chat", true},
		{"openai", "DeepSeek/DeepSeek-V3", true},
		{"openai", "gpt-4o", false},
		{"zai-coding-plan", "glm-5.3", false},
	}

	for _, tt := range tests {
		got := isDeepSeek(tt.provider, tt.modelID)
		assert.Equal(t, tt.want, got, "isDeepSeek(%q, %q)", tt.provider, tt.modelID)
	}
}

func TestDeepSeekBalanceURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		base string
		want string
	}{
		{"https://api.deepseek.com", "https://api.deepseek.com/user/balance"},
		{"https://api.deepseek.com/", "https://api.deepseek.com/user/balance"},
		{"https://api.deepseek.com/v1", "https://api.deepseek.com/user/balance"},
		{"https://api.deepseek.com/v1/", "https://api.deepseek.com/user/balance"},
		{"https://api.deepseek.com/V1", "https://api.deepseek.com/user/balance"},
		{"", "https://api.deepseek.com/user/balance"},
		{"https://proxy.example.com", "https://proxy.example.com/user/balance"},
	}

	for _, tt := range tests {
		got := deepSeekBalanceURL(tt.base)
		assert.Equal(t, tt.want, got, "deepSeekBalanceURL(%q)", tt.base)
	}
}

func TestIsBalanceLow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		balance *llms.AccountBalance
		want    bool
	}{
		{
			name:    "nil balance",
			balance: nil,
			want:    false,
		},
		{
			name: "healthy multi-currency balance",
			balance: &llms.AccountBalance{IsAvailable: true, BalanceInfos: []llms.BalanceInfo{
				{Currency: "CNY", TotalBalance: "114.74"},
				{Currency: "USD", TotalBalance: "7.50"},
			}},
			want: false,
		},
		{
			name: "healthy CNY with unused USD (zero) must not warn",
			balance: &llms.AccountBalance{IsAvailable: true, BalanceInfos: []llms.BalanceInfo{
				{Currency: "CNY", TotalBalance: "109.53"},
				{Currency: "USD", TotalBalance: "0.00"},
			}},
			want: false,
		},
		{
			name: "one currency funded below threshold, other healthy: no warn",
			balance: &llms.AccountBalance{IsAvailable: true, BalanceInfos: []llms.BalanceInfo{
				{Currency: "CNY", TotalBalance: "19.99"},
				{Currency: "USD", TotalBalance: "50.00"},
			}},
			want: false,
		},
		{
			name: "all tracked currencies below their thresholds",
			balance: &llms.AccountBalance{IsAvailable: true, BalanceInfos: []llms.BalanceInfo{
				{Currency: "CNY", TotalBalance: "19.99"},
				{Currency: "USD", TotalBalance: "4.99"},
			}},
			want: true,
		},
		{
			name: "only nonzero currency below threshold",
			balance: &llms.AccountBalance{IsAvailable: true, BalanceInfos: []llms.BalanceInfo{
				{Currency: "CNY", TotalBalance: "0.00"},
				{Currency: "USD", TotalBalance: "4.99"},
			}},
			want: true,
		},
		{
			name: "all balances zero",
			balance: &llms.AccountBalance{IsAvailable: true, BalanceInfos: []llms.BalanceInfo{
				{Currency: "CNY", TotalBalance: "0.00"},
				{Currency: "USD", TotalBalance: "0.00"},
			}},
			want: true,
		},
		{
			name: "unknown currency ignored",
			balance: &llms.AccountBalance{IsAvailable: true, BalanceInfos: []llms.BalanceInfo{
				{Currency: "EUR", TotalBalance: "0.00"},
			}},
			want: false,
		},
		{
			name: "unparsable amount ignored",
			balance: &llms.AccountBalance{IsAvailable: true, BalanceInfos: []llms.BalanceInfo{
				{Currency: "CNY", TotalBalance: "abc"},
			}},
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isBalanceLow(tt.balance))
		})
	}
}

func TestGetModelUsages_WithDeepSeekBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/user/balance", r.URL.Path)
		assert.Equal(t, "Bearer ds-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"is_available": true,
			"balance_infos": [
				{"currency": "CNY", "total_balance": "110.00", "granted_balance": "10.00", "topped_up_balance": "100.00"},
				{"currency": "USD", "total_balance": "12.00", "granted_balance": "0.00", "topped_up_balance": "12.00"}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"gemini":   {API: types.APIGemini, APIKey: "gem-key"},
			"deepseek": {API: types.APIOpenAICompat, APIKey: "ds-key", BaseURL: server.URL},
		},
		Models: []config.ModelConfig{
			{ID: "gemini-3.7-flash", Provider: "gemini"},
			{ID: "deepseek-chat", Provider: "deepseek"},
			{ID: "deepseek-reasoner", Provider: "deepseek"},
		},
	}

	ctx := context.Background()
	usages, err := GetModelUsages(ctx, cfg, types.UsageOptions{Detailed: true})
	require.NoError(t, err)
	require.Len(t, usages, 3)

	// Non-deepseek models never carry a balance.
	assert.Equal(t, "gemini/gemini-3.7-flash", usages[0].Model)
	assert.Equal(t, 1.0, usages[0].Remaining)
	assert.Nil(t, usages[0].Balance)

	// The balance is fetched once and attached to every deepseek model.
	assert.Equal(t, "deepseek/deepseek-chat", usages[1].Model)
	assert.Equal(t, "deepseek/deepseek-reasoner", usages[2].Model)
	for _, usage := range usages[1:] {
		require.NotNil(t, usage.Balance, "deepseek model %q must carry a balance", usage.Model)
		assert.True(t, usage.Balance.IsAvailable)
		require.Len(t, usage.Balance.BalanceInfos, 2)
		cny := usage.Balance.BalanceInfos[0]
		assert.Equal(t, "CNY", cny.Currency)
		assert.Equal(t, "110.00", cny.TotalBalance)
		assert.Equal(t, "10.00", cny.GrantedBalance)
		assert.Equal(t, "100.00", cny.ToppedUpBalance)
		usd := usage.Balance.BalanceInfos[1]
		assert.Equal(t, "USD", usd.Currency)
		assert.Equal(t, "12.00", usd.TotalBalance)
		assert.Equal(t, 1.0, usage.Remaining)
	}
}

func TestGetModelUsages_WithDeepSeekLowBalance(t *testing.T) {
	// A CNY balance below the 20 threshold with no other currency above its
	// threshold must surface the low-balance warning fraction.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"is_available": true,
			"balance_infos": [
				{"currency": "CNY", "total_balance": "15.00", "granted_balance": "0.00", "topped_up_balance": "15.00"},
				{"currency": "USD", "total_balance": "0.00", "granted_balance": "0.00", "topped_up_balance": "0.00"}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"deepseek": {API: types.APIOpenAICompat, APIKey: "ds-key", BaseURL: server.URL},
		},
		Models: []config.ModelConfig{
			{ID: "deepseek-chat", Provider: "deepseek"},
		},
	}

	ctx := context.Background()
	usages, err := GetModelUsages(ctx, cfg, types.UsageOptions{})
	require.NoError(t, err)
	require.Len(t, usages, 1)

	require.NotNil(t, usages[0].Balance)
	assert.True(t, usages[0].Balance.IsAvailable)
	assert.Equal(t, lowBalanceRemaining, usages[0].Remaining, "low balance must surface the 20%% recharge warning")
}

func TestGetModelUsages_WithDeepSeekBalanceUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_available": false, "balance_infos": []}`))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"deepseek": {API: types.APIOpenAICompat, APIKey: "ds-key", BaseURL: server.URL},
		},
		Models: []config.ModelConfig{
			{ID: "deepseek-chat", Provider: "deepseek"},
		},
	}

	ctx := context.Background()
	usages, err := GetModelUsages(ctx, cfg, types.UsageOptions{})
	require.NoError(t, err)
	require.Len(t, usages, 1)

	require.NotNil(t, usages[0].Balance)
	assert.False(t, usages[0].Balance.IsAvailable)
	assert.Equal(t, 0.0, usages[0].Remaining, "empty balance must surface as exhausted quota")
}

func TestGetModelUsages_DeepSeekFetchErrorKeepsDefaults(t *testing.T) {
	// Server returns a non-200 so the balance fetch fails; usage must stay at
	// the fully-available default instead of failing or zeroing quota.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"deepseek": {API: types.APIOpenAICompat, APIKey: "bad-key", BaseURL: server.URL},
		},
		Models: []config.ModelConfig{
			{ID: "deepseek-chat", Provider: "deepseek"},
		},
	}

	ctx := context.Background()
	usages, err := GetModelUsages(ctx, cfg, types.UsageOptions{})
	require.NoError(t, err)
	require.Len(t, usages, 1)
	assert.Equal(t, 1.0, usages[0].Remaining)
	assert.Nil(t, usages[0].Balance)
}

func TestGetModelUsages_DeepSeekWithoutKeySkipsFetch(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"deepseek": {API: types.APIOpenAICompat, BaseURL: "https://api.deepseek.com"},
		},
		Models: []config.ModelConfig{
			{ID: "deepseek-chat", Provider: "deepseek"},
		},
	}

	ctx := context.Background()
	usages, err := GetModelUsages(ctx, cfg, types.UsageOptions{})
	require.NoError(t, err)
	require.Len(t, usages, 1)
	assert.Equal(t, 1.0, usages[0].Remaining)
	assert.Nil(t, usages[0].Balance)
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
