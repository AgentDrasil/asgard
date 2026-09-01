// Package zai provides utilities for interacting with the Z.AI API, including quota queries.
package zai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/AgentDrasil/asgard/llms"
)

const DefaultQuotaEndpoint = "https://api.z.ai/api/monitor/usage/quota/limit"

// Limit represents one quota limit item in the Z.AI API response.
type Limit struct {
	Type          string  `json:"type"`
	Usage         float64 `json:"usage"`
	Remaining     float64 `json:"remaining"`
	Percentage    float64 `json:"percentage"`
	NextResetTime int64   `json:"nextResetTime"`
}

type quotaResponse struct {
	Success bool   `json:"success"`
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Data    struct {
		Limits []Limit `json:"limits"`
	} `json:"data"`
}

// ParseLimits processes limits returned from Z.AI quota API into remaining fraction, refresh date, and breakdown.
func ParseLimits(limitsData []Limit, detailed bool) (float64, int64, []llms.QuotaLimit) {
	var remainingVal = 1.0
	var refreshDate int64 = 0
	var foundTokensLimit bool
	var foundAnyLimit bool

	var limits []llms.QuotaLimit
	for _, limit := range limitsData {
		remVal := 1.0 - (limit.Percentage / 100.0)
		if remVal < 0 {
			remVal = 0
		} else if remVal > 1.0 {
			remVal = 1.0
		}
		refDate := limit.NextResetTime / 1000

		if detailed {
			limits = append(limits, llms.QuotaLimit{
				Name:        limit.Type,
				Remaining:   remVal,
				RefreshDate: refDate,
			})
		}

		if limit.Type == "TOKENS_LIMIT" {
			if !foundTokensLimit || remVal < remainingVal {
				remainingVal = remVal
				refreshDate = refDate
				foundTokensLimit = true
			}
		} else if !foundTokensLimit && !foundAnyLimit {
			remainingVal = remVal
			refreshDate = refDate
			foundAnyLimit = true
		}
	}

	return remainingVal, refreshDate, limits
}

// FetchQuota calls the Z.AI quota endpoint with the given token.
func FetchQuota(ctx context.Context, token string, detailed bool, endpoint ...string) (float64, int64, []llms.QuotaLimit, error) {
	if token == "" {
		return 1.0, 0, nil, fmt.Errorf("empty zai token")
	}

	url := DefaultQuotaEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		url = endpoint[0]
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 1.0, 0, nil, fmt.Errorf("create zai quota request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 1.0, 0, nil, fmt.Errorf("execute zai quota request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var qr quotaResponse
	if err := json.NewDecoder(resp.Body).Decode(&qr); err != nil {
		return 1.0, 0, nil, fmt.Errorf("decode zai quota response: %w", err)
	}
	// A valid response from Z.AI satisfies at least one success indicator:
	// 1. qr.Success == true (explicit boolean success)
	// 2. qr.Code == 200 (standard HTTP-like success code)
	// 3. qr.Code == 0 with non-empty limits (some API versions return code 0 on success)
	isSuccess := qr.Success || qr.Code == 200 || (qr.Code == 0 && len(qr.Data.Limits) > 0)
	if !isSuccess {
		return 1.0, 0, nil, fmt.Errorf("zai quota error: code=%d msg=%s", qr.Code, qr.Msg)
	}

	rem, ref, limits := ParseLimits(qr.Data.Limits, detailed)
	return rem, ref, limits, nil
}
