package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		apiKey    string
		opts      []Option
		wantErr   bool
		errSubstr string
	}{
		{
			name:      "empty api key",
			apiKey:    "",
			wantErr:   true,
			errSubstr: "api key is required",
		},
		{
			name:    "valid client with options",
			apiKey:  "test-api-key",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewClient(tt.apiKey, tt.opts...)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errSubstr)
				assert.Nil(t, client)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, client)
			assert.Equal(t, DefaultBaseURL, client.BaseURL())
			assert.Equal(t, DefaultModel, client.DefaultModel())
		})
	}
}

func TestNewClient_CustomOptions(t *testing.T) {
	t.Parallel()

	customBaseURL := "https://custom.typesafe.local"
	customModel := "jev-1.13.0"
	customHTTPClient := &http.Client{}

	client, err := NewClient("test-key",
		WithBaseURL(customBaseURL),
		WithDefaultModel(customModel),
		WithHTTPClient(customHTTPClient),
	)
	require.NoError(t, err)
	assert.Equal(t, customBaseURL, client.BaseURL())
	assert.Equal(t, customModel, client.DefaultModel())
}

func TestClient_Evaluate_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/systemone", r.URL.Path)
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req EvaluateRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)
		assert.Equal(t, "Help! My payouts have been failing for 3 days.", req.State)
		assert.Equal(t, "jev-latest", req.Model)
		assert.Len(t, req.Questions, 3)

		resp := EvaluateResponse{
			Model: "jev-1.13.0",
			Answers: map[string]Answer{
				"is_urgent": {
					Type: QuestionTypeNoul,
					Noul: 0.95,
				},
				"department": {
					Type:       QuestionTypeChoice,
					Choice:     "billing",
					Confidence: 0.81,
					Probabilities: map[string]float64{
						"billing":   0.88,
						"technical": 0.12,
						"sales":     0.0,
					},
				},
				"frustration": {
					Type:       QuestionTypeScore,
					Score:      1.05,
					Confidence: 0.92,
					Legend: map[string]string{
						"0": "Calm",
						"1": "Frustrated",
						"2": "Very angry",
					},
					Probabilities: map[string]float64{
						"0": 0.0,
						"1": 0.95,
						"2": 0.05,
					},
				},
			},
			Usage: Usage{
				InputTokens:  296,
				OutputTokens: 20,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	client, err := NewClient("test-api-key", WithBaseURL(server.URL))
	require.NoError(t, err)

	ctx := context.Background()
	req := EvaluateRequest{
		State: "Help! My payouts have been failing for 3 days.",
		Questions: map[string]Question{
			"is_urgent": NewNoul("Does this convey urgency?", NoulCriteria{
				True:  "Explicitly time-sensitive",
				False: "No urgency expressed",
			}),
			"department": NewChoice("Which team should handle this?", map[string]any{
				"billing":   "Payments, invoicing, refunds",
				"technical": "Bugs, outages, integrations",
				"sales":     "Pricing, upgrades, new accounts",
			}),
			"frustration": NewScore("How frustrated is the customer?", []any{
				"Calm", "Frustrated", "Very angry",
			}),
		},
	}

	resp, err := client.Evaluate(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "jev-1.13.0", resp.Model)
	assert.Equal(t, 296, resp.Usage.InputTokens)
	assert.Equal(t, 20, resp.Usage.OutputTokens)

	noulAns, ok := resp.Answers["is_urgent"]
	require.True(t, ok)
	assert.Equal(t, QuestionTypeNoul, noulAns.Type)
	assert.InDelta(t, 0.95, noulAns.Noul, 0.0001)

	choiceAns, ok := resp.Answers["department"]
	require.True(t, ok)
	assert.Equal(t, QuestionTypeChoice, choiceAns.Type)
	assert.Equal(t, "billing", choiceAns.Choice)
	assert.InDelta(t, 0.81, choiceAns.Confidence, 0.0001)

	scoreAns, ok := resp.Answers["frustration"]
	require.True(t, ok)
	assert.Equal(t, QuestionTypeScore, scoreAns.Type)
	assert.InDelta(t, 1.05, scoreAns.Score, 0.0001)
	assert.InDelta(t, 0.92, scoreAns.Confidence, 0.0001)
	assert.Equal(t, "Calm", scoreAns.Legend["0"])
}

func TestClient_Evaluate_Validation(t *testing.T) {
	t.Parallel()

	client, err := NewClient("test-key")
	require.NoError(t, err)

	ctx := context.Background()
	req := EvaluateRequest{
		State:     "test",
		Questions: map[string]Question{},
	}

	_, err = client.Evaluate(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "questions map cannot be empty")
}

func TestClient_Evaluate_APIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_api_key"}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient("bad-key", WithBaseURL(server.URL))
	require.NoError(t, err)

	ctx := context.Background()
	req := EvaluateRequest{
		State: "test",
		Questions: map[string]Question{
			"q": NewNoul("test"),
		},
	}

	_, err = client.Evaluate(ctx, req)
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
	assert.Contains(t, apiErr.Body, "invalid_api_key")
}

func TestClient_ListModels_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/models", r.URL.Path)
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))

		resp := ListModelsResponse{
			Models: []ModelInfo{
				{
					Name:        "jev-latest",
					Description: "Latest model",
					ReleaseDate: "2026-09-10T18:38:01.391457+00:00",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	client, err := NewClient("test-api-key", WithBaseURL(server.URL))
	require.NoError(t, err)

	ctx := context.Background()
	resp, err := client.ListModels(ctx)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Models, 1)
	assert.Equal(t, "jev-latest", resp.Models[0].Name)
}
