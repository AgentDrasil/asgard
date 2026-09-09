package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AgentDrasil/asgard/llms"
	"github.com/AgentDrasil/asgard/simplest/internal/config"
)

// defaultDeepSeekBaseURL is used when a deepseek provider config carries no baseUrl.
const defaultDeepSeekBaseURL = "https://api.deepseek.com"

// lowBalanceRemaining is the "remaining" fraction reported when the prepaid
// balance drops below a recharge reminder threshold, so quota UIs surface a
// visible low-quota warning.
const lowBalanceRemaining = 0.2

// lowBalanceThresholds maps a currency code to the minimum total balance
// (in that currency's units) below which the user should be reminded to
// top up the account.
var lowBalanceThresholds = map[string]float64{
	"CNY": 20,
	"USD": 5,
}

// isBalanceLow reports whether the balance can no longer fund comfortable
// usage in any currency: as long as ONE currency's total balance is at or
// above its recharge reminder threshold, no warning is raised. It only warns
// when every tracked currency is below its threshold (a fully zero balance
// counts as below). Unknown currencies and unparsable amounts are ignored.
func isBalanceLow(balance *llms.AccountBalance) bool {
	if balance == nil {
		return false
	}
	checked := 0
	for _, info := range balance.BalanceInfos {
		threshold, ok := lowBalanceThresholds[strings.ToUpper(strings.TrimSpace(info.Currency))]
		if !ok {
			continue
		}
		total, err := strconv.ParseFloat(strings.TrimSpace(info.TotalBalance), 64)
		if err != nil {
			continue
		}
		checked++
		if total >= threshold {
			return false
		}
	}
	return checked > 0
}

// deepSeekBalanceEndpoint is the DeepSeek API path that reports the account
// balance for the configured API key.
const deepSeekBalanceEndpoint = "/user/balance"

// deepSeekBalanceResponse mirrors the DeepSeek GET /user/balance response.
type deepSeekBalanceResponse struct {
	IsAvailable  bool               `json:"is_available"`
	BalanceInfos []llms.BalanceInfo `json:"balance_infos"`
}

// isDeepSeek reports whether the model belongs to the DeepSeek provider.
func isDeepSeek(provider, modelID string) bool {
	if strings.EqualFold(strings.TrimSpace(provider), "deepseek") {
		return true
	}
	id := strings.ToLower(strings.TrimSpace(modelID))
	return id == "deepseek" || strings.HasPrefix(id, "deepseek/")
}

// deepSeekEndpoint returns the API key and base URL configured for the
// deepseek provider (key may be empty when the provider is not configured).
func deepSeekEndpoint(cfg *config.Config) (apiKey, baseURL string) {
	if cfg != nil {
		for name, prov := range cfg.Providers {
			if strings.EqualFold(strings.TrimSpace(name), "deepseek") {
				return prov.APIKey, prov.BaseURL
			}
		}
	}
	return "", ""
}

// deepSeekBalanceURL derives the /user/balance endpoint from the provider's
// base URL. DeepSeek base URLs may carry a trailing /v1 prefix that must be
// stripped because the balance endpoint lives at the API root only.
func deepSeekBalanceURL(baseURL string) string {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = defaultDeepSeekBaseURL
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(strings.ToLower(base), "/v1") {
		base = base[:len(base)-len("/v1")]
	}
	return base + deepSeekBalanceEndpoint
}

// fetchDeepSeekBalance calls GET {baseURL}/user/balance with the provider's
// API key and returns the parsed account balance.
func fetchDeepSeekBalance(ctx context.Context, apiKey, baseURL string) (*llms.AccountBalance, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("empty deepseek api key")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, deepSeekBalanceURL(baseURL), nil)
	if err != nil {
		return nil, fmt.Errorf("create deepseek balance request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute deepseek balance request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deepseek balance error: status=%d", resp.StatusCode)
	}

	var payload deepSeekBalanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode deepseek balance response: %w", err)
	}

	return &llms.AccountBalance{
		IsAvailable:  payload.IsAvailable,
		BalanceInfos: payload.BalanceInfos,
	}, nil
}
